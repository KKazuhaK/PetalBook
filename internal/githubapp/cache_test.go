package githubapp

import (
	"strconv"
	"testing"
	"time"
)

func TestTokenCacheIsCapped(t *testing.T) {
	c, _ := New(Config{})
	a := &App{c: c, tokens: map[string]cachedToken{}, flights: map[string]*flight{}, lookups: map[string]lookup{}}
	now := c.now()
	a.mu.Lock()
	for i := 0; i < MaxTokens+50; i++ {
		a.storeLocked("k"+strconv.Itoa(i), cachedToken{token: "t", expires: now.Add(time.Hour + time.Duration(i)*time.Second)})
	}
	n := len(a.tokens)
	_, oldest := a.tokens["k0"]
	_, newest := a.tokens["k"+strconv.Itoa(MaxTokens+49)]
	a.mu.Unlock()
	if n > MaxTokens || oldest || !newest {
		t.Fatalf("%d entries, oldest kept %v, newest kept %v", n, oldest, newest)
	}
}
