package hook_test

import (
	"testing"

	"github.com/werbot/shade/internal/hook"
)

func TestParseEventReadsCommonAndEventFields(t *testing.T) {
	raw := []byte(`{"hook_event_name":"PreToolUse","cwd":"/repo","session_id":"s1",
		"tool_name":"Bash","tool_input":{"command":"ssh <HOST_1>"}}`)
	ev, err := hook.ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != hook.EventPreToolUse || ev.CWD != "/repo" || ev.SessionID != "s1" || ev.ToolName != "Bash" {
		t.Fatalf("event: %+v", ev)
	}
	if string(ev.ToolInput) != `{"command":"ssh <HOST_1>"}` {
		t.Fatalf("tool_input kept verbatim: %s", ev.ToolInput)
	}
}

func TestParseEventRejectsJunk(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"hook_event_name":42}`} {
		if _, err := hook.ParseEvent([]byte(raw)); err == nil {
			t.Fatalf("%q must not parse", raw)
		}
	}
}

func TestParseEventRejectsAMissingName(t *testing.T) {
	if _, err := hook.ParseEvent([]byte(`{"cwd":"/repo"}`)); err == nil {
		t.Fatal("a payload without hook_event_name must not become an event with an empty name")
	}
}

func TestParseEventReadsMessageDisplayFrames(t *testing.T) {
	ev, err := hook.ParseEvent([]byte(`{"hook_event_name":"MessageDisplay","delta":"ssh <HOST_1","final":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != hook.EventMessageDisplay || ev.Delta != "ssh <HOST_1" || ev.Final {
		t.Fatalf("event: %+v", ev)
	}
}
