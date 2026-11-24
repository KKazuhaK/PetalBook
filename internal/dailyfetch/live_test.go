//go:build live

package dailyfetch

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLiveProviders asks every built-in provider over the real network and
// fails if one does not answer or its paths find no value. It needs the
// network and is not part of CI or "make check": run it with
// "make test-live-daily" when a built-in provider is changed.
func TestLiveProviders(t *testing.T) {
	// As New, with the slow answers of a real service allowed for.
	const slow = 30 * time.Second
	d := &net.Dialer{Timeout: slow, Control: refusePrivate}
	f := newFetcher(&http.Client{
		Timeout:       slow,
		Transport:     &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: slow, MaxResponseHeaderBytes: MaxResponseHeaderBytes},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, false, nil)
	f.SetTimeout(slow)
	probe := func(t *testing.T, a API) ProbeResult {
		t.Helper()
		var res ProbeResult
		var err error
		for try := 0; try < 2; try++ {
			if try > 0 {
				time.Sleep(6 * time.Second)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			res, err = f.Probe(ctx, a, a.Lang, time.Now().Format("2006-01-02"))
			cancel()
			if err == nil && res.OK && len(res.PathErrors) == 0 {
				break
			}
		}
		if err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		if !res.OK || len(res.PathErrors) != 0 || res.Quote == nil || res.Quote.Text == "" {
			t.Fatalf("%s: not ok: status %d, %s, path errors %+v, quote %+v", a.ID, res.Status, res.Message, res.PathErrors, res.Quote)
		}
		t.Logf("%s: ok, status %d, %q by %q, link %q", a.ID, res.Status, res.Quote.Text, res.Quote.Author, res.Quote.Link)
		return res
	}
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			a, err := p.WithOptions(nil) // the defaults of its options
			if err != nil {
				t.Fatal(err)
			}
			probe(t, a)
			// A bilingual one is asked in the other language too.
			if len(a.Texts) > 0 {
				other := probe(t, withLang(a, "en"))
				first := probe(t, withLang(a, "zh-Hans"))
				if other.Quote.Text == first.Quote.Text {
					t.Errorf("both languages gave %q", first.Quote.Text)
				}
			}
		})
	}
	t.Run("jinrishici category", func(t *testing.T) {
		p, _ := ProviderByID("jinrishici")
		a, err := p.WithOptions(map[string][]string{"category": {"shuqing/libie"}})
		if err != nil || !strings.HasSuffix(a.URL, "/shuqing/libie.json") {
			t.Fatalf("%q %v", a.URL, err)
		}
		probe(t, a)
	})
	// The categories of 一言 reach the service: with one chosen, the type that comes back is it.
	t.Run("hitokoto categories", func(t *testing.T) {
		p, _ := ProviderByID("hitokoto")
		a, err := p.WithOptions(map[string][]string{"categories": {"i"}})
		if err != nil {
			t.Fatal(err)
		}
		res := probe(t, a)
		var got struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(res.Raw), &got); err != nil || got.Type != "i" {
			t.Fatalf("asked for c=i, got type %q (%v): %s", got.Type, err, res.Raw)
		}
	})
}

// withLang is the interface as it is asked for a reader of the language: the
// probe asks in the interface's own language, so a copy has that one.
func withLang(a API, lang string) API {
	a.Lang = lang
	return a
}
