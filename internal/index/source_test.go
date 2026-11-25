package index

import (
	"reflect"
	"testing"
)

func TestSourceIsIndexedAndSearchable(t *testing.T) {
	t.Parallel()
	data := []byte("---\nid: 01JD3K8Q7Z\ntitle: 暮雪\nsource: 《雪国》第三章\n---\n正文。\n")
	e, p := BuildEntry("暮雪.md", data, 1)
	if p != nil {
		t.Fatalf("problem: %+v", p)
	}
	// A single text written by hand is one source.
	if !reflect.DeepEqual(e.Sources, []string{"《雪国》第三章"}) {
		t.Fatalf("sources = %q", e.Sources)
	}
	if e.ftsSource != Fold("《雪国》第三章") {
		t.Fatalf("fts source = %q", e.ftsSource)
	}
}

func TestSourcesAreAListLikeTags(t *testing.T) {
	t.Parallel()
	data := []byte("---\nid: 01JD3K8Q7Z\ntitle: x\nsource: [书, 电影, 书]\n---\nb\n")
	e, p := BuildEntry("x.md", data, 1)
	if p != nil {
		t.Fatalf("problem: %+v", p)
	}
	if !reflect.DeepEqual(e.Sources, []string{"书", "电影"}) {
		t.Fatalf("sources = %q", e.Sources)
	}
	// They are rows of the props table (filter, sort, board columns), and
	// not among the properties of the page.
	var rows []string
	for _, r := range e.props {
		if r.key == "source" {
			rows = append(rows, r.val)
		}
	}
	if !reflect.DeepEqual(rows, []string{"书", "电影"}) {
		t.Fatalf("rows = %q", rows)
	}
	if e.PropsJSON != "" {
		t.Fatalf("props = %s", e.PropsJSON)
	}
}
