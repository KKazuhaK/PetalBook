package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(files); i += 2 {
		w, err := zw.Create(files[i])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(files[i+1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSingleFile(t *testing.T) {
	for _, c := range []struct{ name, data, title, body string }{
		{"note.md", "# 暮雪\n\n正文\n", "暮雪", "正文\n"},
		{"note.md", "\n\n# 暮雪 ##\n正文\n", "暮雪", "正文\n"},
		{"雨停之后.markdown", "没有标题的正文\n", "雨停之后", "没有标题的正文\n"},
		{"a.md", "---\ntitle: 头部的\n---\n# 不是标题\n", "头部的", "# 不是标题\n"},
		{"a.md", "## 二级\n", "a", "## 二级\n"},
		{"a.md", "#标签 不是标题\n", "a", "#标签 不是标题\n"},
		{"a.md", "# 只有标题", "只有标题", ""},
		{`C:\dir\win.md`, "x\n", "win", "x\n"},
	} {
		p, err := Single(c.name, []byte(c.data), 0)
		if err != nil {
			t.Fatalf("%q: %v", c.name, err)
		}
		pg := p.Pages[0]
		if pg.Meta.Title() != c.title || pg.Body != c.body {
			t.Errorf("%q %q: title %q body %q", c.name, c.data, pg.Meta.Title(), pg.Body)
		}
	}
}

func TestSingleFileHeader(t *testing.T) {
	p, err := Single("a.md", []byte("---\nid: 0123456789\ntitle: T\ntags: [a, b]\ncreated: 2020-05-06\nseries: 冬\n---\nbody\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	m := p.Pages[0].Meta
	if m.Index("id") >= 0 {
		t.Fatal("the id came along")
	}
	if m.String("created") != "2020-05-06" || m.String("series") != "冬" || len(m.Tags()) != 2 {
		t.Fatalf("%v", m)
	}
	// A creation date that is not one is dropped; a time keeps its day.
	p, _ = Single("a.md", []byte("---\ncreated: yesterday\n---\nx\n"), 0)
	if p.Pages[0].Meta.Index("created") >= 0 {
		t.Fatalf("%v", p.Pages[0].Meta)
	}
	p, _ = Single("a.md", []byte("---\ncreated: 2021-02-03T10:00:00Z\n---\nx\n"), 0)
	if p.Pages[0].Meta.String("created") != "2021-02-03" {
		t.Fatalf("%v", p.Pages[0].Meta)
	}
	// A header that does not parse stays in the body as text.
	p, _ = Single("a.md", []byte("---\ntitle: [x\n---\nbody\n"), 0)
	if !strings.Contains(p.Pages[0].Body, "title: [x") || p.Pages[0].Meta.Title() != "a" {
		t.Fatalf("%+v", p.Pages[0])
	}
}

func TestSingleFileRefusals(t *testing.T) {
	for _, c := range []struct{ name, data string }{{"", "x"}, {"a.txt", "x"}, {"a.md", "\xff\xfe"}, {"  ", "x"}} {
		var ie *Error
		if _, err := Single(c.name, []byte(c.data), 0); !errors.As(err, &ie) {
			t.Errorf("%q: %v", c.name, err)
		}
	}
}

func TestZip(t *testing.T) {
	data := zipOf(t,
		"notes/一.md", "# 一\n\n![图](img/a%20b.png) [附件](<files/doc (1).pdf>) [别页](二.md) [外](https://x.test/a.png)\n![同一张](./img/a%20b.png)\n",
		"notes/二.md", "二的正文 ![缺](missing.png) ![上面](../top.png)\n",
		"notes/img/a b.png", "PNG1",
		"notes/files/doc (1).pdf", "PDF1",
		"top.png", "TOP",
		"notes/unused.docx", "DOC",
		"__MACOSX/notes/._一.md", "junk",
		".hidden/x.md", "# hidden\n",
		"../escape.md", "# no\n",
		"big.md", "# big\n\n![大](huge.bin)\n",
		"huge.bin", strings.Repeat("x", 50),
	)
	p, err := Zip(data, Options{MaxAsset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Pages) != 3 {
		t.Fatalf("%d pages: %+v", len(p.Pages), p.Pages)
	}
	by := map[string]Page{}
	for _, pg := range p.Pages {
		by[pg.Source] = pg
	}
	one := by["notes/一.md"]
	if one.Folder != "notes" || one.Meta.Title() != "一" || len(one.Assets) != 2 {
		t.Fatalf("%+v", one)
	}
	if one.Assets[0].Name != "a b.png" || string(one.Assets[0].Data) != "PNG1" || one.Assets[1].Name != "doc (1).pdf" {
		t.Fatalf("%+v", one.Assets)
	}
	want := "![图](<" + one.Assets[0].Token + ">) [附件](<" + one.Assets[1].Token + ">) [别页](二.md) [外](https://x.test/a.png)\n![同一张](<" + one.Assets[0].Token + ">)\n"
	if one.Body != want {
		t.Fatalf("body:\n%q\nwant\n%q", one.Body, want)
	}
	two := by["notes/二.md"]
	if len(two.Assets) != 1 || string(two.Assets[0].Data) != "TOP" || !strings.Contains(two.Body, "(missing.png)") {
		t.Fatalf("%+v", two)
	}
	big := by["big.md"]
	if len(big.Assets) != 0 || !strings.Contains(big.Body, "(huge.bin)") {
		t.Fatalf("%+v", big)
	}
	reasons := map[string]string{}
	for _, s := range p.Skipped {
		reasons[s.Path] = s.Reason
	}
	if !strings.Contains(reasons["notes/unused.docx"], "no page refers") || !strings.Contains(reasons["huge.bin"], "larger than") {
		t.Fatalf("%v", reasons)
	}
	for path := range reasons {
		if strings.Contains(path, "MACOSX") || strings.Contains(path, "hidden") || strings.Contains(path, "escape") {
			t.Fatalf("%q was looked at", path)
		}
	}
}

func TestZipLinksInCodeAreLeftAlone(t *testing.T) {
	data := zipOf(t, "a.md", "`![x](p.png)` and ![y](p.png)\n\n```\n![z](p.png)\n```\n\n~~~md\n![w](p.png)\n~~~\n", "p.png", "P")
	p, err := Zip(data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	pg := p.Pages[0]
	tok := pg.Assets[0].Token
	want := "`![x](p.png)` and ![y](<" + tok + ">)\n\n```\n![z](p.png)\n```\n\n~~~md\n![w](p.png)\n~~~\n"
	if pg.Body != want {
		t.Fatalf("%q", pg.Body)
	}
}

func TestZipRefusals(t *testing.T) {
	var ie *Error
	if _, err := Zip([]byte("PK\x03\x04 not really"), Options{}); !errors.As(err, &ie) {
		t.Fatal(err)
	}
	if _, err := Zip(zipOf(t, "a.txt", "x"), Options{}); !errors.As(err, &ie) || !strings.Contains(ie.Msg, "no Markdown") {
		t.Fatal(err)
	}
	// What is read counts against the total, compressed or not.
	bomb := zipOf(t, "a.md", strings.Repeat("a", 1<<16), "b.md", strings.Repeat("b", 1<<16))
	if len(bomb) > 1<<12 {
		t.Fatalf("the test zip did not compress: %d", len(bomb))
	}
	if _, err := Zip(bomb, Options{MaxTotal: 1 << 16}); !errors.As(err, &ie) || !strings.Contains(ie.Msg, "unpacks to more") {
		t.Fatal(err)
	}
	// A folder a space cannot have: the file is skipped, the rest imported.
	p, err := Zip(zipOf(t, "assets/x.md", "# x\n", "ok.md", "# ok\n"), Options{ValidFolder: func(rel string) error {
		if rel == "assets" {
			return errors.New("reserved")
		}
		return nil
	}})
	if err != nil || len(p.Pages) != 1 || len(p.Skipped) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestSkippedListIsCapped(t *testing.T) {
	files := []string{"a.md", "# a\n"}
	for i := 0; i < MaxSkipped+7; i++ {
		files = append(files, "f"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26)+".bin", "x")
	}
	p, err := Zip(zipOf(t, files...), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Skipped) != MaxSkipped || p.SkippedMore == 0 {
		t.Fatalf("%d %d", len(p.Skipped), p.SkippedMore)
	}
}

func TestPageSizeLimit(t *testing.T) {
	var ie *Error
	if _, err := Single("a.md", []byte(strings.Repeat("x", 100)), 50); !errors.As(err, &ie) {
		t.Fatal(err)
	}
	// In a zip the large file is left out and named; the rest is imported.
	p, err := Zip(zipOf(t, "big.md", strings.Repeat("x", 1<<16), "ok.md", "# ok\n"), Options{MaxPage: 1 << 10})
	if err != nil || len(p.Pages) != 1 || len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0].Reason, "larger than") {
		t.Fatalf("%v %+v", err, p)
	}
}

// Lines made to be slow to scan are scanned in time proportional to their
// length, or not at all.
func TestHostileLinesAreCheap(t *testing.T) {
	short := strings.Repeat("`x`[a](b) ", 6000)  // under the line limit: scanned
	long := strings.Repeat("`x`[a](b) ", 200000) // over it: left as written
	ticks := strings.Repeat("` `` ``` ", 7000)   // many unclosed runs
	body := short + "\n" + long + "\n" + ticks + "\n"
	n := 0
	out := rewriteLinks(body, func(dest string) (string, bool) { n++; return "", false })
	if out != body {
		t.Fatal("the text was changed")
	}
	if n != 6000 {
		t.Fatalf("%d links looked at", n)
	}
}

func TestCodeSpans(t *testing.T) {
	for line, want := range map[string]int{
		"`a` and `b`":       2,
		"``a ` b`` `c`":     2,
		"`unclosed":         0,
		"``` not closed ``": 0,
		"` `` ` ``":         1,
	} {
		if got := len(codeSpans(line)); got != want {
			t.Errorf("%q: %d spans, want %d", line, got, want)
		}
	}
}
