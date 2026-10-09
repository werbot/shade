// Package hook is the Claude Code hooks adapter: it parses the JSON payload Claude
// Code writes to a hook on stdin and renders the hook's answer on stdout. The hook
// never prints values — only tokens, types and rule names.
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Event names as Claude Code spells them in hook_event_name.
const (
	EventSessionStart     = "SessionStart"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventMessageDisplay   = "MessageDisplay"
)

// Event is the payload of a hook invocation. Only the fields the handlers read are
// kept: the key sets of every event were verified against a real session, and a
// field nobody uses would be a guess. ToolInput and ToolResponse stay raw bytes so
// the walker can hand them back byte-for-byte.
type Event struct {
	Name         string          `json:"hook_event_name"`
	CWD          string          `json:"cwd"`
	Prompt       string          `json:"prompt"`
	ToolName     string          `json:"tool_name"`
	Delta        string          `json:"delta"`
	Final        bool            `json:"final"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

// ParseEvent reads one hook payload. Empty or malformed JSON is an error, and so is
// a payload without hook_event_name: an event with an empty name would silently fall
// through the handler table. ToolName is not normalized — the skip list in the
// PreToolUse handler compares it case-sensitively.
func ParseEvent(raw []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return Event{}, fmt.Errorf("hook payload: %w", err)
	}
	if ev.Name == "" {
		return Event{}, errors.New("hook payload: hook_event_name is missing")
	}
	return ev, nil
}
