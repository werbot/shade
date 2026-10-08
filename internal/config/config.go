// Package config — the layered shade config: defaults, global file, project file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config — the settings read in phase 1. There are exactly as many fields as there are
// consumers: FailPolicy is read by the CLI (fail_open_log turns exit code 3 into
// a warning), EntitiesTTL — entities prune. The remaining keys of the spec
// (stream_mode, upstream, api_key_env, [categories]) will arrive together with their
// consumers; toml.Unmarshal ignores unknown keys, so a file with
// them right now will not break.
type Config struct {
	FailPolicy  string `toml:"fail_policy"`
	EntitiesTTL string `toml:"entities_ttl"`
}

// Default returns the values in effect when no file has set a field.
func Default() Config {
	return Config{FailPolicy: "fail_closed", EntitiesTTL: "90d"}
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
		if err := mergeFile(&c, filepath.Join(l.dir, l.name)); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

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
