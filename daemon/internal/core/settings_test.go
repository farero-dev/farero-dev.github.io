package core

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/store"
)

func TestSettingsSet(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	n := 0
	send := func(typ, raw string) ipc.Message {
		t.Helper()
		n++
		id := "s" + strconv.Itoa(n)
		m := ipc.Message{Type: typ, ID: id}
		if raw != "" {
			m.Data = json.RawMessage(raw)
		}
		if err := u.conn.Write(m); err != nil {
			t.Fatal(err)
		}
		return u.reply(id)
	}
	expect := func(m ipc.Message, limit int, update bool) {
		t.Helper()
		got, err := ipc.Decode[ipc.Settings](m)
		if m.Type != ipc.TypeSettingsGet || err != nil || got.ResultLimitBytes != limit || got.UpdateCheck != update {
			t.Fatalf("got %s %s, want {%d %v}", m.Type, m.Data, limit, update)
		}
	}
	expect(send(ipc.TypeSettingsGet, ""), store.DefaultResultLimit, true)

	// Partial updates keep the other field.
	expect(send(ipc.TypeSettingsSet, `{"result_limit_bytes":4096}`), 4096, true)
	expect(send(ipc.TypeSettingsSet, `{"update_check":false}`), 4096, false)
	expect(send(ipc.TypeSettingsSet, `{"update_check":null}`), 4096, false)

	// Out of range (1 KB to 1 MB) or malformed values are refused.
	for _, raw := range []string{
		`{"result_limit_bytes":1023}`,
		`{"result_limit_bytes":1048577}`,
		`{"result_limit_bytes":0}`,
		`{"result_limit_bytes":-1}`,
		`{"result_limit_bytes":"big"}`,
		`{"result_limit_bytes":0,"update_check":true}`, // nothing is applied
	} {
		if m := send(ipc.TypeSettingsSet, raw); m.Type != ipc.TypeError {
			t.Errorf("%s accepted: %s %s", raw, m.Type, m.Data)
		}
	}
	expect(send(ipc.TypeSettingsGet, ""), 4096, false)

	// The log keeps the first 4096 bytes and the original size; the agent
	// still gets the whole result.
	a := h.agent("s1", 100)
	big := strings.Repeat("가나", 2000) // 12000 bytes
	if res := a.call("dev_echo", map[string]any{"text": big}); text(res) != big {
		t.Fatalf("agent result cut: %d bytes", len(text(res)))
	}
	c := h.lastCall()
	if len(c.ResultText) > 4096 || len(c.ResultText) < 4090 || !utf8.ValidString(c.ResultText) || c.ResultBytes != len(big) {
		t.Fatalf("stored %d bytes (valid %v), result_bytes %d", len(c.ResultText), utf8.ValidString(c.ResultText), c.ResultBytes)
	}

	// The bounds themselves are allowed; back to the default stores it whole.
	expect(send(ipc.TypeSettingsSet, `{"result_limit_bytes":1048576}`), 1048576, false)
	expect(send(ipc.TypeSettingsSet, `{"result_limit_bytes":1024}`), 1024, false)
	expect(send(ipc.TypeSettingsSet, `{"result_limit_bytes":16384,"update_check":true}`), store.DefaultResultLimit, true)
	a.call("dev_echo", map[string]any{"text": big + "!"})
	if c := h.lastCall(); c.ResultText != big+"!" || c.ResultBytes != len(big)+1 {
		t.Fatalf("default limit: stored %d bytes, result_bytes %d", len(c.ResultText), c.ResultBytes)
	}
}
