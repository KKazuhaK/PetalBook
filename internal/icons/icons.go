// Package icons says what may be the icon and the colour of a folder, a page or
// a page type: a Lucide name the web app ships or one emoji (one grapheme
// cluster), and one of eight colours.
package icons

import (
	_ "embed"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxBytes is the longest icon: an emoji may be several code points long.
const MaxBytes = 64

// Colors are the colours an icon may have, each with a light and a dark shade
// the client has tuned. Empty is the colour of the text.
var Colors = []string{"red", "orange", "amber", "green", "teal", "blue", "purple", "pink"}

// lucide is the list of the names of the Lucide icons the web app ships, one
// per line (web/scripts/lucide-names.mjs writes it; a web test checks it).
//
//go:embed lucide.txt
var lucide string

var names = func() map[string]struct{} {
	m := make(map[string]struct{}, 2200)
	for _, n := range strings.Split(lucide, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			m[n] = struct{}{}
		}
	}
	return m
}()

// nameLike is what the client takes for a Lucide name rather than text.
var nameLike = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// IsName reports whether icon is a Lucide name the web app ships.
func IsName(icon string) bool {
	_, ok := names[icon]
	return ok
}

// Valid reports whether icon can be an icon: a Lucide name the web app ships,
// or one grapheme cluster of at most MaxBytes with no control character, space
// or markup. "" is not an icon.
func Valid(icon string) bool {
	if icon == "" || len(icon) > MaxBytes || !utf8.ValidString(icon) {
		return false
	}
	if nameLike.MatchString(icon) {
		return IsName(icon)
	}
	return oneCluster(icon)
}

// ValidColor reports whether c is one of Colors.
func ValidColor(c string) bool {
	for _, x := range Colors {
		if x == c {
			return true
		}
	}
	return false
}

// Of is icon when it is one, else "": what is read from a file written by hand.
func Of(icon string) string {
	if Valid(icon) {
		return icon
	}
	return ""
}

// ColorOf is c when it is one of Colors, else "".
func ColorOf(c string) string {
	if ValidColor(c) {
		return c
	}
	return ""
}

const zwj = 0x200d

func regional(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }

// base reports whether r can start a cluster (or follow a zero-width joiner).
func base(r rune) bool {
	switch {
	case r == utf8.RuneError, r == zwj, unicode.IsControl(r), unicode.IsSpace(r), unicode.Is(unicode.M, r):
		return false
	case strings.ContainsRune("<>&\"'`", r):
		return false
	case unicode.Is(unicode.Cf, r) && !regional(r):
		return false
	}
	return true
}

// extends reports whether r stays in the cluster of the rune before it: a
// mark (combining marks, variation selectors, the keycap), a skin tone or a
// tag character (the flags of regions).
func extends(r rune) bool {
	switch {
	case unicode.Is(unicode.M, r):
		return true
	case r >= 0x1f3fb && r <= 0x1f3ff: // skin tones
		return true
	case r >= 0xe0020 && r <= 0xe007f: // tags
		return true
	}
	return false
}

// oneCluster reports whether s is a single grapheme cluster, as far as icons
// go: a base, the runes that extend it, other bases joined to it by zero-width
// joiners, and a pair of regional indicators (a flag).
func oneCluster(s string) bool {
	rs := []rune(s)
	if len(rs) == 0 || !base(rs[0]) {
		return false
	}
	i := 1
	if regional(rs[0]) && i < len(rs) && regional(rs[i]) {
		i++
	}
	for i < len(rs) {
		r := rs[i]
		switch {
		case extends(r):
			i++
		case r == zwj && i+1 < len(rs) && base(rs[i+1]):
			i += 2
		default:
			return false
		}
	}
	return true
}
