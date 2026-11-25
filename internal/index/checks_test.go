package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"petalbook.app/server/internal/page"
)

// poemRules is one type, "poem", bound nowhere: a page has it when its header
// says so.
func poemRules(fp string) *TypeRules {
	poem := &TypeRule{Fields: map[string]FieldRule{
		"status": {Kind: FieldSelect, Options: []string{"draft", "done"}},
		"rating": {Kind: FieldNumber},
		"done":   {Kind: FieldCheckbox},
		"due":    {Kind: FieldDate},
		"site":   {Kind: FieldURL},
		"moods":  {Kind: FieldMultiSelect, Options: []string{"calm", "wild"}},
		"note":   {Kind: FieldText},
	}}
	return &TypeRules{Fingerprint: fp, For: func(folder, typ string) *TypeRule {
		if typ == "poem" {
			return poem
		}
		return nil
	}}
}

func page8(id, title, extra, body string) string {
	h := "---\nid: " + id + "\n"
	if title != "" {
		h += "title: " + title + "\n"
	}
	return h + extra + "---\n" + body
}

type checkView struct {
	counts map[string]int
	items  []Check
	next   string
}

func listChecks(t *testing.T, x *Index, kind, cursor string) checkView {
	t.Helper()
	c, items, next, err := x.Checks(kind, cursor)
	if err != nil {
		t.Fatal(err)
	}
	return checkView{c, items, next}
}

func summary(items []Check) []string {
	var out []string
	for _, c := range items {
		out = append(out, c.Kind+" "+c.Path)
	}
	return out
}

