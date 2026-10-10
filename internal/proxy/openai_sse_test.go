package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/placeholder"
)

// openAIChunk renders one chat-completions stream frame the way the upstream sends it: a data
// line and its terminating blank line. OpenAI has no event: line.
func openAIChunk(data string) string {
	return "data: " + data + "\n\n"
}

// openAIDone is the terminator frame.
const openAIDone = "data: [DONE]\n\n"

// runOpenAIPipe drives one stream through the pipe in the default (incremental) mode.
func runOpenAIPipe(t *testing.T, s *Server, eng Engine, src string) (string, error) {
	t.Helper()
	return runOpenAIPipeMode(t, s, eng, config.StreamIncremental, src)
}

// runOpenAIPipeMode drives one stream through the pipe in the given mode.
func runOpenAIPipeMode(t *testing.T, s *Server, eng Engine, mode, src string) (string, error) {
	t.Helper()
	var dst streamRecorder
	err := s.pipeOpenAISSE(context.Background(), eng, streamCfg(t, s, mode), strings.NewReader(src), &dst)
	return dst.String(), err
}

// chunkDelta builds one choice frame whose delta carries the given fields.
func chunkDelta(fields string) string {
	return openAIChunk(`{"choices":[{"index":0,"delta":{` + fields + `},"finish_reason":null}]}`)
}

// finishChunk is the frame that ends a choice, carrying a non-empty finish_reason.
func finishChunk(reason string) string {
	return openAIChunk(`{"choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}`)
}

