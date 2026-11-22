package index

import (
	"encoding/json"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// Entry is what the index keeps about one page file.
type Entry struct {
	ID, Path, Folder string
	Title, Subtitle  string // "" when the page has none
	Source           string // where the text comes from, or ""
	Label            string
	Untitled         bool
	Type             string
	Tags             []string
	Created          string // as written in the header (YYYY-MM-DD), or ""
	CreatedKey       int64  // sort key: the created date, else the file's mtime
	MtimeNs          int64
	Size             int64
	Rev              string
	Excerpt          string
	// PropsJSON is the JSON object of the header's properties (see propsOf),
	// "" for none.
	PropsJSON string
	// PartialJSON is the JSON array of the keys whose copy in PropsJSON is not
	// the whole of the value (see propsOf), "" for none.
	PartialJSON string

	props []propRow

	// Text fed to the full-text index, already tokenised.
	ftsTitle, ftsSubtitle, ftsSource, ftsBody string
}

// BuildEntry parses a page file. When the file cannot be a page (no usable
// id), it returns the first problem instead and no entry.
func BuildEntry(rel string, data []byte, mtimeNs int64) (*Entry, *page.Problem) {
	doc := page.Parse(data)
	if ps := doc.Problems(); len(ps) > 0 {
		return nil, &ps[0]
	}
	m := doc.Meta()
	title := m.Title()
	label, untitled := page.Label(title, doc.Body)
	plain := page.PlainText(doc.Body)
	e := &Entry{
		ID: m.ID(), Path: rel, Folder: folderOf(rel),
		Title: title, Subtitle: m.Subtitle(), Source: m.Source(), Label: label, Untitled: untitled,
		Type: m.Type(), Tags: m.Tags(), Created: m.Created(),
		MtimeNs: mtimeNs, Size: int64(len(data)), Rev: page.Rev(data),
		Excerpt:  page.Excerpt(doc.Body),
		ftsTitle: Fold(title), ftsSubtitle: Fold(m.Subtitle()), ftsSource: Fold(m.Source()), ftsBody: Fold(plain),
	}
	if e.Tags == nil {
		e.Tags = []string{}
	}
	var partial []string
	e.props, e.PropsJSON, partial = propsOf(m, doc.InexactKeys())
	if len(partial) > 0 {
		b, _ := json.Marshal(partial)
		e.PartialJSON = string(b)
	}
	e.CreatedKey = mtimeNs
	if t, err := time.Parse("2006-01-02", e.Created); err == nil {
		e.CreatedKey = t.UnixNano()
	} else {
		e.Created = "" // not a date: do not show it as one
	}
	return e, nil
}

func folderOf(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

func tagsJSON(tags []string) string {
	b, _ := json.Marshal(tags)
	return string(b)
}

// sepToken stands for punctuation in the full-text index. It is a private-use
// character, which SQLite's tokenizer treats as part of a word, so it becomes
// a token of its own and a phrase query cannot match across it.
const sepToken = ""

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// Fold prepares text for the full-text index, and is also applied to search
// terms so both sides agree:
//
//   - every CJK character becomes a word of its own, so one- and two-character
//     Chinese words can be found with a phrase query;
//   - punctuation and symbols become sepToken (once per run), so "栀子花" does
//     not match "栀子，花开了";
//   - a blank line, or a line break after CJK text (a line of verse), counts as
//     punctuation too, while a line break inside Latin text does not.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 4 / 3)
	started, pending, prevCJK := false, false, false
	newlines := 0
	space := func() {
		if n := b.Len(); n > 0 && b.String()[n-1] != ' ' {
			b.WriteByte(' ')
		}
	}
	for _, r := range s {
		switch {
		case r == '\n':
			newlines++
			if newlines >= 2 || prevCJK {
				pending = true
			}
			space()
		case unicode.IsSpace(r):
			space()
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			newlines = 0
			pending = true
			space()
		default:
			newlines = 0
			if pending && started {
				b.WriteString(sepToken)
				b.WriteByte(' ')
			}
			pending = false
			started = true
			if isCJK(r) {
				space()
				b.WriteRune(r)
				b.WriteByte(' ')
				prevCJK = true
			} else {
				b.WriteRune(r)
				prevCJK = false
			}
		}
	}
	return b.String()
}

// MatchQuery turns what a person typed into an FTS5 expression: each
// whitespace-separated term becomes a phrase and all of them must match. ok is
// false when the text holds nothing searchable (empty, or only punctuation).
func MatchQuery(q string) (match string, ok bool) {
	var parts []string
	for _, term := range strings.Fields(q) {
		if f := strings.TrimSpace(Fold(term)); f != "" {
			parts = append(parts, `"`+f+`"`)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " AND "), true
}
