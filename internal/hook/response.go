package hook

import (
	"bytes"
	"encoding/json"
)

// Response is the answer a hook prints on stdout. An empty Response is the hook
// staying silent, which is not the same as answering with {}: Claude Code treats a
// printed object as a decision, so silence must stay silence.
type Response struct {
	HookSpecificOutput *Specific `json:"hookSpecificOutput,omitempty"`
	Decision           string    `json:"decision,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	SystemMessage      string    `json:"systemMessage,omitempty"`
}

// Specific is the per-event part of the answer. HookEventName is always set: it is
// how Claude Code routes the block back to the event that produced it.
type Specific struct {
	HookEventName            string          `json:"hookEventName"`
	AdditionalContext        string          `json:"additionalContext,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
	UpdatedToolOutput        json.RawMessage `json:"updatedToolOutput,omitempty"`
	DisplayContent           string          `json:"displayContent,omitempty"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
}

// Bytes renders the response. An empty response returns nil, not "{}": see the
// Response doc comment. HTML escaping is off because the answer carries placeholder
// tokens, and <USER_1> is no longer a token the model can pass on.
func (r Response) Bytes() ([]byte, error) {
	if r.HookSpecificOutput == nil && r.Decision == "" && r.Reason == "" && r.SystemMessage == "" {
		return nil, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
