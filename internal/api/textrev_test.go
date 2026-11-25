package api_test

import (
	"strings"
	"testing"
)

// The page is open in an editor while a page it links to is renamed: the server
// rewrites the link in the file. The editor's save, based on the rev before,
// gets the same rewrite (a rebase), so the answer's body differs from the text
// that was sent. The answer says, with textRev, which rev that text stands for.
// An editor that kept typing while the save was out bases its next save on
// textRev, and the server makes the rewrite again on top of the new typing:
// neither is lost.
func TestTypingDuringASaveKeepsItsLinkRewrites(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tg := f.create("B", map[string]any{"title": "乙"}, "x\n")
	// A link with a title of its own carries no id: only its path names the page.
	const text = "[看](../B/乙.md \"看乙\")\n"
	a := f.create("A", map[string]any{"title": "甲"}, text)
	aURL := base + "/pages/" + a["id"].(string)

	// The target is renamed while the editor has a open: the link follows it.
	if code, _, raw := f.do("PUT", base+"/pages/"+tg["id"].(string), map[string]any{"baseRev": tg["rev"], "meta": map[string]any{"title": "丙"}, "body": "x\n"}); code != 200 {
		t.Fatalf("rename: %d %s", code, raw)
	}

	// The editor saves on the rev it read, with the old link.
	code, r, raw := f.do("PUT", aURL, map[string]any{"baseRev": a["rev"], "meta": map[string]any{"title": "甲"}, "body": text + "一\n"})
	if code != 200 || !strings.Contains(r["body"].(string), "](../B/丙.md ") {
		t.Fatalf("save: %d %s", code, raw)
	}
	textRev, _ := r["textRev"].(string)
	if textRev == "" || textRev == r["rev"] {
		t.Fatalf("a save whose links were rewritten needs a textRev of its own: %s", raw)
	}

	// It typed on while the save was out: its text still has the old path.
	code, r2, raw := f.do("PUT", aURL, map[string]any{"baseRev": textRev, "meta": map[string]any{"title": "甲"}, "body": text + "一\n二\n"})
	if code != 200 {
		t.Fatalf("save on textRev: %d %s", code, raw)
	}
	if b := r2["body"].(string); !strings.Contains(b, "](../B/丙.md ") || strings.Contains(b, "乙.md") || !strings.Contains(b, "一\n二\n") {
		t.Fatalf("the rewrite or the typing was lost: %q", b)
	}
	// That save was rebased again (its text had the old path): its textRev is its own.
	if r2["textRev"] == r2["rev"] {
		t.Fatalf("%s", raw)
	}
	// A save of the server's text on its rev rewrites nothing: textRev is rev.
	code, r3, raw := f.do("PUT", aURL, map[string]any{"baseRev": r2["rev"], "meta": map[string]any{"title": "甲"}, "body": r2["body"].(string) + "三\n"})
	if code != 200 || r3["textRev"] != r3["rev"] {
		t.Fatalf("%d %s", code, raw)
	}
	// GET carries no textRev: it answers no save.
	if _, g, raw := f.do("GET", aURL, nil); g["textRev"] != nil {
		t.Fatalf("GET: %s", raw)
	}
}

// The editor's way: drafts and commits. A draft stored on textRev is not
// stale, and its commit keeps both the rewrite and what was typed.
func TestDraftOnTextRevCommitsWithTheRewrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tg := f.create("B", map[string]any{"title": "乙"}, "x\n")
	const text = "[看](../B/乙.md \"看乙\")\n"
	a := f.create("A", map[string]any{"title": "甲"}, text)
	url := base + "/pages/" + a["id"].(string)
	if code, _, raw := f.do("PUT", base+"/pages/"+tg["id"].(string), map[string]any{"baseRev": tg["rev"], "meta": map[string]any{"title": "丙"}, "body": "x\n"}); code != 200 {
		t.Fatalf("rename: %d %s", code, raw)
	}

	code, d, raw := f.do("PUT", url+"/draft", map[string]any{"prev": "", "baseRev": a["rev"], "meta": map[string]any{"title": "甲"}, "body": text + "一\n"})
	if code != 200 || d["stale"] != false {
		t.Fatalf("draft: %d %s", code, raw)
	}
	code, r, raw := f.do("POST", url+"/commit", map[string]any{"version": d["version"]})
	if code != 200 || !strings.Contains(r["body"].(string), "](../B/丙.md ") {
		t.Fatalf("commit: %d %s", code, raw)
	}
	textRev, _ := r["textRev"].(string)
	if textRev == "" || textRev == r["rev"] {
		t.Fatalf("commit: %s", raw)
	}

	code, d, raw = f.do("PUT", url+"/draft", map[string]any{"prev": "", "baseRev": textRev, "meta": map[string]any{"title": "甲"}, "body": text + "一\n二\n"})
	if code != 200 || d["stale"] != false {
		t.Fatalf("a draft on textRev: %d %s", code, raw)
	}
	code, r, raw = f.do("POST", url+"/commit", map[string]any{"version": d["version"]})
	if code != 200 {
		t.Fatalf("commit: %d %s", code, raw)
	}
	if b := r["body"].(string); !strings.Contains(b, "](../B/丙.md ") || strings.Contains(b, "乙.md") || !strings.Contains(b, "一\n二\n") {
		t.Fatalf("%q", b)
	}
}
