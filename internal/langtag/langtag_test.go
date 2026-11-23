package langtag

import (
	"slices"
	"testing"
)

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"zh": "zh-Hans", "ZH": "zh-Hans", "zh-Hans": "zh-Hans", "zh-hant": "zh-Hant", "ZH-cn": "zh-CN", "zh-CN": "zh-CN",
		"en": "en", "EN": "en", "pt-br": "pt-BR", "pt-BR": "pt-BR", "en-gb": "en-GB", "ja": "ja", "tlh": "tlh",
	} {
		got, ok := Canonical(in)
		if !ok || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "cn", "english", "zh_CN", "xx", "und", "x-foo", "en-", "-en", "e", "zh Hans", "zh-Hans-", "中文", "en-GB-u-ca-x-y-z-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if got, ok := Canonical(in); ok {
			t.Errorf("Canonical(%q) = %q; want a refusal", in, got)
		}
	}
}

func TestRead(t *testing.T) {
	// Values saved before the rule are read as their canonical tags; what cannot be one is left as it is.
	for in, want := range map[string]string{"zh": "zh-Hans", "pt-br": "pt-BR", "en": "en", "": "", "Chinese!": "chinese!"} {
		if got := Read(in); got != want {
			t.Errorf("Read(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	have := []string{"zh-Hans", "zh-Hant", "en", "pt-BR"}
	for in, want := range map[string]string{
		"zh-CN": "zh-Hans", "zh-SG": "zh-Hans", "zh": "zh-Hans", "zh-Hans": "zh-Hans", "zh-TW": "zh-Hant", "zh-HK": "zh-Hant", "zh-hant-tw": "zh-Hant",
		"en-GB": "en", "en": "en", "en-US": "en", "pt": "pt-BR", "pt-PT": "pt-BR",
	} {
		got, ok := Match(in, have)
		if !ok || got != want {
			t.Errorf("Match(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"fr", "ja", "", "cn", "!!"} {
		if got, ok := Match(in, have); ok {
			t.Errorf("Match(%q) = %q; want none", in, got)
		}
	}
	if got, ok := Match("zh-TW", []string{"zh-Hans", "en"}); !ok || got != "zh-Hans" {
		t.Errorf("a script that is not there still finds its language: %q %v", got, ok)
	}
	if _, ok := Match("en", nil); ok {
		t.Error("nothing to match")
	}
	// Old values among the configured ones are read as their tags.
	if got, ok := Match("zh-CN", []string{"zh", "en"}); !ok || got != "zh-Hans" {
		t.Errorf("%q %v", got, ok)
	}
}

func TestKnown(t *testing.T) {
	k := Known()
	if len(k) < 30 {
		t.Fatalf("%d", len(k))
	}
	seen := map[string]bool{}
	for _, l := range k {
		if c, ok := Canonical(l.Tag); !ok || c != l.Tag {
			t.Errorf("%q is not canonical", l.Tag)
		}
		if seen[l.Tag] || l.Name == "" || l.English == "" {
			t.Errorf("%+v", l)
		}
		seen[l.Tag] = true
	}
	if k[0].Tag != "zh-Hans" || k[0].Name != "中文（简体）" || k[0].English != "Chinese (Simplified)" {
		t.Errorf("%+v", k[0])
	}
	if !slices.ContainsFunc(k, func(l Language) bool { return l.Tag == "en" && l.Name == "English" }) {
		t.Error("English is missing")
	}
}
