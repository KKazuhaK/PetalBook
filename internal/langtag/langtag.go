// Package langtag is how the daily text names and matches languages: BCP 47
// language tags in canonical form (zh-Hans, en, pt-BR), and a reader's
// language matched to the ones that are configured the BCP 47 way (zh-CN finds
// zh-Hans, en-GB finds en).
package langtag

import (
	"strings"

	"golang.org/x/text/language"
)

// MaxLen is the longest tag taken.
const MaxLen = 35

func wellFormedChars(s string) bool {
	if s == "" || len(s) > MaxLen {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Canonical is the canonical form of a well-formed language tag whose
// language subtag is in the IANA registry: `ZH-cn` is `zh-CN`, `pt-br` is
// `pt-BR`, and `zh` alone is `zh-Hans`. ok is false for anything else (`cn`,
// `english`, `zh_CN`, `und`, a private-use tag).
func Canonical(s string) (string, bool) {
	if !wellFormedChars(s) {
		return "", false
	}
	tag, err := language.Parse(s)
	if err != nil {
		return "", false
	}
	if tag == language.Und || !hasBase(tag) {
		return "", false
	}
	out := tag.String()
	if out == "zh" {
		out = "zh-Hans"
	}
	return out, true
}

// Read is a tag as written: the canonical tag when it is one (`zh` or
// `pt-br` is read as `zh-Hans` or `pt-BR`), else the text in lower case.
func Read(s string) string {
	if c, ok := Canonical(s); ok {
		return c
	}
	return strings.ToLower(s)
}

// Match is the one of have (as a canonical tag) a reader's language is matched to, the BCP 47 way:
// zh-CN and zh-SG find zh-Hans, zh-TW and zh-HK find zh-Hant, en-GB finds en.
// A language written in another script is still its language (zh-TW finds
// zh-Hans when there is no zh-Hant); ok is false for another language.
func Match(reader string, have []string) (string, bool) {
	if len(have) == 0 || !wellFormedChars(reader) {
		return "", false
	}
	r, err := language.Parse(reader)
	if err != nil {
		return "", false
	}
	if r == language.Und || !hasBase(r) {
		return "", false
	}
	tags := make([]language.Tag, 0, len(have))
	names := make([]string, 0, len(have))
	for _, h := range have {
		c, ok := Canonical(h)
		if !ok {
			continue
		}
		tags = append(tags, language.Make(c))
		names = append(names, c)
	}
	if len(tags) == 0 {
		return "", false
	}
	_, i, conf := language.NewMatcher(tags).Match(r)
	if conf <= language.No || i < 0 || i >= len(names) {
		return "", false
	}
	return names[i], true
}

// Language is a language a client offers to pick.
type Language struct {
	Tag     string `json:"tag"`
	Name    string `json:"name"`
	English string `json:"english"`
}

var known = []Language{
	{"zh-Hans", "中文（简体）", "Chinese (Simplified)"},
	{"zh-Hant", "中文（繁體）", "Chinese (Traditional)"},
	{"en", "English", "English"},
	{"ja", "日本語", "Japanese"},
	{"ko", "한국어", "Korean"},
	{"fr", "français", "French"},
	{"de", "Deutsch", "German"},
	{"es", "español", "Spanish"},
	{"pt-BR", "português (Brasil)", "Portuguese (Brazil)"},
	{"pt-PT", "português (Portugal)", "Portuguese (Portugal)"},
	{"ru", "русский", "Russian"},
	{"it", "italiano", "Italian"},
	{"nl", "Nederlands", "Dutch"},
	{"pl", "polski", "Polish"},
	{"tr", "Türkçe", "Turkish"},
	{"uk", "українська", "Ukrainian"},
	{"cs", "čeština", "Czech"},
	{"sv", "svenska", "Swedish"},
	{"da", "dansk", "Danish"},
	{"nb", "norsk bokmål", "Norwegian Bokmål"},
	{"fi", "suomi", "Finnish"},
	{"el", "Ελληνικά", "Greek"},
	{"he", "עברית", "Hebrew"},
	{"ar", "العربية", "Arabic"},
	{"fa", "فارسی", "Persian"},
	{"hi", "हिन्दी", "Hindi"},
	{"bn", "বাংলা", "Bengali"},
	{"ta", "தமிழ்", "Tamil"},
	{"th", "ไทย", "Thai"},
	{"vi", "Tiếng Việt", "Vietnamese"},
	{"id", "Indonesia", "Indonesian"},
	{"ms", "Melayu", "Malay"},
	{"fil", "Filipino", "Filipino"},
	{"ro", "română", "Romanian"},
	{"hu", "magyar", "Hungarian"},
	{"bg", "български", "Bulgarian"},
	{"ca", "català", "Catalan"},
	{"la", "Latina", "Latin"},
	{"en-GB", "English (United Kingdom)", "English (United Kingdom)"},
	{"es-419", "español (Latinoamérica)", "Spanish (Latin America)"},
	{"yue-Hant", "粵語（繁體）", "Cantonese (Traditional)"},
}

// Known are the common languages a client offers to pick from; any other
// valid tag may still be typed.
func Known() []Language { return append([]Language(nil), known...) }

// hasBase is whether the tag names a language (a private-use tag does not).
func hasBase(t language.Tag) bool {
	_, c := t.Base()
	return c != language.No
}