// openAIDeltaContent concatenates every choices[0].delta.content the client received.
func openAIDeltaContent(out string) string {
	var sb strings.Builder
	for _, frame := range outputFrames(out) {
		data, ok := parseFrameData(frame)
		if !ok {
			continue
		}
		choices, _ := data["choices"].([]any)
		for _, c := range choices {
			delta, _ := c.(map[string]any)["delta"].(map[string]any)
			if s, ok := delta["content"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// openAIToolArgChunks returns, in order, every non-empty tool-call arguments string the client
// received, with the frame index it arrived in.
func openAIToolArgChunks(out string) []struct {
	at   int
	args string
} {
	var got []struct {
		at   int
		args string
	}
	for i, frame := range outputFrames(out) {
		data, ok := parseFrameData(frame)
		if !ok {
			continue
		}
		choices, _ := data["choices"].([]any)
		for _, c := range choices {
			delta, _ := c.(map[string]any)["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, tc := range calls {
				fn, _ := tc.(map[string]any)["function"].(map[string]any)
				if args, ok := fn["arguments"].(string); ok && args != "" {
					got = append(got, struct {
						at   int
						args string
					}{at: i, args: args})
				}
			}
		}
	}
	return got
}

// finishFrameAt returns the index of the first frame carrying a non-empty finish_reason.
func finishFrameAt(out string) int {
	for i, frame := range outputFrames(out) {
		data, ok := parseFrameData(frame)
		if !ok {
			continue
		}
		choices, _ := data["choices"].([]any)
		for _, c := range choices {
			if fr, _ := c.(map[string]any)["finish_reason"].(string); fr != "" {
				return i
			}
		}
	}
	return -1
}

// firstChoiceDelta returns choices[0].delta of a frame, or nil.
func firstChoiceDelta(frame sseFrame) map[string]any {
	data, ok := parseFrameData(frame)
	if !ok {
		return nil
	}
	choices, _ := data["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	return delta
}

// TestOpenAISSEStreamsContentThroughTheFlusher pins the safe-boundary flush for delta.content: a
// placeholder split between two chunks at any position is restored whole, and no raw fragment
// reaches the client. Restore per chunk instead of through the flusher and the middle splits stay
// broken.
func TestOpenAISSEStreamsContentThroughTheFlusher(t *testing.T) {
	const token = "<HOST_1>"
	const value = "db.prod.local"
	for split := 0; split <= len(token); split++ {
		t.Run(strconv.Itoa(split), func(t *testing.T) {
			src := chunkDelta(`"content":"a`+token[:split]+`"`) +
				chunkDelta(`"content":"`+token[split:]+`b"`) +
				finishChunk("stop") + openAIDone
			out, err := runOpenAIPipe(t, walkerServer(t, nil), &answerEngine{replace: [][2]string{{token, value}}}, src)
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			if got := openAIDeltaContent(out); got != "a"+value+"b" {
				t.Errorf("restored content = %q, want %q", got, "a"+value+"b")
			}
			if strings.Contains(out, "<HOST_1") {
				t.Errorf("a raw placeholder fragment reached the client: %q", out)
			}
		})
	}
}

// TestOpenAISSEBuffersToolArgumentsUntilFinishReason pins the tool-call path: argument fragments
// are never forwarded as they arrive — they come out as one restored chunk, immediately before
// the frame carrying the choice's finish_reason, which is OpenAI's end signal (there is no
// content_block_stop). Emit them as they arrive and several fragments appear before the finish.
func TestOpenAISSEBuffersToolArgumentsUntilFinishReason(t *testing.T) {
	const value = "db.prod.local"
	src := chunkDelta(`"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\":\"ssh "}}]`) +
		chunkDelta(`"tool_calls":[{"index":0,"function":{"arguments":"<HOST_1>"}}]`) +
		chunkDelta(`"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]`) +
		finishChunk("tool_calls") + openAIDone
	out, err := runOpenAIPipe(t, walkerServer(t, nil), &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	if strings.Contains(out, "<HOST_1") {
		t.Errorf("the raw token reached the client: %q", out)
	}
	args := openAIToolArgChunks(out)
	if len(args) != 1 {
		t.Fatalf("non-empty arguments chunks = %d, want exactly 1 restored chunk", len(args))
	}
	finish := finishFrameAt(out)
	if finish == -1 {
		t.Fatal("no finish_reason frame was emitted")
	}
	if args[0].at >= finish {
		t.Errorf("arguments chunk at %d, finish_reason at %d, want the arguments before it", args[0].at, finish)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args[0].args), &parsed); err != nil {
		t.Fatalf("restored arguments are not valid JSON: %q: %v", args[0].args, err)
	}
	if parsed["cmd"] != "ssh "+value {
		t.Errorf("restored cmd = %v, want %q", parsed["cmd"], "ssh "+value)
	}
}

// TestOpenAISSEBufferedMergesContentBeforeFinish pins buffered mode: content deltas are collected
// per choice and emitted as one restored chunk before the finish_reason frame, so a fail-closed
// refusal is possible. Emit each delta as it arrives and more than one content chunk appears.
func TestOpenAISSEBufferedMergesContentBeforeFinish(t *testing.T) {
	const value = "db.prod.local"
	src := chunkDelta(`"content":"a<HOST_1"`) +
		chunkDelta(`"content":">b"`) +
		finishChunk("stop") + openAIDone
	out, err := runOpenAIPipeMode(t, walkerServer(t, nil), &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, config.StreamBuffered, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	if got := openAIDeltaContent(out); got != "a"+value+"b" {
		t.Errorf("merged content = %q, want %q", got, "a"+value+"b")
	}
	if strings.Contains(out, "<HOST_1") {
		t.Errorf("a raw placeholder fragment reached the client: %q", out)
	}
	finish := finishFrameAt(out)
	if finish == -1 {
		t.Fatal("no finish_reason frame was emitted")
	}
	content := -1
	for i, frame := range outputFrames(out) {
		if s, _ := firstChoiceDelta(frame)["content"].(string); s != "" {
			if content != -1 {
				t.Errorf("more than one non-empty content chunk was emitted")
			}
			content = i
		}
	}
	if content == -1 || content >= finish {
		t.Errorf("merged content chunk at %d, finish_reason at %d, want the content before it", content, finish)
	}
}

// TestOpenAISSEBufferedRefusesUnderFailClosed pins the buffered refusal: an unresolved token in
// the collected content emits one data frame carrying the refusal body, then data: [DONE], and no
// finish_reason frame; the token never reaches the client and a blocked row is journalled. Skip
// the refuseUnresolved call and the restored text with the raw token goes out instead.
func TestOpenAISSEBufferedRefusesUnderFailClosed(t *testing.T) {
	const token = "<HOST_1>"
	eng := &answerEngine{unresolved: []placeholder.Token{{Type: "HOST", Raw: token}}}
	src := chunkDelta(`"content":"the host is `+token+`"`) +
		finishChunk("stop") + openAIDone
	out, err := runOpenAIPipeMode(t, walkerServer(t, nil), eng, config.StreamBuffered, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	if strings.Contains(out, token) {
		t.Errorf("the refusal leaked the token: %q", out)
	}
	if finish := finishFrameAt(out); finish != -1 {
		t.Errorf("a finish_reason frame was emitted after a refusal: %q", out)
	}
	want := openAIChunk(string(errorBody("shade_unresolved", "1 placeholders could not be restored: HOST"))) + openAIDone
	if !strings.Contains(out, want) {
		t.Errorf("output = %q, want the refusal then [DONE]: %q", out, want)
	}
	if got := eng.blockedTypes(); len(got) != 1 || got[0] != "HOST" {
		t.Errorf("blocked types = %v, want one HOST row", got)
	}
}

// TestOpenAISSEForwardsDoneLast pins the terminator: data: [DONE] is the last thing the client
// sees, and a held unclosed "<" tail is dropped — a raw token never may reach the client — with
// the loss reported. Forward a held tail and the raw fragment appears; drop the [DONE] handling
// and it is no longer last.
func TestOpenAISSEForwardsDoneLast(t *testing.T) {
	var diag bytes.Buffer
	src := chunkDelta(`"content":"hi <HOST"`) +
		finishChunk("stop") + openAIDone
	out, err := runOpenAIPipe(t, walkerServer(t, &diag), &answerEngine{replace: [][2]string{{"<HOST_1>", "db.prod.local"}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	frames := outputFrames(out)
	if len(frames) == 0 {
		t.Fatal("no frames were emitted")
	}
	if last := string(frames[len(frames)-1].data); last != "[DONE]" {
		t.Errorf("last frame data = %q, want [DONE]", last)
	}
	if strings.Contains(out, "<HOST") {
		t.Errorf("the raw placeholder fragment reached the client: %q", out)
	}
	if line := diag.String(); !strings.Contains(line, "withheld") {
		t.Errorf("diag = %q, want the withheld tail reported", line)
	}
}

// TestOpenAISSEForwardsAnUnparseableFrame pins ruling 5: a data line that is not [DONE] and not
// parseable JSON — a gateway's own error page inside the stream — is forwarded as it came, not
// mangled or dropped.
func TestOpenAISSEForwardsAnUnparseableFrame(t *testing.T) {
	const page = "<html><body>502 Bad Gateway</body></html>"
	src := openAIChunk(page) + openAIDone
	out, err := runOpenAIPipe(t, walkerServer(t, nil), &answerEngine{}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if !strings.Contains(out, openAIChunk(page)) {
		t.Errorf("output = %q, want the unparseable frame forwarded as it came", out)
	}
}

// TestBrokenOpenAIStreamEndsWithARefusalFrame is the OpenAI half of the empty-200 problem: a
// stream that stops before [DONE] must not leave the client with one that simply ends. The
// break is stated in the only frame the format has for it — the refusal body as a data frame,
// followed by [DONE] — and the held tail is still dropped on the way out.
func TestBrokenOpenAIStreamEndsWithARefusalFrame(t *testing.T) {
	src := chunkDelta(`"role":"assistant","content":"the host is <HOST_1"`) // no [DONE]
	s := walkerServer(t, nil)
	w := httptest.NewRecorder()
	s.streamSSE(context.Background(), &answerEngine{replace: [][2]string{{"<HOST_1>", "db.prod.local"}}},
		s.openaiShape(), streamCfg(t, s, config.StreamIncremental), streamResponse(src), w)

	got := w.Body.String()
	body := string(errorBody("shade_stream_incomplete", "the upstream stream ended before it was complete"))
	if !strings.HasSuffix(got, "data: "+body+"\n\n"+openAIDone) {
		t.Fatalf("output = %q, want it to end with the refusal frame and [DONE]", got)
	}
	if !strings.Contains(got, "the host is ") {
		t.Errorf("output = %q, want the restored text the upstream did send", got)
	}
	if strings.Contains(got, "<HOST_1") {
		t.Errorf("a raw placeholder fragment reached the client: %q", got)
	}
}

// TestCompleteOpenAIStreamGetsNoRefusalFrame is the negative: a stream that reached [DONE] ends
// where the upstream ended it.
func TestCompleteOpenAIStreamGetsNoRefusalFrame(t *testing.T) {
	stream := chunkDelta(`"role":"assistant","content":"hello"`) + finishChunk("stop") + openAIDone
	s := walkerServer(t, nil)
	w := httptest.NewRecorder()
	s.streamSSE(context.Background(), &answerEngine{}, s.openaiShape(),
		streamCfg(t, s, config.StreamIncremental), streamResponse(stream), w)
	if got := w.Body.String(); got != stream {
		t.Errorf("output = %q, want the stream untouched %q", got, stream)
	}
}

// TestRefusedOpenAIStreamIsNotDoubled pins the other negative: a buffered refusal already wrote
// the refusal frame and [DONE], so nothing may follow them.
func TestRefusedOpenAIStreamIsNotDoubled(t *testing.T) {
	const token = "<HOST_1>"
	src := chunkDelta(`"role":"assistant","content":"the host is `+token+`"`) +
		finishChunk("stop") // no [DONE] after the refusal point
	s := walkerServer(t, nil)
	w := httptest.NewRecorder()
	eng := &answerEngine{unresolved: []placeholder.Token{{Type: "HOST", Raw: token}}}
	s.streamSSE(context.Background(), eng, s.openaiShape(),
		streamCfg(t, s, config.StreamBuffered), streamResponse(src), w)

	got := w.Body.String()
	if n := strings.Count(got, string(errorBody("shade_unresolved", "1 placeholders could not be restored: HOST"))); n != 1 {
		t.Errorf("refusal frames = %d, want exactly 1: %q", n, got)
	}
	if strings.Contains(got, "shade_stream_incomplete") {
		t.Errorf("a refusal must not be followed by a broken-stream frame: %q", got)
	}
	if strings.Contains(got, token) {
		t.Errorf("the refusal leaked the token: %q", got)
	}
}
