package proxy

import (
	"bytes"
	"testing"
)

// TestRestoreAnthropicLeavesThinkingImageAndUnknownBlocks pins the answer walker's field
// table on the way out: the same blocks the request walker skips are skipped here, and an
// answer with nothing to restore comes back byte-for-byte rather than re-marshalled.
func TestRestoreAnthropicLeavesThinkingImageAndUnknownBlocks(t *testing.T) {
	s := walkerServer(t, nil)
	const (
		thinkingText = "the host is <HOST_1>"
		signature    = "sig-abc-123"
		imageData    = "iVBORw0KGgoAAAANSUhEUg=="
		docData      = "JVBERi0xLjQK"
	)
	// A pair that would be applied if the walker entered any of these blocks.
	eng := &answerEngine{replace: [][2]string{{"<HOST_1>", "db.prod.local"}}}
	body := []byte(`{"content":[` +
		`{"type":"thinking","thinking":"the host is <HOST_1>","signature":"sig-abc-123"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAANSUhEUg=="}},` +
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}},` +
		`{"type":"mystery","data":"<HOST_1>"}]}`)

	out, unresolved, err := s.restoreAnthropic(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("restoreAnthropic: %v", err)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %+v, want none", unresolved)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("out =\n%s\nwant the input byte-for-byte", out)
	}
	doc := decodeJSON(t, out)
	blocks, ok := doc["content"].([]any)
	if !ok || len(blocks) != 4 {
		t.Fatalf("content = %#v, want four blocks", doc["content"])
	}
	thinking, _ := blocks[0].(map[string]any)
	if thinking["thinking"] != thinkingText || thinking["signature"] != signature {
		t.Errorf("thinking = %#v, want it byte-for-byte", thinking)
	}
	imageSrc, _ := blocks[1].(map[string]any)["source"].(map[string]any)
	if imageSrc["data"] != imageData {
		t.Errorf("image data = %v, want byte-for-byte %q", imageSrc["data"], imageData)
	}
	docSrc, _ := blocks[2].(map[string]any)["source"].(map[string]any)
	if docSrc["data"] != docData {
		t.Errorf("document data = %v, want byte-for-byte %q", docSrc["data"], docData)
	}
	if got := blocks[3].(map[string]any)["data"]; got != "<HOST_1>" {
		t.Errorf("unknown block data = %v, want it left whole", got)
	}
	if eng.saw("<HOST_1>") {
		t.Error("the walker routed a skipped block through the engine")
	}
}
