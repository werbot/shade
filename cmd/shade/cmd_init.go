package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/werbot/shade/internal/settings"
	"github.com/werbot/shade/internal/skills"
	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "init",
		Help: "install the claude code hooks and skills",
		Run:  runInit,
	})
}

// tempBinaryWarning names the one silent failure of the install: a hook pointing at
// a binary in a temporary build directory stops working as soon as that directory
// is cleaned, and nothing in the session says so.
const tempBinaryWarning = "warning: the shade binary lies in a temporary build directory; a hook pointing at it stops working once that directory is cleaned — install shade to a stable path"

// keepOldWarning explains the consequence of --keep-old-hook: both hooks rewrite the
// same PostToolUse event, and the order between them is not defined.
const keepOldWarning = "warning: --keep-old-hook keeps the old PostToolUse hook; two rewrites on the same event are non-deterministic"

// runInit installs the plugin into the state directory of shade and registers it
// with Claude Code. Two settings files are involved and they are not
// interchangeable: the marketplace declaration is read only from the user settings,
// while enabledPlugins goes to the project ones unless --global asks otherwise.
func runInit(args []string, stdio IO) int {
	var global, dryRun, keepOld bool
	pos, err := parseFlags(args, nil, map[string]*bool{
		"--global":        &global,
		"--dry-run":       &dryRun,
		"--keep-old-hook": &keepOld,
	})
	if err != nil {
		return fail(stdio, "init", 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, "init", 2, err)
	}

	// The user home is resolved before anything else: it is the only path here that
	// can be missing, and store.Home() falls back to it as well.
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fail(stdio, "init", 1, fmt.Errorf("locating the user home: %w", err))
	}
	bin, err := os.Executable()
	if err != nil {
		return fail(stdio, "init", 1, fmt.Errorf("locating the shade binary: %w", err))
	}
	if inTempBuildDir(bin) {
		fmt.Fprintln(stdio.Out, tempBinaryWarning)
	}

	// The plugin is shade's own state and lives under SHADE_HOME; the marketplace
	// declaration points at this directory.
	pluginDir := filepath.Join(store.Home(), "claude")
	if err := installPlugin(stdio, pluginDir, bin, dryRun); err != nil {
		return fail(stdio, "init", 1, err)
	}

	userPath := filepath.Join(userHome, ".claude", "settings.json")
	user, err := settings.Load(userPath)
	if err != nil {
		return fail(stdio, "init", 1, err)
	}
	user.AddMarketplace(settings.MarketplaceName, pluginDir)
	if keepOld {
		fmt.Fprintln(stdio.Out, keepOldWarning)
	} else {
		user.RemoveHooks("PostToolUse", isOldHook)
	}

	// Claude Code spells the plugin id plugin-id@marketplace-id.
	pluginID := settings.PluginName + "@" + settings.MarketplaceName
	projectPath := ""
	if global {
		user.EnablePlugin(pluginID, true)
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return fail(stdio, "init", 1, fmt.Errorf("working directory: %w", err))
		}
		// The git root, not the current directory: Claude Code reads the project
		// settings only from the directory it was launched in, and the project of
		// shade is the repository. Settings written into a subdirectory would never
		// be read by a session started at the root — the plugin would stay off with
		// no error anywhere.
		projectPath = filepath.Join(store.ProjectRoot(context.Background(), wd), ".claude", "settings.json")
	}
	if err := applySettings(stdio, userPath, user, dryRun); err != nil {
		return fail(stdio, "init", 1, err)
	}

	if projectPath != "" {
		project, err := settings.Load(projectPath)
		if err != nil {
			return fail(stdio, "init", 1, err)
		}
		project.EnablePlugin(pluginID, true)
		if err := applySettings(stdio, projectPath, project, dryRun); err != nil {
			return fail(stdio, "init", 1, err)
		}
	}
	return 0
}

// installPlugin writes the plugin files under dir — the hooks and manifest from
// settings, the skills from internal/skills — showing the diff of each against what
// is already on disk. A missing file counts as fully added, which is what a first
// install should look like.
func installPlugin(stdio IO, dir, shadeBin string, dryRun bool) error {
	files := settings.PluginFiles(shadeBin)
	maps.Copy(files, skills.Files())
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, name)
		before, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if !printDiff(stdio.Out, path, before, files[name]) || dryRun {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// applySettings prints the diff of the settings file and saves it unless dryRun.
// The diff is taken against the bytes on disk, not against the parsed file: a file
// that does not exist yet must read as created, not as an edit of "{}".
func applySettings(stdio IO, path string, f settings.File, dryRun bool) error {
	before, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	after, err := f.Bytes()
	if err != nil {
		return err
	}
	if !printDiff(stdio.Out, path, before, after) || dryRun {
		return nil
	}
	return f.Save(path)
}

// printDiff prints the diff of one file and reports whether there was anything to
// print. An unchanged file stays silent: a second init must read as a no-op.
func printDiff(w io.Writer, path string, before, after []byte) bool {
	d := settings.Diff(before, after)
	if d == "" {
		return false
	}
	fmt.Fprintf(w, "%s:\n%s", path, d)
	return true
}

// isOldHook matches the hook shade replaces: the redact_output.py of the previous
// installation. A substring and not the whole command, because the user's path to
// it is spelled differently (~/… against an absolute path).
func isOldHook(command string) bool {
	return strings.Contains(command, "redact_output.py")
}

// inTempBuildDir reports whether a binary path lies in a temporary build directory:
// `go run` and `go test` place the binary under $TMPDIR/go-buildNNN/b001.
func inTempBuildDir(path string) bool {
	return strings.Contains(path, string(filepath.Separator)+"go-build")
}
