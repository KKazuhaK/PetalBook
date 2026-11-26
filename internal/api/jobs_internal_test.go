package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// A job refused because an active job holds its lock key (two moves of one
// space, say) answers 409 busy, not 500.
func TestAHeldJobLockAnswersBusy(t *testing.T) {
	t.Parallel()
	s := &server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := httptest.NewRecorder()
	s.fail(w, fmt.Errorf("enqueue: %w", store.ErrJobLocked))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error":"busy"`) {
		t.Fatalf("%d %s; want 409 busy", w.Code, w.Body)
	}
}
