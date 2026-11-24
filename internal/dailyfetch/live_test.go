//go:build live

package dailyfetch

import (
	"context"
	"net"
	"net/http"
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
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			var res ProbeResult
			var err error
			for try := 0; try < 2; try++ {
				if try > 0 {
					time.Sleep(6 * time.Second)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				res, err = f.Probe(ctx, p.API, p.Lang, time.Now().Format("2006-01-02"))
				cancel()
				if err == nil && res.OK && len(res.PathErrors) == 0 {
					break
				}
			}
			if err != nil {
				t.Fatalf("%s: %v", p.ID, err)
			}
			if !res.OK || len(res.PathErrors) != 0 || res.Quote == nil || res.Quote.Text == "" {
				t.Fatalf("%s: not ok: status %d, %s, path errors %+v, quote %+v", p.ID, res.Status, res.Message, res.PathErrors, res.Quote)
			}
			t.Logf("%s: ok, status %d, %q by %q, link %q", p.ID, res.Status, res.Quote.Text, res.Quote.Author, res.Quote.Link)
		})
	}
}
