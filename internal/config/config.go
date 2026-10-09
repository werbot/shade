// Package config — the layered shade config: defaults, global file, project file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// FailOpenLog is the fail_policy value that lets an answer through with the values
// that could be restored and a warning about the tokens that could not. Every other
// value — including a missing one — is fail_closed. The CLI and the hook both compare
// FailPolicy against this constant: a bare literal in either place would drift from
// the other on a rename and fail in the unsafe direction.
const FailOpenLog = "fail_open_log"

// Config — the settings read so far. There are exactly as many fields as there are
// consumers: FailPolicy is read by the CLI (fail_open_log turns exit code 3 into
// a warning), EntitiesTTL — entities prune, PromptGate — the UserPromptSubmit gate.
// The remaining keys of the spec (stream_mode, upstream, api_key_env, [categories])
// will arrive together with their consumers; toml.Unmarshal ignores unknown keys, so
// a file with them right now will not break.
type Config struct {
	FailPolicy  string `toml:"fail_policy"`
	EntitiesTTL string `toml:"entities_ttl"`
	PromptGate  string `toml:"prompt_gate"`
}

// Default returns the values in effect when no file has set a field.
func Default() Config {
	return Config{FailPolicy: "fail_closed", EntitiesTTL: "90d", PromptGate: "off"}
}

// Load assembles the config from three layers: defaults, the global file home/config.toml,
// then the project projectRoot/.shade.toml. Each layer is parsed into an already
// filled structure, so what the project sets overrides the global value, and
// an untouched field keeps the value of the previous layer — telling "not set" apart
// from "set to zero" is not needed. A missing file is a skipped layer, not an error.
//
// home comes from store.Home() (config knows nothing about SHADE_HOME) and may be
// empty if the home directory is not defined.
func Load(home, projectRoot string) (Config, error) {
	c := Default()
	layers := []struct{ dir, name string }{
		{home, "config.toml"},
		{projectRoot, ".shade.toml"},
	}
	for _, l := range layers {
		// An empty directory means skipping the layer: filepath.Join("", name) would point at
		// the current directory rather than a non-existent one.
		if l.dir == "" {
			continue
		}
		path := filepath.Join(l.dir, l.name)
		if err := mergeFile(&c, path); err != nil {
			return Config{}, err
		}
		// Validated per layer, not after the merge: only here is the file the bad value
		// came from still known. A typo in the global file stays an error even when the
		// project file overrides it — the typo is real, and a gate that guards a leak fails loud.
		switch c.PromptGate {
		case "off", "on", "auto":
		default:
			return Config{}, fmt.Errorf("config %s: prompt_gate: %q is not off, on or auto", path, c.PromptGate)
		}
	}
	return c, nil
}

// PromptGateEnabled reports whether the UserPromptSubmit gate must block a prompt
// with sensitive content. "auto" means "unless a proxy is active": the check for an
// active proxy appears in phase 4 together with shade serve, so until then auto is
// off, and the default is off for the same reason.
func (c Config) PromptGateEnabled() bool { return c.PromptGate == "on" }

// mergeFile parses one layer into c. The path in the error is mandatory: there are two configs, and
// without it the user will not understand which one to fix.
func mergeFile(c *Config, path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	if err := toml.Unmarshal(b, c); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// MaxAgeDays is how many days fit in a time.Duration. Beyond that n*24h
// overflows int64 and gives a negative age.
const MaxAgeDays = int64(math.MaxInt64) / int64(24*time.Hour)

// ParseAge converts an age like 30d into a duration. The unit d is a day: ages in
// the CLI are given in days (`--older-than 30d`, `--since 7d`, `entities_ttl`), while
// time.ParseDuration does not know such a unit.
//
// The overflow is checked explicitly, rather than relying on "nobody will
// write such an age": a negative age in PruneEntities turns into a boundary in
// the future, and `--older-than 200000d` would wipe all entities of the project — the values
// live only in value_enc, so the loss cannot be rolled back.
func ParseAge(s string) (time.Duration, error) {
	days, ok := strings.CutSuffix(s, "d")
	if !ok {
		return 0, fmt.Errorf("age %q: expected a number of days with a d suffix, for example 30d", s)
	}
	n, err := strconv.Atoi(days)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("age %q: expected a number of days with a d suffix, for example 30d", s)
	}
	if int64(n) > MaxAgeDays {
		return 0, fmt.Errorf("age %q: too large, %dd is the maximum", s, MaxAgeDays)
	}
	return time.Duration(n) * 24 * time.Hour, nil
}
