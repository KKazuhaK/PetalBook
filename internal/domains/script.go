package domains

import (
	"strings"
	"unicode"
)

// MixedScript says whether a label of the domain (a key) mixes scripts, the
// way a lookalike of another name is made (a Cyrillic "а" in "аpple.com"). It
// follows the "highly restrictive" level of Unicode TS #39: one script per
// label, or Latin with the scripts written together in Japan (Han, Hiragana,
// Katakana), Korea (Han, Hangul) or China (Han, Bopomofo). Digits, hyphens and
// marks belong to no script. It only warns: such a domain may be claimed.
func MixedScript(key string) bool {
	for l := range strings.SplitSeq(Display(key), ".") {
		if mixedLabel(l) {
			return true
		}
	}
	return false
}

var scripts = []struct {
	name string
	t    *unicode.RangeTable
}{
	{"Latin", unicode.Latin}, {"Cyrillic", unicode.Cyrillic}, {"Greek", unicode.Greek}, {"Armenian", unicode.Armenian},
	{"Hebrew", unicode.Hebrew}, {"Arabic", unicode.Arabic}, {"Han", unicode.Han}, {"Hiragana", unicode.Hiragana},
	{"Katakana", unicode.Katakana}, {"Hangul", unicode.Hangul}, {"Bopomofo", unicode.Bopomofo}, {"Thai", unicode.Thai},
	{"Devanagari", unicode.Devanagari}, {"Georgian", unicode.Georgian},
}

// cjk are the sets of scripts written together, each of which Latin may join.
var cjk = []map[string]bool{
	{"Han": true, "Hiragana": true, "Katakana": true},
	{"Han": true, "Hangul": true},
	{"Han": true, "Bopomofo": true},
}

func scriptOf(r rune) string {
	if !unicode.IsLetter(r) {
		return ""
	}
	for _, s := range scripts {
		if unicode.Is(s.t, r) {
			return s.name
		}
	}
	return "other:" + string(r) // a script not listed: counted as its own
}

func mixedLabel(l string) bool {
	seen := map[string]bool{}
	for _, r := range l {
		if s := scriptOf(r); s != "" {
			if strings.HasPrefix(s, "other:") {
				s = "other"
			}
			seen[s] = true
		}
	}
	if len(seen) <= 1 {
		return false
	}
	rest := map[string]bool{}
	for s := range seen {
		if s != "Latin" {
			rest[s] = true
		}
	}
	for _, set := range cjk {
		within := true
		for s := range rest {
			within = within && set[s]
		}
		if within {
			return false
		}
	}
	return true
}
