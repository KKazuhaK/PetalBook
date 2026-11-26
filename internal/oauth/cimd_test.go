package oauth

import (
	"context"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/dailyfetch"
)

// A document that could not be fetched is not asked for again for a minute:
// every authorize request for it would otherwise be one more fetch.
func TestAFailedMetadataFetchIsRemembered(t *testing.T) {
	t.Parallel()
	c := newCIMD(t, func(string) any { return nil }) // 404
	now := time.Now()
	f := fetcherFor(c, func() time.Time { return now })
	id := c.URL + "/client.json"
	for i := 0; i < 3; i++ {
		if _, err := f.get(context.Background(), id); err == nil {
			t.Fatal("a missing document was taken")
		}
	}
	if c.hits.Load() != 1 {
		t.Fatalf("%d fetches of a failing document", c.hits.Load())
	}
	now = now.Add(cimdFailTTL + time.Second)
	f.get(context.Background(), id)
	if c.hits.Load() != 2 {
		t.Fatalf("%d fetches after the failure was forgotten", c.hits.Load())
	}
}

// Requests for the same client_id at the same moment share one fetch.
func TestConcurrentMetadataFetchesAreOne(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	c := newCIMD(t, good(cb))
	inner := c.Config.Handler
	c.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		inner.ServeHTTP(w, r)
	})
	f := fetcherFor(c, time.Now)
	id := c.URL + "/client.json"
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.get(context.Background(), id)
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.hits.Load() != 1 {
		t.Fatalf("%d fetches for one client_id", c.hits.Load())
	}
}

// The address check is the one of internal/dailyfetch: everything it refuses
// is refused here as well.
func TestMetadataFetchRefusesWhatDailyfetchRefuses(t *testing.T) {
	for _, s := range []string{"192.0.2.1", "198.51.100.7", "203.0.113.9", "2001:db8::1", "127.0.0.1", "fe80::1%eth0"} {
		a := netip.MustParseAddr(s)
		if dailyfetch.Public(a) {
			t.Fatalf("%s is public to dailyfetch", s)
		}
		if !blockedAddr(a) {
			t.Errorf("%s is refused by dailyfetch but not for metadata documents", s)
		}
	}
	tr, ok := safeClient().Transport.(*http.Transport)
	if !ok || tr.MaxResponseHeaderBytes != 16<<10 {
		t.Fatalf("response headers are not limited to 16 KiB: %+v", tr)
	}
}
