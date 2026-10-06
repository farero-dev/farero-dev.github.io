package session

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// interruptMarker starts the text Claude Code appends to the transcript as a
// user message when the user stops a turn: "[Request interrupted by user]"
// or "[Request interrupted by user for tool use]".
const interruptMarker = "[Request interrupted by user"

// transcriptInterrupted reports whether the last message (user or assistant
// row) written at or after offset from in a Claude Code transcript is the
// interrupt marker. from is the file's size at the session's last event, so
// a marker of an earlier turn does not count. Bookkeeping rows after it
// (cost-state, file-history-snapshot, system) are skipped; they can be
// large, so the tail is read in growing chunks. A row still being written
// (no newline yet) means no answer yet.
func transcriptInterrupted(path string, from int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	size := fi.Size()
	if size <= from {
		return false
	}
	for _, chunk := range []int64{64 << 10, 1 << 20, 8 << 20} {
		off := max(size-chunk, from, 0)
		// Read one byte more to tell whether off starts a line.
		start := max(off-1, 0)
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return false
		}
		if buf[len(buf)-1] != '\n' {
			return false
		}
		if off > 0 {
			i := bytes.IndexByte(buf, '\n') // ends the line before off
			buf = buf[i+1:]
		}
		lines := bytes.Split(bytes.TrimSuffix(buf, []byte("\n")), []byte("\n"))
		for i := len(lines) - 1; i >= 0; i-- {
			if interrupted, ok := messageRow(lines[i]); ok {
				return interrupted
			}
		}
		if off == from || off == 0 {
			return false
		}
	}
	return false
}

// messageRow reports whether line is a user or assistant row (ok) and
// whether it is the interrupt marker.
func messageRow(line []byte) (interrupted, ok bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return false, false
	}
	var row struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &row) != nil {
		return false, false
	}
	switch row.Type {
	case "assistant":
		return false, true
	case "user":
	default:
		return false, false
	}
	var text string
	if json.Unmarshal(row.Message.Content, &text) == nil {
		return strings.HasPrefix(text, interruptMarker), true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(row.Message.Content, &blocks)
	for _, b := range blocks {
		if b.Type == "text" && strings.HasPrefix(b.Text, interruptMarker) {
			return true, true
		}
	}
	return false, true
}
