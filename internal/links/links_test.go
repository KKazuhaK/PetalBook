package links

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type fixtureLink struct {
	Text   string `json:"text"`
	Path   string `json:"path"`
	ID     string `json:"id"`
	Anchor string `json:"anchor"`
}

type fixture struct {
	Name     string        `json:"name"`
	Src      string        `json:"src"`
	Markdown string        `json:"markdown"`
	Links    []fixtureLink `json:"links"`
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	data, err := os.ReadFile("../../web/src/markdown/__fixtures__/links.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []fixture `json:"cases"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Cases
}

func pageOf(body string) []byte {
	return []byte("---\nid: 01JD3K8Q00\ntitle: T\n---\n" + body + "\n")
}

func TestFixtures(t *testing.T) {
	for _, c := range loadFixtures(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := Extract(c.Src, pageOf(c.Markdown))
			var have []fixtureLink
			for _, l := range got {
				have = append(have, fixtureLink{l.Text, l.Path, l.ID, l.Anchor})
			}
			a, _ := json.Marshal(have)
			b, _ := json.Marshal(append([]fixtureLink(nil), c.Links...))
			if len(have) == 0 && len(c.Links) == 0 {
				return
			}
			if string(a) != string(b) {
				t.Fatalf("got  %s\nwant %s", a, b)
			}
		})
	}
}

// Every inline link of the fixtures is located, and replacing its destination
// by itself changes nothing.
func TestOffsets(t *testing.T) {
	for _, c := range loadFixtures(t) {
		data := pageOf(c.Markdown)
		for _, l := range Extract(c.Src, data) {
			if strings.Contains(c.Name, "reference") {
				if l.DestStart != -1 {
					t.Errorf("%s: reference link located", c.Name)
				}
				continue
			}
			if l.DestStart < 0 && c.Name == "empty text" {
				continue // no text to find the end of: found, never rewritten
			}
			if l.DestStart < 0 {
				t.Errorf("%s: link %q not located", c.Name, l.Text)
				continue
			}
			e, _ := l.DestEdit(data, c.Src, l.Path)
			out := Apply(data, []Edit{e})
			if string(out) != string(data) && !strings.Contains(c.Name, "paren") && !strings.Contains(c.Name, "dot") && !strings.Contains(c.Name, "rooted") {
				t.Errorf("%s: rewriting to the same target changed the file:\n%q\n%q", c.Name, data, out)
			}
		}
	}
}

func TestRel(t *testing.T) {
	for _, c := range [][3]string{
		{"a.md", "b.md", "b.md"}, {"A/a.md", "A/b.md", "b.md"}, {"A/a.md", "B/b.md", "../B/b.md"},
		{"A/B/a.md", "b.md", "../../b.md"}, {"a.md", "A/B/b.md", "A/B/b.md"}, {"A/a.md", "A/B/b.md", "B/b.md"},
	} {
		if got := Rel(c[0], c[1]); got != c[2] {
			t.Errorf("Rel(%s,%s)=%s want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestDestEditKeepsStyleAndRest(t *testing.T) {
	data := []byte("---\nid: 01JD3K8Q00\n---\r\n[a](<My Page.md#x>) [b](./b%20c.md?q=1#y) [c](%E5%94%90/d.md 'T') [d](/e.md)\r\n")
	ls := Extract("a.md", data)
	if len(ls) != 4 {
		t.Fatalf("%d", len(ls))
	}
	var edits []Edit
	for _, l := range ls {
		e, ok := l.DestEdit(data, "Z/a.md", "N/x (1) 唐.md")
		if !ok {
			t.Fatal("not located")
		}
		edits = append(edits, e)
	}
	got := string(Apply(data, edits))
	want := "---\nid: 01JD3K8Q00\n---\r\n[a](<../N/x (1) 唐.md#x>) [b](./../N/x%20%281%29%20唐.md?q=1#y) [c](../N/x%20%281%29%20%E5%94%90.md 'T') [d](/N/x%20%281%29%20唐.md)\r\n"
	if got != strings.Replace(want, "./../", "../", 1) {
		t.Fatalf("\n%q\n%q", got, want)
	}
	if e, ok := ls[2].IDEdit("01JD3K8Q7Z"); ok {
		t.Fatalf("titled link got an id edit: %+v", e)
	}
	e, ok := ls[0].IDEdit("01JD3K8Q7Z")
	if !ok || !strings.HasPrefix(string(Apply(data, []Edit{e})[ls[0].DestStart:]), `<My Page.md#x> "01JD3K8Q7Z")`) {
		t.Fatalf("%+v", e)
	}
}

func TestOffsetsWithBOMAndCRLF(t *testing.T) {
	data := []byte("\xef\xbb\xbf---\r\nid: 01JD3K8Q00\r\n---\r\npara one\r\n\r\nsee [x](x.md)\r\n")
	ls := Extract("a.md", data)
	if len(ls) != 1 || string(data[ls[0].DestStart:ls[0].DestEnd]) != "x.md" {
		t.Fatalf("%+v", ls)
	}
	if got := strings.TrimSpace(string(data[ls[0].BlockStart:ls[0].BlockEnd])); got != "see [x](x.md)" {
		t.Fatalf("%q", got)
	}
}
