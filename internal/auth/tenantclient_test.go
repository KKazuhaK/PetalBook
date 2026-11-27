package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// A tenant's administrators choose its provider's addresses, but the server
// connects: a tenant's provider reaches public addresses only (a loopback
// server here), while the site's own may reach any.
func TestATenantsProviderReachesPublicAddressesOnly(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	s := &Service{}
	if _, err := s.httpClientFor("TENANTB001").Get(srv.URL); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("a tenant's provider reached a loopback address: %v", err)
	}
	resp, err := s.httpClientFor(store.ProviderSite).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
