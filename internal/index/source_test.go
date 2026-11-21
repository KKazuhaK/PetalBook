package index

import "testing"

func TestSourceIsIndexedAndSearchable(t *testing.T) {
	data := []byte("---\nid: 01JD3K8Q7Z\ntitle: 暮雪\nsource: 《雪国》第三章\n---\n正文。\n")
	e, p := BuildEntry("暮雪.md", data, 1)
	if p != nil {
		t.Fatalf("problem: %+v", p)
	}
	if e.Source != "《雪国》第三章" {
		t.Fatalf("source = %q", e.Source)
	}
	if e.ftsSource != Fold("《雪国》第三章") {
		t.Fatalf("fts source = %q", e.ftsSource)
	}
}
