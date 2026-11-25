package sentence

import "testing"

func TestSentences(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"No such page.":           "No such page.",
		"no such page":            "No such page.",
		"it broke..":              "It broke.",
		"Wait for it...":          "Wait for it...",
		"  Done ":                 "Done.",
		"Really?":                 "Really?",
		"https://x.example/a.git": "Https://x.example/a.git.",
		"已经有了。":                   "已经有了。",
	} {
		if got := Of(in); got != want {
			t.Errorf("Of(%q) = %q, want %q", in, got, want)
		}
	}
	if End("a name") != "a name." {
		t.Error("End must not change the case")
	}
}
