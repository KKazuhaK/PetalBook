package api

import (
	"testing"

	"petalbook.app/server/internal/store"
)

// A page of tenants is answered from one plan index, not a plan query a row.
func TestPlanIndexAnswersFromTheMap(t *testing.T) {
	t.Parallel()
	idx := indexPlans([]store.Plan{{ID: "p1", Slug: "pro", Name: "Pro"}, {ID: "p2", Slug: "free", Name: "Free"}})
	if r := idx.refOf("p2"); r == nil || r.Slug != "free" || r.Name != "Free" {
		t.Fatalf("p2: %+v", r)
	}
	if idx.refOf("gone") != nil || idx.refOf("") != nil {
		t.Fatal("an unknown plan is none")
	}
}
