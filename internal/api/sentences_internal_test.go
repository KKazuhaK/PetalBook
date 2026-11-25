package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Whatever a message was made from, it leaves as a sentence.
func TestErrorsLeaveAsSentences(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"No such page.":            "No such page.",
		"bad input":                "bad input.",
		"made from an inner one..": "made from an inner one.",
	} {
		w := httptest.NewRecorder()
		writeError(w, 400, "invalid", in)
		var b errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || b.Message != want {
			t.Errorf("%q left as %q (%v), want %q", in, b.Message, err, want)
		}
		if got := mcpErr("invalid", in).Message; got != want {
			t.Errorf("mcp: %q left as %q, want %q", in, got, want)
		}
	}
}
