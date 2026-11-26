package settings_test

import (
	"testing"

	"petalbook.app/server/internal/settings"
)

// The nearest value a restriction allows: for a choice the next stricter
// allowed one, or the strictest allowed when none is stricter.
func TestTheNearestAllowedChoice(t *testing.T) {
	d := &settings.Def{Key: "k", Kind: settings.Choice, Choices: []string{"a", "b", "c", "d"}}
	for _, c := range []struct {
		opts []any
		v    string
		want string
	}{
		{[]any{"c", "a"}, "b", "c"},
		{[]any{"a", "b"}, "d", "b"},
		{[]any{"b", "d"}, "b", "b"},
		{[]any{"d"}, "a", "d"},
	} {
		l := settings.Lower{Mode: settings.Restrict, Options: c.opts}
		if got := l.Nearest(d, c.v); got != c.want {
			t.Errorf("%v of %v: %v, want %v", c.v, c.opts, got, c.want)
		}
	}
}
