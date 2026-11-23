package tokens

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateShapeAndHash(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if !WellFormed(s) || !strings.HasPrefix(s, "pbk_") || len(s) != 47 {
			t.Fatalf("%q", s)
		}
		if seen[s] {
			t.Fatal("a secret came twice")
		}
		seen[s] = true
		if p := ShownPrefix(s); len(p) != 12 || !strings.HasPrefix(s, p) {
			t.Fatalf("%q", p)
		}
		if h := HashSecret(s); len(h) != 64 || h != HashSecret(s) || strings.Contains(h, s) {
			t.Fatalf("%q", h)
		}
	}
}

func TestWellFormed(t *testing.T) {
	t.Parallel()
	good := "pbk_" + strings.Repeat("a", 43)
	for _, c := range []struct {
		in string
		ok bool
	}{
		{good, true},
		{"", false},
		{"pbk_", false},
		{good + "a", false},
		{good[:46], false},
		{"rbk_" + strings.Repeat("a", 43), false},
		{"pbk_" + strings.Repeat("a", 42) + "-", false},
		{"pbk_" + strings.Repeat("a", 42) + " ", false},
		{"pbk_" + strings.Repeat("é", 21) + "a", false},
		{"Pbk_" + strings.Repeat("a", 43), false},
	} {
		if got := WellFormed(c.in); got != c.ok {
			t.Errorf("%q: %v", c.in, got)
		}
	}
}

func TestLimiter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	l := &Limiter{Now: func() time.Time { return now }}
	for i := 0; i < 10; i++ {
		if blocked, _ := l.Blocked("1.2.3.4"); blocked {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("1.2.3.4")
	}
	blocked, wait := l.Blocked("1.2.3.4")
	if !blocked || wait < 1 || wait > 61 {
		t.Fatalf("%v %d", blocked, wait)
	}
	if blocked, _ := l.Blocked("5.6.7.8"); blocked {
		t.Fatal("another address is blocked")
	}
	now = now.Add(30 * time.Second)
	if blocked, w := l.Blocked("1.2.3.4"); !blocked || w > 31 {
		t.Fatalf("%v %d", blocked, w)
	}
	now = now.Add(31 * time.Second)
	if blocked, _ := l.Blocked("1.2.3.4"); blocked {
		t.Fatal("still blocked after the window")
	}
	// A new window starts counting again.
	l.Fail("1.2.3.4")
	if blocked, _ := l.Blocked("1.2.3.4"); blocked {
		t.Fatal("one failure blocks")
	}
}

func TestLimiterIsBounded(t *testing.T) {
	t.Parallel()
	l := &Limiter{}
	for i := 0; i < maxClients+500; i++ {
		l.Fail(strings.Repeat("x", 1) + string(rune('a'+i%26)) + time.Duration(i).String())
	}
	l.mu.Lock()
	n := len(l.m)
	l.mu.Unlock()
	if n > maxClients {
		t.Fatalf("%d clients kept", n)
	}
}
