package api

import (
	"fmt"
	"io/fs"
	"net/http/httptest"
	"testing"

	"petalbook.app/server/internal/space"
)

func TestExistingNameErrorsAre409NotFiveHundred(t *testing.T) {
	t.Parallel()
	s := &server{}
	for _, err := range []error{
		fs.ErrExist,
		fmt.Errorf("rename: %w", fs.ErrExist),
		space.ErrExists,
		fmt.Errorf("%w: no free file name", space.ErrExists),
	} {
		rec := httptest.NewRecorder()
		s.fail(rec, err)
		if rec.Code != 409 {
			t.Errorf("%v: %d %s", err, rec.Code, rec.Body)
		}
	}
}
