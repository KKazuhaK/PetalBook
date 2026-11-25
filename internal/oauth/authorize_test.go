package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

const testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

func authorizeQuery(clientID, redirect string, mod func(url.Values)) string {
	v := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "code_challenge": {testChallenge},
		"code_challenge_method": {"S256"}, "resource": {"https://notes.example.test/mcp"}, "state": {"st"}}
	if mod != nil {
		mod(v)
	}
	return v.Encode()
}

func begin(s *Service, user, query string) (string, *Problem) {
	return s.Begin(httptest.NewRequest("GET", PathAuthorize+"?"+query, nil), user)
}

func registeredClient(t *testing.T, s *Service) string {
	t.Helper()
	rec := reg(s, 0)
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var v map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v["client_id"].(string)
}

// One person cannot fill the table of pending requests for everyone: past 20
// of their own, they are refused with temporarily_unavailable on the error page,
// and someone else can still start one.
func TestPendingRequestsAreCappedPerPerson(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := newService(t, st, nil)
		cid := registeredClient(t, s)
		q := authorizeQuery(cid, "https://c.example/cb", nil)
		for i := 0; i < maxPendingPerUser; i++ {
			if dest, p := begin(s, "U1", q); p != nil || !strings.HasPrefix(dest, PathConsent) {
				t.Fatalf("%d: %q %+v", i, dest, p)
			}
		}
		dest, p := begin(s, "U1", q)
		if p == nil || p.Status != http.StatusTooManyRequests || p.Code != "temporarily_unavailable" || dest != "" {
			t.Fatalf("over the cap: %q %+v", dest, p)
		}
		if dest, p := begin(s, "U2", q); p != nil || !strings.HasPrefix(dest, PathConsent) {
			t.Fatalf("another person was refused: %q %+v", dest, p)
		}
		// A request that is decided makes room again.
		if _, err := s.Decide("U2", Decision{Req: "x"}); err != ErrNotFound {
			t.Fatal(err)
		}
		var req string
		for id, pa := range s.pending {
			if pa.userID == "U1" {
				req = id
				break
			}
		}
		if _, err := s.Decide("U1", Decision{Req: req}); err != nil {
			t.Fatal(err)
		}
		if dest, p := begin(s, "U1", q); p != nil || !strings.HasPrefix(dest, PathConsent) {
			t.Fatalf("after a decision: %q %+v", dest, p)
		}
	})
}

// RFC 9700 section 4.11.2: before the person has consented, a bad request of a
// self-registered client or of a client metadata document is shown on the
// server's page and never redirected, so the server is no open redirector.
func TestABadRequestIsNeverRedirectedBeforeConsent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		c := newCIMD(t, good("https://c.example/cb"))
		s := newService(t, st, c.Client())
		clients := map[string]string{"registered": registeredClient(t, s), "metadata document": c.URL + "/client.json"}
		bad := map[string]struct {
			mod  func(url.Values)
			code string
		}{
			"token response": {func(v url.Values) { v.Set("response_type", "token") }, "unsupported_response_type"},
			"plain PKCE":     {func(v url.Values) { v.Set("code_challenge_method", "plain") }, "invalid_request"},
			"no PKCE":        {func(v url.Values) { v.Del("code_challenge") }, "invalid_request"},
			"bad challenge":  {func(v url.Values) { v.Set("code_challenge", "abc") }, "invalid_request"},
			"no resource":    {func(v url.Values) { v.Del("resource") }, "invalid_target"},
			"other resource": {func(v url.Values) { v.Set("resource", "https://evil.example/") }, "invalid_target"},
			"unknown scope":  {func(v url.Values) { v.Set("scope", "admin") }, "invalid_scope"},
		}
		for kind, cid := range clients {
			for name, b := range bad {
				dest, p := begin(s, "U1", authorizeQuery(cid, "https://c.example/cb", b.mod))
				if dest != "" || p == nil || p.Status != http.StatusBadRequest || p.Code != b.code {
					t.Errorf("%s, %s: %q %+v", kind, name, dest, p)
				}
			}
			if dest, p := begin(s, "U1", authorizeQuery(cid, "https://c.example/cb", nil)); p != nil || !strings.HasPrefix(dest, PathConsent) {
				t.Errorf("%s, good request: %q %+v", kind, dest, p)
			}
		}
		// The page names the error and links nowhere.
		_, p := begin(s, "U1", authorizeQuery(clients["registered"], "https://c.example/cb", bad["unknown scope"].mod))
		rec := httptest.NewRecorder()
		s.WriteProblem(rec, httptest.NewRequest("GET", PathAuthorize, nil), p)
		if rec.Code != 400 || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "invalid_scope") || strings.Contains(rec.Body.String(), "c.example") {
			t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
		}
	})
}
