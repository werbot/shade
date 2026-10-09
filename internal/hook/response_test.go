package hook_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/hook"
)

// The value carries a real placeholder token on purpose: with HTML escaping left on
// the angle brackets come back escaped, the literal token below is absent, and the
// test fails. So this asserts the escaping is off, not merely the shape.
func TestResponseBytesKeepsBracketsAndShape(t *testing.T) {
	r := hook.Response{
		HookSpecificOutput: &hook.Specific{
			HookEventName: hook.EventPreToolUse,
			UpdatedInput:  json.RawMessage(`{"command":"ssh <USER_1>@<HOST_1>"}`),
		},
	}
	b, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hookEventName":"PreToolUse"`) {
		t.Fatalf("shape: %s", b)
	}
	if !strings.Contains(string(b), `ssh <USER_1>@<HOST_1>`) {
		t.Fatalf("angle brackets must stay literal: %s", b)
	}
}

func TestResponseBytesIsEmptyForAnEmptyResponse(t *testing.T) {
	b, err := hook.Response{}.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("an empty response must print nothing, got %s", b)
	}
}
