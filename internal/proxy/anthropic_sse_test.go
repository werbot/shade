package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// sseEvent renders one server-sent event the way the upstream does.
func sseEvent(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// frameWithDelta builds a content_block_delta frame carrying delta at index.
func frameWithDelta(t *testing.T, index int, delta map[string]any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
	if err != nil {
		t.Fatal(err)
	}
	return sseEvent("content_block_delta", string(data))
}

func textDeltaFrame(t *testing.T, index int, text string) string {
	t.Helper()
	return frameWithDelta(t, index, map[string]any{"type": "text_delta", "text": text})
}

func inputJSONDeltaFrame(t *testing.T, index int, partial string) string {
	t.Helper()
	return frameWithDelta(t, index, map[string]any{"type": "input_json_delta", "partial_json": partial})
}

// streamRecorder is the dst stand-in: it keeps the bytes written and counts flushes.
type streamRecorder struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	flushes int
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *streamRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
}

func (r *streamRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// runPipe drives one stream through the pipe and returns what reached the client.
func runPipe(t *testing.T, s *Server, eng Engine, src string) (string, error) {
	t.Helper()
	var dst streamRecorder
	err := s.pipeAnthropicSSE(context.Background(), eng, strings.NewReader(src), &dst)
	return dst.String(), err
}

// outputFrames parses what was written back into frames, using the same reader the pipe does.
func outputFrames(out string) []sseFrame {
	br := bufio.NewReader(strings.NewReader(out))
	var frames []sseFrame
	for {
		frame, err := readFrame(br)
		if err != nil {
			break
		}
		frames = append(frames, frame)
	}
	return frames
}

// collectedText is the concatenation of every text_delta the client received, which is what
// the client would render.
func collectedText(out string) string {
	var sb strings.Builder
	for _, frame := range outputFrames(out) {
		if frame.event != "content_block_delta" {
			continue
		}
		data, ok := parseFrameData(frame)
		if !ok {
			continue
		}
		delta, _ := data["delta"].(map[string]any)
		if typ, _ := delta["type"].(string); typ == "text_delta" {
			s, _ := delta["text"].(string)
			sb.WriteString(s)
		}
	}
	return sb.String()
}

// toolArguments returns the partial_json carried by an input_json_delta frame.
func toolArguments(t *testing.T, frame sseFrame) string {
	t.Helper()
	data, ok := parseFrameData(frame)
	if !ok {
		t.Fatalf("tool delta frame is not a json object: %s", frame.data)
	}
	delta, _ := data["delta"].(map[string]any)
	pj, _ := delta["partial_json"].(string)
	return pj
}

// findInputJSONDelta returns the single input_json_delta frame in the output.
func findInputJSONDelta(t *testing.T, out string) sseFrame {
	t.Helper()
	for _, frame := range outputFrames(out) {
		if frame.event != "content_block_delta" {
			continue
		}
		data, ok := parseFrameData(frame)
		if !ok {
			continue
		}
		delta, _ := data["delta"].(map[string]any)
		if typ, _ := delta["type"].(string); typ == "input_json_delta" {
			return frame
		}
	}
	t.Fatal("no input_json_delta frame in the output")
	return sseFrame{}
}

// gatedReader hands out one step at a time, waiting for the gate to open before every step
// after the first. A frame written before the gate opens was written before the next frame
// existed, so it proves the frame was not held.
type gatedReader struct {
	steps [][]byte
	gate  chan struct{}
	i     int
	buf   []byte
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if len(g.buf) == 0 {
		if g.i >= len(g.steps) {
			return 0, io.EOF
		}
		if g.i > 0 {
			<-g.gate
		}
		g.buf = g.steps[g.i]
		g.i++
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

// signallingDst signals on every flush, so a test can wait for a frame to reach the client.
type signallingDst struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	flushed chan struct{}
}

func (d *signallingDst) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.Write(p)
}

func (d *signallingDst) Flush() {
	select {
	case d.flushed <- struct{}{}:
	default:
	}
}

func (d *signallingDst) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// barrierReader serves chunks in order, pausing between the first and the second until every
// reader sharing wg has consumed the first. It fixes the interleaving of two streams, so a
// flusher shared between them is caught deterministically rather than by a lucky race.
type barrierReader struct {
	chunks [][]byte
	wg     *sync.WaitGroup
	once   sync.Once
	i      int
	buf    []byte
}

func (r *barrierReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.i == 1 {
			r.once.Do(func() {
				r.wg.Done()
				r.wg.Wait()
			})
		}
		if r.i >= len(r.chunks) {
			return 0, io.EOF
		}
		r.buf = r.chunks[r.i]
		r.i++
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// TestSSEForwardsPingImmediately pins the idle-watchdog rule: a ping reaches the client
// before the next frame exists. Remove the flush after a frame (or hold ping like text) and
// no signal arrives before the gate opens.
func TestSSEForwardsPingImmediately(t *testing.T) {
	ping := sseEvent("ping", `{"type":"ping"}`)
	stop := sseEvent("message_stop", `{"type":"message_stop"}`)
	src := &gatedReader{steps: [][]byte{[]byte(ping), []byte(stop)}, gate: make(chan struct{})}
	dst := &signallingDst{flushed: make(chan struct{}, 4)}
	s := walkerServer(t, nil)

	done := make(chan error, 1)
	go func() { done <- s.pipeAnthropicSSE(context.Background(), &answerEngine{}, src, dst) }()

	select {
	case <-dst.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("ping was not flushed before the next frame was released")
	}
	close(src.gate)
	if err := <-done; err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if got := dst.String(); got != ping+stop {
		t.Errorf("output = %q, want %q", got, ping+stop)
	}
}

// TestSSERestoresTextSplitAtEveryFrameBoundary pins the safe-boundary flush: a placeholder
// split between two text_delta frames at any position is restored whole. Restore per frame
// instead of through the flusher and the middle splits stay broken.
func TestSSERestoresTextSplitAtEveryFrameBoundary(t *testing.T) {
	const token = "<HOST_1>"
	const value = "db.prod.local"
	for split := 0; split <= len(token); split++ {
		t.Run(strconv.Itoa(split), func(t *testing.T) {
			src := textDeltaFrame(t, 0, "a"+token[:split]) +
				textDeltaFrame(t, 0, token[split:]+"b") +
				sseEvent("message_stop", `{"type":"message_stop"}`)
			s := walkerServer(t, nil)
			out, err := runPipe(t, s, &answerEngine{replace: [][2]string{{token, value}}}, src)
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			if got := collectedText(out); got != "a"+value+"b" {
				t.Errorf("restored text = %q, want %q", got, "a"+value+"b")
			}
			if strings.Contains(out, "<HOST_1") {
				t.Errorf("a raw placeholder fragment reached the client: %q", out)
			}
		})
	}
}

// TestSSERestoresTextSplitInsideOneFrame pins the network-chunk boundary: the whole stream
// arrives one byte at a time, so no read ever sees a whole frame, and a token split across
// two frames must still be restored. A parser that assumes one read is one frame fails here.
func TestSSERestoresTextSplitInsideOneFrame(t *testing.T) {
	const token = "<HOST_1>"
	const value = "db.prod.local"
	src := textDeltaFrame(t, 0, "a"+token[:3]) +
		textDeltaFrame(t, 0, token[3:]+"b") +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	s := walkerServer(t, nil)
	var dst streamRecorder
	err := s.pipeAnthropicSSE(context.Background(), &answerEngine{replace: [][2]string{{token, value}}},
		iotest.OneByteReader(strings.NewReader(src)), &dst)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if got := collectedText(dst.String()); got != "a"+value+"b" {
		t.Errorf("restored text = %q, want %q", got, "a"+value+"b")
	}
	if strings.Contains(dst.String(), "<HOST_1") {
		t.Errorf("a raw placeholder fragment reached the client: %q", dst.String())
	}
}

// TestSSEKeepsTheEventOrder pins that no frame is reordered or delayed: the events reach the
// client in the order they arrived. Hold message_delta back, or emit a delta before its
// content_block_start, and the order breaks.
func TestSSEKeepsTheEventOrder(t *testing.T) {
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	src := sseEvent("message_start", `{"type":"message_start"}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		textDeltaFrame(t, 0, "hello world") +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	s := walkerServer(t, nil)
	out, err := runPipe(t, s, &answerEngine{}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var got []string
	for _, frame := range outputFrames(out) {
		got = append(got, frame.event)
	}
	if !slices.Equal(got, want) {
		t.Errorf("event order = %v, want %v", got, want)
	}
}

// TestSSEBuffersToolArgumentsUntilTheBlockStops pins that input_json_delta frames are never
// forwarded as they arrive: the arguments come out as one frame, immediately before the
// content_block_stop, with the token already restored. Forward them directly and several
// input_json_delta frames appear before the stop.
func TestSSEBuffersToolArgumentsUntilTheBlockStops(t *testing.T) {
	const value = "db.prod.local"
	src := sseEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"Bash","input":{}}}`) +
		inputJSONDeltaFrame(t, 1, `{"cmd":"ssh `) +
		inputJSONDeltaFrame(t, 1, `<HOST_1>`) +
		inputJSONDeltaFrame(t, 1, `"}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	s := walkerServer(t, nil)
	out, err := runPipe(t, s, &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	jsonDelta, stop := -1, -1
	for i, frame := range outputFrames(out) {
		switch frame.event {
		case "content_block_stop":
			stop = i
		case "content_block_delta":
			if data, ok := parseFrameData(frame); ok {
				if delta, _ := data["delta"].(map[string]any); delta["type"] == "input_json_delta" {
					if jsonDelta != -1 {
						t.Errorf("more than one input_json_delta frame was emitted")
					}
					jsonDelta = i
				}
			}
		}
	}
	if jsonDelta == -1 {
		t.Fatal("no input_json_delta frame was emitted")
	}
	if stop == -1 {
		t.Fatal("no content_block_stop frame was emitted")
	}
	if jsonDelta != stop-1 {
		t.Errorf("input_json_delta at %d, content_block_stop at %d, want them adjacent", jsonDelta, stop)
	}
	args := decodeJSON(t, []byte(toolArguments(t, outputFrames(out)[jsonDelta])))
	if got := args["cmd"]; got != "ssh "+value {
		t.Errorf("tool command = %v, want %q", got, "ssh "+value)
	}
}

// TestSSERestoresAnEscapedPlaceholderInToolArguments pins Review Focus 3: the token reaches
// the pipe HTML-escaped inside partial_json (the encoder writes "<" as its \u escape). A text
// edit of the fragment would not see it; parsing the arguments as JSON does, so the value is
// put back and no raw token survives.
func TestSSERestoresAnEscapedPlaceholderInToolArguments(t *testing.T) {
	const value = "db.prod.local"
	// json.Marshal HTML-escapes "<", so the accumulated arguments carry the escape the
	// upstream sends, not a literal "<HOST_1>".
	quoted, err := json.Marshal("<HOST_1>")
	if err != nil {
		t.Fatal(err)
	}
	escaped := string(quoted[1 : len(quoted)-1])
	pieces := []string{`{"cmd":"ssh `, escaped, `"}`}
	src := sseEvent("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_2","name":"Bash","input":{}}}`)
	for _, piece := range pieces {
		src += inputJSONDeltaFrame(t, 2, piece)
	}
	src += sseEvent("content_block_stop", `{"type":"content_block_stop","index":2}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	s := walkerServer(t, nil)
	out, err := runPipe(t, s, &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	got := decodeJSON(t, []byte(toolArguments(t, findInputJSONDelta(t, out))))
	if got["cmd"] != "ssh "+value {
		t.Errorf("tool command = %v, want %q", got["cmd"], "ssh "+value)
	}
	if raw := toolArguments(t, findInputJSONDelta(t, out)); strings.Contains(raw, "<HOST_1") {
		t.Errorf("the raw token survived in the arguments: %q", raw)
	}
}

// TestSSEHoldsAnUnfinishedPlaceholderUntilMessageStop pins that an unclosed "<" never
// reaches the client. At message_stop the held tail is dropped — a raw token must never go
// out — and the loss is reported on diag rather than swallowed. Hand the tail to the client
// and the raw fragment appears.
func TestSSEHoldsAnUnfinishedPlaceholderUntilMessageStop(t *testing.T) {
	const value = "db.prod.local"
	var diag bytes.Buffer
	s := walkerServer(t, &diag)
	src := sseEvent("message_start", `{"type":"message_start"}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		textDeltaFrame(t, 0, "a <HOST_1") +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	out, err := runPipe(t, s, &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if strings.Contains(out, "<HOST_1") {
		t.Errorf("the raw placeholder fragment reached the client: %q", out)
	}
	if got := collectedText(out); got != "a " {
		t.Errorf("text = %q, want %q", got, "a ")
	}
	if line := diag.String(); !strings.Contains(line, "withheld") {
		t.Errorf("diag = %q, want the withheld tail reported", line)
	}
	if line := diag.String(); strings.Contains(line, value) {
		t.Errorf("diag must carry no value: %q", line)
	}
}

// TestSSEEarlyEOFFlushesAndReports pins Review Focus 5: a stream that stops without
// message_stop is not silent. The held tail is dropped, the loss is reported on diag, and an
// error is returned so the caller knows the answer was incomplete. Return nil at EOF and the
// error assertion fails.
func TestSSEEarlyEOFFlushesAndReports(t *testing.T) {
	var diag bytes.Buffer
	s := walkerServer(t, &diag)
	src := sseEvent("message_start", `{"type":"message_start"}`) +
		textDeltaFrame(t, 0, "a <HOST_1") // no message_stop
	out, err := runPipe(t, s, &answerEngine{}, src)
	if err == nil {
		t.Fatal("want an error when the stream ends without message_stop")
	}
	if line := diag.String(); !strings.Contains(line, "stream ended early") {
		t.Errorf("diag = %q, want the early end reported", line)
	}
	if strings.Contains(out, "<HOST_1") {
		t.Errorf("the raw placeholder fragment reached the client: %q", out)
	}
}

// TestSSEForwardsUnknownEventsAndThinkingVerbatim pins that only the text path is rewritten:
// thinking_delta, signature_delta and an unknown event go on byte-for-byte, because
// rewriting a thinking signature invalidates it and an unknown event is not this build's to
// touch. Route them through the engine and the frames change.
func TestSSEForwardsUnknownEventsAndThinkingVerbatim(t *testing.T) {
	const token = "<HOST_1>"
	thinking := sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"`+token+`"}}`)
	signature := sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc123"}}`)
	unknown := sseEvent("future_event", `{"type":"future_event","data":"`+token+`"}`)
	src := sseEvent("message_start", `{"type":"message_start"}`) + thinking + signature + unknown +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	s := walkerServer(t, nil)
	out, err := runPipe(t, s, &answerEngine{replace: [][2]string{{token, "db.prod.local"}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	for _, want := range []string{thinking, signature, unknown} {
		if !strings.Contains(out, want) {
			t.Errorf("frame was not forwarded verbatim:\nwant %q\nin %q", want, out)
		}
	}
	if !strings.Contains(out, token) {
		t.Errorf("the token inside the untouched frames must survive: %q", out)
	}
}

// TestSSETwoStreamsDoNotShareState pins that the flusher and the accumulators belong to one
// call, not to the Server. Two streams run on the same input, forced to interleave after
// their first frame; a flusher shared between them leaks one stream's held fragment into the
// other's output. Move the flusher onto Server and this fails deterministically.
func TestSSETwoStreamsDoNotShareState(t *testing.T) {
	f1 := textDeltaFrame(t, 0, "x<HOST_1")
	tail := textDeltaFrame(t, 0, ">") + sseEvent("message_stop", `{"type":"message_stop"}`)

	s := walkerServer(t, nil)
	var wg sync.WaitGroup
	wg.Add(2)

	type result struct {
		out string
		err error
	}
	run := func(value string) result {
		var dst streamRecorder
		src := &barrierReader{chunks: [][]byte{[]byte(f1), []byte(tail)}, wg: &wg}
		err := s.pipeAnthropicSSE(context.Background(), &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, src, &dst)
		return result{out: dst.String(), err: err}
	}

	results := make(chan result, 2)
	go func() { results <- run("host-A") }()
	go func() { results <- run("host-B") }()

	got := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("pipe: %v", r.err)
			}
			got[collectedText(r.out)] = true
		case <-time.After(5 * time.Second):
			t.Fatal("the two streams did not finish; the barrier deadlocked")
		}
	}
	for _, want := range []string{"xhost-A", "xhost-B"} {
		if !got[want] {
			t.Errorf("missing stream output %q; got %v", want, got)
		}
	}
}

// TestSSESanitizesTheErrorMessage pins the error event: the provider's message keeps its
// wording — a client retries on it — while a value inside becomes a token. Forward the event
// untouched and the value leaks.
func TestSSESanitizesTheErrorMessage(t *testing.T) {
	const value = "db.prod.local"
	src := sseEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"could not reach `+value+`"}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	s := walkerServer(t, nil)
	out, err := runPipe(t, s, &answerEngine{replace: [][2]string{{value, "<HOST_1>"}}}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if strings.Contains(out, value) {
		t.Errorf("the error event leaked the value: %q", out)
	}
	if !strings.Contains(out, "could not reach") || !strings.Contains(out, "<HOST_1>") {
		t.Errorf("the wording must survive with the value tokenized: %q", out)
	}
}

// TestSSEKeepsUnparseableToolArguments pins that a tool_use block whose arguments do not
// parse is not dropped: they go on as they arrived, and the failure is reported on diag
// without its text. Return the arguments empty on a parse failure and this fails.
func TestSSEKeepsUnparseableToolArguments(t *testing.T) {
	var diag bytes.Buffer
	s := walkerServer(t, &diag)
	src := sseEvent("content_block_start", `{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"tu_3","name":"Bash","input":{}}}`) +
		inputJSONDeltaFrame(t, 3, `not json at all`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":3}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	out, err := runPipe(t, s, &answerEngine{}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if got := toolArguments(t, findInputJSONDelta(t, out)); got != "not json at all" {
		t.Errorf("arguments = %q, want them passed on as they arrived", got)
	}
	if line := diag.String(); !strings.Contains(line, "tool arguments could not be restored") {
		t.Errorf("diag = %q, want the parse failure reported", line)
	}
}

// noFlushWriter is an http.ResponseWriter without Flush, to exercise the buffered fallback a
// wrapping middleware could present.
type noFlushWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *noFlushWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *noFlushWriter) WriteHeader(code int) { w.code = code }

