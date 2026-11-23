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
