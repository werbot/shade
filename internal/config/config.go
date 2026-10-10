// Package config — the layered shade config: defaults, global file, project file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
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

// The values of the proxy keys, and their defaults. StreamMode selects how shade serve
// relays the upstream answer — incremental streams it, buffered waits for the whole body,
// which is the fallback for a client that cannot handle a stream. Upstream is the real
// endpoint the proxy forwards to. APIKeyEnv names the environment variable holding the
// key the proxy substitutes for the client's; an empty value is legal and means "forward
// the client's own credentials untouched".
const (
	StreamIncremental = "incremental"
	StreamBuffered    = "buffered"

	PromptGateOff  = "off"
	PromptGateOn   = "on"
	PromptGateAuto = "auto"

	DefaultUpstream   = "https://api.anthropic.com"
	DefaultAPIKeyEnv  = "ANTHROPIC_API_KEY"
	DefaultStreamMode = StreamIncremental
)

// Config — the settings read so far. There are exactly as many fields as there are
// consumers: FailPolicy is read by the CLI (fail_open_log turns exit code 3 into
// a warning), EntitiesTTL — entities prune, PromptGate — the UserPromptSubmit gate,
// StreamMode, Upstream and APIKeyEnv — shade serve. The remaining key of the spec
// ([categories]) will arrive together with its consumer; toml.Unmarshal ignores unknown
// keys, so a file with it right now will not break.
type Config struct {
	FailPolicy  string `toml:"fail_policy"`
	EntitiesTTL string `toml:"entities_ttl"`
	PromptGate  string `toml:"prompt_gate"`
	StreamMode  string `toml:"stream_mode"`
	Upstream    string `toml:"upstream"`
	APIKeyEnv   string `toml:"api_key_env"`
}

// Default returns the values in effect when no file has set a field.
func Default() Config {
	return Config{
		FailPolicy:  "fail_closed",
		EntitiesTTL: "90d",
		PromptGate:  PromptGateOff,
		StreamMode:  DefaultStreamMode,
		Upstream:    DefaultUpstream,
		APIKeyEnv:   DefaultAPIKeyEnv,
	}
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
		case PromptGateOff, PromptGateOn, PromptGateAuto:
		default:
			return Config{}, fmt.Errorf("config %s: prompt_gate: %q is not %s, %s or %s", path, c.PromptGate, PromptGateOff, PromptGateOn, PromptGateAuto)
		}
		// A misspelled stream_mode is an error rather than a silent fallback: the mode
		// decides whether shade serve buffers the answer, and a wrong guess changes behaviour.
		switch c.StreamMode {
		case StreamIncremental, StreamBuffered:
		default:
			return Config{}, fmt.Errorf("config %s: stream_mode: %q is not %s or %s", path, c.StreamMode, StreamIncremental, StreamBuffered)
		}
		// The target must carry a scheme and a host: without a scheme the forwarder cannot
		// pick a transport, and a bare host would be read as a relative path, not an endpoint.
		if u, err := url.Parse(c.Upstream); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return Config{}, fmt.Errorf("config %s: upstream: %q must be an absolute http(s) url with a host", path, c.Upstream)
		}
	}
	return c, nil
}

// PromptGateEnabled reports whether the UserPromptSubmit gate must block a prompt with
// sensitive content. "auto" means "unless a proxy is active": proxyCovers is the caller's
// answer to "does shade serve wrap this traffic?" — with the original anonymized on the
// way out, auto has nothing left to block. The explicit on and off ignore the proxy.
func (c Config) PromptGateEnabled(proxyCovers bool) bool {
	switch c.PromptGate {
	case PromptGateOn:
		return true
	case PromptGateAuto:
		return !proxyCovers
	default:
		return false
	}
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

// maxAgeDays is how many days fit in a time.Duration. Beyond that n*24h
// overflows int64 and gives a negative age.
const maxAgeDays = int64(math.MaxInt64) / int64(24*time.Hour)

// ParseAge converts an age like 30d into a duration. The unit d is a day: ages are
// given in days both by the CLI (`--older-than 30d`, `--since 7d`, `entities_ttl`) and
// by the MCP `unresolved_report` tool (`since`), while time.ParseDuration does not
// know such a unit.
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
	if int64(n) > maxAgeDays {
		return 0, fmt.Errorf("age %q: too large, %dd is the maximum", s, maxAgeDays)
	}
	return time.Duration(n) * 24 * time.Hour, nil
}
