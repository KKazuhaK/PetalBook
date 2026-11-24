package api

import (
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// restoreUser restores a person's item (see "Restoring and purging a user").
func (s *server) restoreUser(c *call, it *store.RecycleItem) {
	writeError(c.w, http.StatusNotFound, "not_found", "no such item in the recycle bin")
}
