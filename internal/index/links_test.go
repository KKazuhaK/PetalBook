package index

import (
	"fmt"
	"testing"
)

func linkPage(id, title, body string) string {
	return "---\nid: " + id + "\ntitle: " + title + "\n---\n" + body
}

func TestLinksAreKeptPerPage(t *testing.T) {
	t.Parallel()
	x, src := openIndex(t), newMem()
	src.put("a.md", linkPage("0000000001", "A", "x\n"))
	src.put("d/b.md", linkPage("0000000002", "B", "[a](../a.md \"0000000001\") [a2](../a.md#h) [none](nope.md) [self](b.md) [ext](https://e.com/z.md)\n"))
	src.put("c.md", linkPage("0000000003", "C", "[b](d/b.md)\n"))
	if _, err := x.Sync(src, false); err != nil {
		t.Fatal(err)
	}
	out, ok, err := x.OutLinks("0000000002")
	if err != nil || !ok || len(out) != 3 {
		t.Fatalf("%+v %v %v", out, ok, err)
	}
	if out[0].Target == nil || out[0].Target.Label != "A" || out[1].Target == nil || out[2].Target != nil {
		t.Fatalf("%+v", out)
	}
	items, _, total, err := x.Backlinks("0000000001", "", 10)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != "0000000002" {
		t.Fatalf("%+v %v", items, err)
	}
	// By path only: found while the target is where it is, lost when it moves, found again.
	if _, _, total, _ := x.Backlinks("0000000002", "", 10); total != 1 {
		t.Fatalf("total %d", total)
	}
	src.put("e/b.md", linkPage("0000000002", "B", "no links now\n"))
	delete(src.files, "d/b.md")
	if _, err := x.Sync(src, false); err != nil {
		t.Fatal(err)
	}
	if _, _, total, _ := x.Backlinks("0000000002", "", 10); total != 0 {
		t.Fatalf("total %d", total)
	}
	if _, _, total, _ := x.Backlinks("0000000001", "", 10); total != 0 {
		t.Fatalf("links of a page that lost them: %d", total)
	}
	st, err := x.StaleLinks(nil)
	if err != nil || len(st) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestStaleLinksFindMovedTargets(t *testing.T) {
	t.Parallel()
	x, src := openIndex(t), newMem()
	src.put("a.md", linkPage("0000000001", "A", "x\n"))
	src.put("r.md", linkPage("0000000002", "R", "[a](a.md \"0000000001\")\n"))
	src.put("r2.md", linkPage("0000000004", "R2", "[a](a.md \"0000000001\")\n"))
	x.Sync(src, false)
	delete(src.files, "a.md")
	src.put("n/a.md", linkPage("0000000001", "A", "x\n"))
	x.Sync(src, false)
	st, err := x.StaleLinks(nil)
	if err != nil || len(st) != 2 || st[0].Target.Path != "n/a.md" {
		t.Fatalf("%+v %v", st, err)
	}
	if st, _ := x.StaleLinks([]string{"0000000009"}); len(st) != 0 {
		t.Fatal(st)
	}
	for i := range 3 {
		src.put(fmt.Sprintf("m%d.md", i), linkPage(fmt.Sprintf("00000001%02d", i), "M", "[a](n/a.md)\n"))
	}
	x.Sync(src, false)
	refs, err := x.Referrers(nil, []string{"n/a.md"})
	if err != nil || len(refs) != 3 {
		t.Fatalf("%+v %v", refs, err)
	}
}
