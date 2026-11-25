package index

import "testing"

// An icon or a colour written by hand that is none is read as none: a client
// that looks the icon up by name must never be given "constructor".
func TestAnIconThatIsNoneIsNotIndexed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ icon, color, wantIcon, wantColor string }{
		{"constructor", "__proto__", "", ""},
		{"toString", "mauve", "", ""},
		{"book-open", "teal", "book-open", "teal"},
		{"\"\U0001F4DA\"", "pink", "\U0001F4DA", "pink"},
	} {
		data := []byte("---\nid: 01JD3K8Q7Z\ntitle: x\nicon: " + c.icon + "\ncolor: " + c.color + "\n---\nb\n")
		e, p := BuildEntry("x.md", data, 1)
		if p != nil {
			t.Fatalf("problem: %+v", p)
		}
		if e.Icon != c.wantIcon || e.Color != c.wantColor {
			t.Errorf("icon %s color %s: indexed %q %q", c.icon, c.color, e.Icon, e.Color)
		}
	}
}
