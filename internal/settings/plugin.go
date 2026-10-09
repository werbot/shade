package settings

// hookTimeout is the per-hook timeout in seconds. It sits above the 5 s busy_timeout
// of the database, so a stuck engine is cut off before Claude Code gives up on the
// hook and keeps the session moving.
const hookTimeout = 15

// hookCommand is one command hook of the plugin. It uses the exec form (command
// plus args) and not a shell string: a binary path containing a space must not
// split into two commands.
type hookCommand struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
}

// hookGroup is a group of hooks of one event. The matcher key is omitted for the
// events that take none: MessageDisplay and the lifecycle events work without it,
// while PreToolUse and PostToolUse match every tool.
type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

// pluginEvents are the Claude Code events shade subscribes to. Only the tool events
// carry a matcher; MessageDisplay was verified on disk without one.
var pluginEvents = map[string]string{
	"SessionStart":     "",
	"UserPromptSubmit": "",
	"PreToolUse":       "*",
	"PostToolUse":      "*",
	"MessageDisplay":   "",
}

// PluginFiles returns the plugin shade installs, keyed by path relative to the
// plugin directory: the marketplace declaration, the plugin manifest and the hook
// definitions. The plugin directory is not an argument: none of the three files
// contains it — the binary path is absolute and source "./" is relative to the
// marketplace root — so where to write them is the caller's business.
func PluginFiles(shadeBin string) map[string][]byte {
	hook := hookCommand{Type: "command", Command: shadeBin, Args: []string{"hook"}, Timeout: hookTimeout}
	hooks := make(map[string][]hookGroup, len(pluginEvents))
	for event, matcher := range pluginEvents {
		hooks[event] = []hookGroup{{Matcher: matcher, Hooks: []hookCommand{hook}}}
	}

	docs := map[string]File{
		".claude-plugin/marketplace.json": {
			"name": MarketplaceName,
			"plugins": []any{
				map[string]any{"name": PluginName, "source": "./"},
			},
		},
		".claude-plugin/plugin.json": {"name": PluginName},
		"hooks/hooks.json":           {"hooks": hooks},
	}

	files := make(map[string][]byte, len(docs))
	for name, doc := range docs {
		b, err := doc.Bytes()
		if err != nil {
			// The documents are built from constants right here, so a marshal
			// failure is a bug in shade, not a condition the caller could handle.
			panic(err)
		}
		files[name] = b
	}
	return files
}