func detailOf(t *testing.T, c Check) map[string]any {
	t.Helper()
	var m map[string]any
	if c.Detail == nil {
		return nil
	}
	if err := json.Unmarshal(c.Detail, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCheckKindsOfFilesThatAreNotPages(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	m := newMem()
	m.put("noid.md", "---\ntitle: x\n---\nbody\n")
	m.put("badid.md", "---\nid: nope\n---\nbody\n")
	m.put("nohead.md", "just text\n")
	m.put("yaml.md", "---\ntitle: [unclosed\nother: 1\n---\nbody\n")
	m.put("open.md", "---\ntitle: x\nno end\n")
	m.put("a.md", page8("0000000001", "A", "", "text"))
	m.put("z.md", page8("0000000001", "B", "", "text"))
	syncAll(t, x, m, false)

	v := listChecks(t, x, "", "")
	if got := strings.Join(summary(v.items), ","); got != "no_id badid.md,no_id noid.md,duplicate_id z.md,bad_header nohead.md,bad_header open.md,bad_header yaml.md" {
		t.Fatalf("items: %s", got)
	}
	want := map[string]int{"no_id": 2, "duplicate_id": 1, "bad_header": 3, "type_mismatch": 0, "not_in_type": 0, "broken_link": 0, "missing_asset": 0, "duplicate_title": 0}
	for k, n := range want {
		if v.counts[k] != n {
			t.Errorf("count %s = %d, want %d", k, v.counts[k], n)
		}
	}
	for _, c := range v.items {
		switch c.Path {
		case "noid.md", "badid.md":
			if c.Fix != "add_id" {
				t.Errorf("%s fix %q", c.Path, c.Fix)
			}
		default:
			if c.Fix != "" {
				t.Errorf("%s fix %q", c.Path, c.Fix)
			}
		}
		if c.Message == "" {
			t.Errorf("%s has no message", c.Path)
		}
	}
	for _, c := range v.items {
		switch c.Path {
		case "z.md":
			d := detailOf(t, c)
			if c.ID != "0000000001" || fmt.Sprint(d["paths"]) != "[a.md z.md]" || c.Rev != page.Rev(m.files["z.md"].data) {
				t.Errorf("duplicate: id %q detail %v", c.ID, d)
			}
		case "noid.md":
			if c.Rev != page.Rev([]byte("---\ntitle: x\n---\nbody\n")) || c.Detail != nil {
				t.Errorf("no_id: rev %q detail %s", c.Rev, c.Detail)
			}
		case "yaml.md":
			if d := detailOf(t, c); d["line"] != float64(2) {
				t.Errorf("yaml line: %v", d)
			}
		case "open.md":
			if d := detailOf(t, c); d["line"] != float64(1) {
				t.Errorf("open line: %v", d)
			}
		case "nohead.md":
			if c.Detail != nil {
				t.Errorf("no line is known: %s", c.Detail)
			}
		}
	}
	// A kind filter.
	if v := listChecks(t, x, "bad_header", ""); len(v.items) != 3 || v.counts["no_id"] != 2 {
		t.Fatalf("filtered: %v counts %v", summary(v.items), v.counts)
	}
	// Fixing the file moves it out of the list.
	m.put("noid.md", page8("0000000002", "x", "", "body\n"))
	if _, err := x.Refresh(m, []string{"noid.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if v := listChecks(t, x, "no_id", ""); len(v.items) != 1 || v.counts["no_id"] != 1 {
		t.Fatalf("after the fix: %v", summary(v.items))
	}
	// A twin that is deleted frees the duplicate.
	delete(m.files, "a.md")
	if err := x.Remove("0000000001"); err != nil {
		t.Fatal(err)
	}
	if err := x.RetryDuplicates(m); err != nil {
		t.Fatal(err)
	}
	if v := listChecks(t, x, "duplicate_id", ""); len(v.items) != 0 || v.counts["duplicate_id"] != 0 {
		t.Fatalf("duplicate remains: %v", summary(v.items))
	}
}

func TestCheckTypeMismatchAndNotInType(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	x.UseTypes(poemRules("a"))
	m := newMem()
	m.put("good.md", page8("0000000001", "Good", "type: poem\nstatus: done\nrating: 4\ndone: true\ndue: 2026-01-02\nsite: https://x.test/a\nmoods: [calm]\nnote: hi\nslug: s\n", "x"))
	m.put("bad.md", page8("0000000002", "Bad", "type: poem\nstatus: maybe\nrating: high\ndone: yes please\ndue: tomorrow\nsite: nowhere\nmoods: calm\nnote: [a, b]\n", "x"))
	m.put("stray.md", page8("0000000003", "Stray", "type: poem\nstatus: draft\ncolour: red\nsize: 3\nempty:\n", "x"))
	m.put("both.md", page8("0000000004", "Both", "type: poem\nrating: no\nextra: 1\n", "x"))
	m.put("untyped.md", page8("0000000005", "Untyped", "colour: red\nrating: no\n", "x"))
	m.put("unknown.md", page8("0000000006", "Unknown", "type: nothing\ncolour: red\n", "x"))
	syncAll(t, x, m, false)

	mm := listChecks(t, x, "type_mismatch", "")
	if got := strings.Join(summary(mm.items), ","); got != "type_mismatch bad.md,type_mismatch both.md" {
		t.Fatalf("mismatch: %s", got)
	}
	if f := fmt.Sprint(detailOf(t, mm.items[0])["fields"]); f != "[status rating done due site moods note]" {
		t.Errorf("bad.md fields: %s", f)
	}
	if f := fmt.Sprint(detailOf(t, mm.items[1])["fields"]); f != "[rating]" {
		t.Errorf("both.md fields: %s", f)
	}
	nt := listChecks(t, x, "not_in_type", "")
	if got := strings.Join(summary(nt.items), ","); got != "not_in_type both.md,not_in_type stray.md" {
		t.Fatalf("not in type: %s", got)
	}
	if f := fmt.Sprint(detailOf(t, nt.items[1])["fields"]); f != "[colour size]" {
		t.Errorf("stray.md fields: %s", f)
	}
	if nt.items[0].Fix != "remove_fields" || mm.items[0].Fix != "" {
		t.Errorf("fixes: %q %q", nt.items[0].Fix, mm.items[0].Fix)
	}
	if mm.counts["type_mismatch"] != 2 || mm.counts["not_in_type"] != 2 {
		t.Errorf("counts %v", mm.counts)
	}

	// A save that repairs one page and breaks another.
	if _, err := x.Put("bad.md", []byte(page8("0000000002", "Bad", "type: poem\nstatus: done\n", "x")), clock()+1); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Put("good.md", []byte(page8("0000000001", "Good", "type: poem\nstatus: sad\nodd: 1\n", "x")), clock()+2); err != nil {
		t.Fatal(err)
	}
	v := listChecks(t, x, "", "")
	if v.counts["type_mismatch"] != 2 || v.counts["not_in_type"] != 3 {
		t.Fatalf("counts after saves: %v", v.counts)
	}
	for _, c := range v.items {
		if c.Path == "bad.md" {
			t.Errorf("bad.md should be fine now: %s", c.Kind)
		}
	}

	// A page whose type loses a field: ApplyTypes checks every page again.
	rules := poemRules("b")
	rules.For = func(folder, typ string) *TypeRule {
		if typ == "poem" {
			return &TypeRule{Fields: map[string]FieldRule{"status": {Kind: FieldSelect, Options: []string{"draft", "done", "sad"}}, "colour": {Kind: FieldText}}}
		}
		return nil
	}
	if err := x.ApplyTypes(rules); err != nil {
		t.Fatal(err)
	}
	v = listChecks(t, x, "", "")
	if got := strings.Join(summary(v.items), ","); got != "not_in_type both.md,not_in_type good.md,not_in_type stray.md" {
		t.Fatalf("after the types changed: %s", got)
	}
	// The same rules again change nothing and cost no recheck (the fingerprint).
	before := v.counts
	if err := x.ApplyTypes(rules); err != nil {
		t.Fatal(err)
	}
	if v2 := listChecks(t, x, "", ""); fmt.Sprint(v2.counts) != fmt.Sprint(before) {
		t.Errorf("counts moved: %v -> %v", before, v2.counts)
	}
}

func TestCheckStrayFieldsOfADoc(t *testing.T) {
	t.Parallel()
	rule := &TypeRule{Fields: map[string]FieldRule{"status": {Kind: FieldText}}}
	doc := page.Parse([]byte(page8("0000000001", "T", "status: a\ncolour: red\nslug: s\nobj:\n  a: 1\nnone:\n", "x")))
	if got := fmt.Sprint(StrayFields(doc, rule)); got != "[colour obj]" {
		t.Fatalf("stray: %s", got)
	}
	if StrayFields(doc, nil) != nil {
		t.Fatal("no rule, no strays")
	}
}

func TestCheckMissingAssets(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	have := map[string]bool{"assets/0000000001/a.png": true}
	x.SetAssetExists(func(rel string) bool { return have[rel] })
	m := newMem()
	body := "![x](/assets/0000000001/a.png) and [file](/assets/0000000001/gone.pdf) and ![y](<../assets/0000000001/b.png>)\n" +
		"[web](https://x.test/assets/a/b.png) [rel](assets/0000000001/c.png)\n" +
		"```\n![code](/assets/0000000001/in-code.png)\n```\n"
	m.put("n/p.md", page8("0000000001", "P", "", body))
	m.put("q.md", page8("0000000002", "Q", "", "![](assets/0000000001/gone%20too.png \"title\") [two](/assets/0000000001/a.png)\n"))
	syncAll(t, x, m, false)

	v := listChecks(t, x, "missing_asset", "")
	var targets []string
	for _, c := range v.items {
		targets = append(targets, c.Path+" "+detailOf(t, c)["target"].(string))
	}
	if got := strings.Join(targets, ","); got != "n/p.md /assets/0000000001/gone.pdf,n/p.md ../assets/0000000001/b.png,q.md assets/0000000001/gone%20too.png" {
		t.Fatalf("missing: %s", got)
	}
	if v.counts["missing_asset"] != 3 {
		t.Fatalf("count %v", v.counts)
	}
	// The file arrives: its problems go without anyone saving the page.
	have["assets/0000000001/gone.pdf"] = true
	if err := x.AssetAppeared("assets/0000000001/gone.pdf"); err != nil {
		t.Fatal(err)
	}
	if v := listChecks(t, x, "missing_asset", ""); len(v.items) != 2 {
		t.Fatalf("after the upload: %d", len(v.items))
	}
	// Saving the page again checks again.
	m.put("n/p.md", page8("0000000001", "P", "", "![x](/assets/0000000001/a.png)\n"))
	if _, err := x.Put("n/p.md", m.files["n/p.md"].data, clock()); err != nil {
		t.Fatal(err)
	}
	if v := listChecks(t, x, "missing_asset", ""); len(v.items) != 1 || v.items[0].Path != "q.md" {
		t.Fatalf("after the save: %v", summary(v.items))
	}
}

func TestCheckDuplicateTitles(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	m := newMem()
	m.put("a/one.md", page8("0000000001", "Same", "", "x"))
	m.put("a/two.md", page8("0000000002", "same", "", "x"))
	m.put("b/one.md", page8("0000000003", "Same", "", "x"))
	m.put("a/u1.md", page8("0000000004", "", "", "untitled one"))
	m.put("a/u2.md", page8("0000000005", "", "", "untitled two"))
	syncAll(t, x, m, false)

	paths := func() string { return strings.Join(summary(listChecks(t, x, "duplicate_title", "").items), ",") }
	if got := paths(); got != "duplicate_title a/one.md,duplicate_title a/two.md" {
		t.Fatalf("after the sync: %s", got)
	}
	v := listChecks(t, x, "duplicate_title", "")
	if p := fmt.Sprint(detailOf(t, v.items[0])["paths"]); p != "[a/one.md a/two.md]" || v.items[0].ID != "0000000001" {
		t.Fatalf("detail %s id %s", p, v.items[0].ID)
	}
	// A page of another folder joins in by moving.
	m.put("a/three.md", page8("0000000003", "Same", "", "x"))
	delete(m.files, "b/one.md")
	if _, err := x.Put("a/three.md", m.files["a/three.md"].data, clock()); err != nil {
		t.Fatal(err)
	}
	if got := paths(); got != "duplicate_title a/one.md,duplicate_title a/three.md,duplicate_title a/two.md" {
		t.Fatalf("after the move: %s", got)
	}
	// A retitle leaves the group, a delete shrinks it to one.
	m.put("a/two.md", page8("0000000002", "Other", "", "x"))
	if _, err := x.Put("a/two.md", m.files["a/two.md"].data, clock()); err != nil {
		t.Fatal(err)
	}
	if got := paths(); got != "duplicate_title a/one.md,duplicate_title a/three.md" {
		t.Fatalf("after the retitle: %s", got)
	}
	if err := x.Remove("0000000003"); err != nil {
		t.Fatal(err)
	}
	if got := paths(); got != "" {
		t.Fatalf("after the delete: %s", got)
	}
	if c := listChecks(t, x, "", "").counts["duplicate_title"]; c != 0 {
		t.Fatalf("count %d", c)
	}
	// A full rebuild finds them all again with one query.
	m.put("a/three.md", page8("0000000003", "Same", "", "x"))
	syncAll(t, x, m, true)
	if got := paths(); got != "duplicate_title a/one.md,duplicate_title a/three.md" {
		t.Fatalf("after the rebuild: %s", got)
	}
}

func TestCheckListingPagesAndOrder(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	x.UseTypes(poemRules("a"))
	m := newMem()
	const n = 120
	for i := 0; i < n; i++ {
		m.put(fmt.Sprintf("p/no%03d.md", i), "---\ntitle: t\n---\nx\n") // no_id
	}
	for i := 0; i < 60; i++ {
		m.put(fmt.Sprintf("p/ty%03d.md", i), page8(fmt.Sprintf("%010d", i+1), fmt.Sprintf("T%d", i), "type: poem\nrating: bad\n", "x"))
	}
	for i := 0; i < 3; i++ {
		m.put(fmt.Sprintf("p/hd%d.md", i), "no header\n")
	}
	syncAll(t, x, m, false)

	var all []string
	cursor := ""
	pages := 0
	for {
		v := listChecks(t, x, "", cursor)
		pages++
		if len(v.items) > CheckPage {
			t.Fatalf("page of %d", len(v.items))
		}
		all = append(all, summary(v.items)...)
		if v.next == "" {
			break
		}
		if len(v.items) != CheckPage {
			t.Fatalf("a page of %d with more to come", len(v.items))
		}
		cursor = v.next
	}
	if len(all) != n+60+3 || pages != 4 {
		t.Fatalf("%d items in %d pages", len(all), pages)
	}
	for i := 1; i < len(all); i++ {
		a, b := strings.SplitN(all[i-1], " ", 2), strings.SplitN(all[i], " ", 2)
		ra, rb := kindRank(a[0]), kindRank(b[0])
		if ra > rb || (ra == rb && a[1] >= b[1]) {
			t.Fatalf("order: %q then %q", all[i-1], all[i])
		}
	}
	// Exactly one page: no cursor.
	if v := listChecks(t, x, "bad_header", ""); len(v.items) != 3 || v.next != "" {
		t.Fatalf("bad_header: %d %q", len(v.items), v.next)
	}
	// A kind filter pages through that kind only, and a cursor of another kind is no harm.
	v := listChecks(t, x, "type_mismatch", "")
	if len(v.items) != 50 || v.next == "" {
		t.Fatalf("filtered page: %d %q", len(v.items), v.next)
	}
	v = listChecks(t, x, "type_mismatch", v.next)
	if len(v.items) != 10 || v.next != "" || v.items[0].Path != "p/ty050.md" {
		t.Fatalf("filtered second page: %d %q %s", len(v.items), v.next, v.items[0].Path)
	}
	// Bad input.
	for _, tc := range [][2]string{{"nope", ""}, {"", "!!"}, {"", "bm9wZQ"}} {
		if _, _, _, err := x.Checks(tc[0], tc[1]); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("Checks(%q, %q): %v", tc[0], tc[1], err)
		}
	}
	// Every kind is counted, listed or not.
	if c := listChecks(t, x, "", "").counts; len(c) != len(CheckKinds) || c["broken_link"] != 0 {
		t.Errorf("counts %v", c)
	}
}

func kindRank(k string) int {
	for i, c := range CheckKinds {
		if c == k {
			return i
		}
	}
	return -1
}

func TestCheckRowsFollowAPageThatMoves(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	x.UseTypes(poemRules("a"))
	m := newMem()
	m.put("old.md", page8("0000000001", "A", "type: poem\ncolour: red\n", "x"))
	syncAll(t, x, m, false)
	// The same page under a new name (a rename on save).
	m.put("new.md", page8("0000000001", "A", "type: poem\ncolour: red\n", "x"))
	delete(m.files, "old.md")
	if _, err := x.Put("new.md", m.files["new.md"].data, clock()); err != nil {
		t.Fatal(err)
	}
	v := listChecks(t, x, "", "")
	if got := strings.Join(summary(v.items), ","); got != "not_in_type new.md" {
		t.Fatalf("after the rename: %s", got)
	}
	if err := x.Forget([]string{"new.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if c := listChecks(t, x, "", "").counts["not_in_type"]; c != 0 {
		t.Fatalf("after forget: %d", c)
	}
}

func brokenOf(t *testing.T, x *Index) []string {
	t.Helper()
	var out []string
	for _, c := range listChecks(t, x, KindBrokenLink, "").items {
		d := detailOf(t, c)
		out = append(out, fmt.Sprintf("%s>%v@%v", c.Path, d["target"], d["line"]))
	}
	return out
}

func TestCheckBrokenLinksFollowTheLinksIndex(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	m := newMem()
	m.put("a.md", page8("0000000001", "A", "", "x\n"))
	// An id link to a, an id link to an unknown id, a path-only link to b.md
	// (not there yet), a link to a by path.
	m.put("s.md", page8("0000000002", "S", "", "[a](a.md \"0000000001\")\n\n[z](z.md \"0000000009\")\n\n[b](b.md)\n[a](a.md)\n"))
	syncAll(t, x, m, false)
	want := "s.md>z.md@7 s.md>b.md@9"
	if got := strings.Join(brokenOf(t, x), " "); got != want {
		t.Fatalf("after the sync: %s", got)
	}
	c := listChecks(t, x, KindBrokenLink, "").items[0]
	if c.ID != "0000000002" || c.Fix != "" || !strings.Contains(c.Message, "0000000009") || c.Rev != page.Rev(m.files["s.md"].data) {
		t.Fatalf("%+v", c)
	}
	// The page b appears (a save): its link is fine now.
	if _, err := x.Put("b.md", []byte(page8("0000000003", "B", "", "y\n")), clock()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(brokenOf(t, x), " "); got != "s.md>z.md@7" {
		t.Fatalf("after b: %s", got)
	}
	// a moves: the id link still finds it, the path-only one does not.
	m.put("b.md", page8("0000000003", "B", "", "y\n"))
	m.put("n/a.md", page8("0000000001", "A", "", "x\n"))
	delete(m.files, "a.md")
	if _, err := x.Refresh(m, []string{"a.md", "n/a.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(brokenOf(t, x), " "); got != "s.md>z.md@7 s.md>a.md@10" {
		t.Fatalf("after the move: %s", got)
	}
	// The page with id 0000000009 appears; b is removed.
	m.put("z.md", page8("0000000009", "Z", "", "z\n"))
	delete(m.files, "b.md")
	if _, err := x.Refresh(m, []string{"z.md", "b.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(brokenOf(t, x), " "); got != "s.md>b.md@9 s.md>a.md@10" {
		t.Fatalf("after z and b: %s", got)
	}
	if err := x.Remove("0000000009"); err != nil {
		t.Fatal(err)
	}
	// The page with the links is fixed by a save, then removed.
	m.put("s.md", page8("0000000002", "S", "", "[a](n/a.md)\n"))
	if _, err := x.Put("s.md", m.files["s.md"].data, clock()); err != nil {
		t.Fatal(err)
	}
	if got := brokenOf(t, x); len(got) != 0 {
		t.Fatalf("after the fix: %v", got)
	}
	m.put("s.md", page8("0000000002", "S", "", "[q](q.md)\n"))
	syncAll(t, x, m, false)
	if got := strings.Join(brokenOf(t, x), " "); got != "s.md>q.md@5" {
		t.Fatalf("after a sync: %s", got)
	}
	delete(m.files, "s.md")
	if err := x.Forget([]string{"s.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if n := listChecks(t, x, "", "").counts[KindBrokenLink]; n != 0 {
		t.Fatalf("after forget: %d", n)
	}
	// A rebuild records them in one go, at most maxRowsPerKind a page; a
	// reference link is on the line of its paragraph.
	var b strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, "[x%d](gone%d.md)\n", i, i)
	}
	b.WriteString("\n[r][d]\n\n[d]: r.md\n")
	m.put("many.md", page8("0000000004", "M", "", b.String()))
	syncAll(t, x, m, true)
	got := brokenOf(t, x)
	if len(got) != maxRowsPerKind || got[0] != "many.md>gone0.md@5" || got[19] != "many.md>gone19.md@24" {
		t.Fatalf("after the rebuild: %v", got)
	}
	// The same through the incremental path.
	if _, err := x.Put("many.md", []byte(page8("0000000004", "M", "", "a\n\n[r][d]\n\n[d]: r.md\n")), clock()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(brokenOf(t, x), " "); got != "many.md>r.md@7" {
		t.Fatalf("a reference link: %s", got)
	}
}
