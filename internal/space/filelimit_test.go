package space

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// Templates and types are files a write creates or replaces like any other:
// files.maxBytes (the author's MaxFile) refuses one over it, and nothing is
// written. One that is over a lowered limit already may be written again at its
// size or smaller.
func TestTemplatesAndTypesAreJudgedByTheFileLimit(t *testing.T) {
	t.Parallel()
	s := open(t)
	small := Author{MaxFile: 300}
	var ftl *FileTooLargeError

	_, err := s.PutTemplate(small, "big", "", page.Meta{{Key: "title", Value: "Big"}}, strings.Repeat("words words words\n", 40))
	if !errors.As(err, &ftl) || ftl.Limit != 300 || ftl.Name != "big.md" {
		t.Fatalf("a template over the limit: %v", err)
	}
	if _, err := s.Template("big"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the template was written: %v", err)
	}
	tpl, err := s.PutTemplate(Author{}, "grown", "", page.Meta{{Key: "title", Value: "Grown"}}, strings.Repeat("words words words\n", 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutTemplate(small, "grown", tpl.Rev, page.Meta{{Key: "title", Value: "Grown"}}, strings.Repeat("words words words\n", 30)); err != nil {
		t.Fatalf("a template over a lowered limit could not be made smaller: %v", err)
	}

	var fields []FieldInput
	for i := 0; i < 20; i++ {
		fields = append(fields, FieldInput{Key: fmt.Sprintf("field%02d", i), Title: "A field with a title", Kind: KindText})
	}
	_, err = s.PutType(small, "wide", "", TypeInput{Title: "Wide", Fields: fields})
	if !errors.As(err, &ftl) || ftl.Limit != 300 || ftl.Name != "wide.json" {
		t.Fatalf("a type over the limit: %v", err)
	}
	if l, _, err := s.Types(); err != nil || len(l) != 0 {
		t.Fatalf("the type was written: %v %v", l, err)
	}
}