func (w *noFlushWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

// TestStreamAnthropicBuffersWhenTheWriterCannotFlush pins the fallback: a ResponseWriter with
// no Flush still receives the whole stream rather than losing it. Return early on the failed
// assertion and the body is empty.
func TestStreamAnthropicBuffersWhenTheWriterCannotFlush(t *testing.T) {
	stream := textDeltaFrame(t, 0, "hello") + sseEvent("message_stop", `{"type":"message_stop"}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}
	w := &noFlushWriter{}
	s := walkerServer(t, nil)
	s.streamAnthropic(context.Background(), &answerEngine{}, resp, w)
	if got := w.body.String(); got != stream {
		t.Errorf("buffered stream = %q, want the whole stream %q", got, stream)
	}
}

// TestHandlerStreamsAnEventStream pins the handler's branch: a text/event-stream answer is
// piped frame by frame and its values restored, not handed on whole. Remove the Content-Type
// branch and the stream falls to the non-JSON arm, which forwards the raw token.
func TestHandlerStreamsAnEventStream(t *testing.T) {
	stream := textDeltaFrame(t, 0, "the host is <HOST_1>") + sseEvent("message_stop", `{"type":"message_stop"}`)
	up := &capturingUpstream{
		status: http.StatusOK,
		header: http.Header{"Content-Type": {"text/event-stream"}},
		body:   []byte(stream),
	}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	s := answerServer(t, ts.URL, &answerEngine{replace: [][2]string{{"<HOST_1>", "db.prod.local"}}}, nil, "")

	rec := postMessages(s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := collectedText(rec.Body.String()); got != "the host is db.prod.local" {
		t.Errorf("streamed text = %q, want the value put back", got)
	}
}

// TestSSESendsNoToolDeltaForAnEmptyInput pins that a tool_use block with no arguments adds
// nothing: its input is already the start frame's {}, so no empty input_json_delta is emitted
// and no spurious diagnostic is written. Drop the empty guard and a stray delta appears.
func TestSSESendsNoToolDeltaForAnEmptyInput(t *testing.T) {
	var diag bytes.Buffer
	s := walkerServer(t, &diag)
	src := sseEvent("content_block_start", `{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"tu_4","name":"NoArgs","input":{}}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":4}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)
	out, err := runPipe(t, s, &answerEngine{}, src)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	for _, frame := range outputFrames(out) {
		if frame.event == "content_block_delta" {
			t.Errorf("an empty tool block emitted a delta: %s", frame.data)
		}
	}
	if line := diag.String(); strings.Contains(line, "could not be restored") {
		t.Errorf("diag = %q, want no spurious restore failure", line)
	}
}
