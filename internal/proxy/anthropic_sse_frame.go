package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// streamDst is the client side of an event stream: anything that writes and flushes. Both a
// real http.ResponseWriter and httptest.ResponseRecorder satisfy it.
type streamDst interface {
	io.Writer
	Flush()
}

// sseFrame is one fully assembled server-sent event: the bytes exactly as they arrived,
// plus the two fields the pipe reads. Keeping raw lets an event the pipe does not touch go
// on byte-for-byte.
type sseFrame struct {
	raw   []byte
	event string
	data  []byte
}

// readFrame reads one event up to and including its terminating blank line, tolerating both
// \n and \r\n. io.EOF comes back only when the stream ends cleanly at a frame boundary; a
// stream that stops inside a frame is io.ErrUnexpectedEOF.
func readFrame(br *bufio.Reader) (sseFrame, error) {
	var (
		raw   []byte
		event string
		data  []byte
	)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 {
			if len(raw) == 0 {
				if err == nil {
					return sseFrame{}, io.ErrUnexpectedEOF
				}
				return sseFrame{}, err // io.EOF at a boundary, or a read error
			}
			return sseFrame{}, io.ErrUnexpectedEOF
		}
		raw = append(raw, line...)
		text := strings.TrimRight(string(line), "\r\n")
		if text == "" {
			return sseFrame{raw: raw, event: event, data: data}, nil
		}
		if err != nil {
			return sseFrame{}, io.ErrUnexpectedEOF // EOF in the middle of a frame
		}
		field, value, _ := strings.Cut(text, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, value...)
		}
	}
}

// writeRaw sends a frame as it arrived and flushes. A frame is always written whole, then
// flushed — never half a frame.
func writeRaw(dst streamDst, raw []byte) error {
	if _, err := dst.Write(raw); err != nil {
		return err
	}
	dst.Flush()
	return nil
}

// writeEvent rebuilds one frame from its event and data and sends it whole.
func writeEvent(dst streamDst, event string, data []byte) error {
	frame := make([]byte, 0, len(event)+len(data)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, event...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, data...)
	frame = append(frame, "\n\n"...)
	if _, err := dst.Write(frame); err != nil {
		return err
	}
	dst.Flush()
	return nil
}

// parseFrameData decodes a frame's data into a JSON object with UseNumber, so a large id is
// not turned into exponential form. It reports false for anything that is not exactly one
// object, and the caller then forwards the frame as it arrived rather than guessing.
func parseFrameData(frame sseFrame) (map[string]any, bool) {
	if len(frame.data) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(frame.data))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil || v == nil {
		return nil, false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, false
	}
	return v, true
}
