package icons

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	good := []string{
		"book", "book-open", "file-text", "\U0001F4DA", "❤️", "\U0001F44D\U0001F3FD", "\U0001F469‍\U0001F4BB",
		"\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466", "\U0001F1E8\U0001F1F3", "1️⃣", "#️⃣",
		"\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F", "书", "A", "é", "é",
	}
	for _, s := range good {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	bad := []string{
		"", "constructor", "__proto__", "toString", "hasownproperty", "not-an-icon", "\U0001F4DA\U0001F4DA", "ab", "书本",
		"<b>", "<", "&", "\"", " \U0001F4DA", "\U0001F4DA ", "\n", "\x00", "a\u0007", "‍", "́", "​",
		"\U0001F1E8\U0001F1F3\U0001F1E8", "éx", "\xff", "\U0001F469‍", strings.Repeat("́", 40),
	}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
	if long := "\U0001F4DA" + strings.Repeat("️", 21); Valid(long) {
		t.Errorf("an icon of %d bytes is valid", len(long))
	}
}

func TestValidColor(t *testing.T) {
	for _, c := range Colors {
		if !ValidColor(c) {
			t.Errorf("ValidColor(%q) = false", c)
		}
	}
	for _, c := range []string{"", "Red", "constructor", "#ff0000", "grey", "__proto__"} {
		if ValidColor(c) {
			t.Errorf("ValidColor(%q) = true", c)
		}
	}
	if len(Colors) != 8 {
		t.Errorf("%d colours, want eight", len(Colors))
	}
}

func TestOf(t *testing.T) {
	if Of("constructor") != "" || Of("book-open") != "book-open" || ColorOf("teal") != "teal" || ColorOf("x") != "" {
		t.Error("Of and ColorOf do not keep only what is valid")
	}
}
