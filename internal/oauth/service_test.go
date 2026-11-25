package oauth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
	"petalbook.app/server/internal/tokens"
)

type noUsers struct{}

func (noUsers) CachedUser(context.Context, string) (*store.User, error) {
	return nil, store.ErrNotFound
}

func newService(t *testing.T, st *store.Store, cimd *http.Client) *Service {
	t.Helper()
	s, err := New(context.Background(), Config{Store: st, Secrets: secret.NewBoxWithKey([32]byte{1}), Users: noUsers{}, Machine: tokens.New(tokens.Config{Store: st}),
		Issuer: "https://notes.example.test", CIMDClient: cimd})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func post(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body))
	req.RemoteAddr = "198.51.100.1:1"
	h(rec, req)
	return rec
}

func reg(s *Service, i int) *httptest.ResponseRecorder {
	return post(s.register, `{"client_name":"c`+strconv.Itoa(i)+`","redirect_uris":["https://c.example/cb"]}`)
}

// With the table full, the oldest registration nobody used makes room; when
// every one is in use, the answer is 503 and nothing is dropped.
func TestTheRegistryCapEvictsTheOldestUnused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		old := maxRegistrations
		maxRegistrations = 3
		t.Cleanup(func() { maxRegistrations = old })
		s := newService(t, st, nil)
		s.regLimit.Max = 1000
		// Each registration a millisecond after the last, so "oldest" is not
		// left to the random ids of clients made in the same millisecond.
		clock := time.Now()
		s.now = func() time.Time { clock = clock.Add(time.Millisecond); return clock }
		var ids []string
		for i := 0; i < 3; i++ {
			rec := reg(s, i)
			if rec.Code != 201 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			var v map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &v)
			ids = append(ids, v["client_id"].(string))
		}
		// The oldest one has been used for a grant; the next oldest is dropped.
		if err := store.TouchOAuthClient(context.Background(), st.Q(), ids[0], time.Now()); err != nil {
			t.Fatal(err)
		}
		if rec := reg(s, 3); rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if _, err := store.GetOAuthClient(context.Background(), st.Q(), ids[0]); err != nil {
			t.Fatal("a used client was dropped")
		}
		if _, err := store.GetOAuthClient(context.Background(), st.Q(), ids[1]); err == nil {
			t.Fatal("the oldest unused client stayed")
		}
		// Everyone in use: refused.
		for _, id := range ids[2:] {
			_ = store.TouchOAuthClient(context.Background(), st.Q(), id, time.Now())
		}
		rows, _ := st.Q().Query(context.Background(), `SELECT id FROM oauth_clients`)
		var all []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			all = append(all, id)
		}
		rows.Close()
		for _, id := range all {
			_ = store.TouchOAuthClient(context.Background(), st.Q(), id, time.Now())
		}
		rec := reg(s, 4)
		if rec.Code != 503 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "temporarily_unavailable") {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		if n, _ := store.CountOAuthClients(context.Background(), st.Q()); n != 3 {
			t.Fatalf("%d clients", n)
		}
	})
}

// Two clients registering the same details at the same moment end up with one
// row, and both are told its id.
func TestTheSameRegistrationAtTheSameMomentIsOneClient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := newService(t, st, nil)
		s.regLimit.Max = 1000
		done := make(chan string, 8)
		for i := 0; i < 8; i++ {
			go func() {
				rec := post(s.register, `{"client_name":"same","redirect_uris":["https://c.example/cb"]}`)
				var v map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &v)
				id, _ := v["client_id"].(string)
				done <- id
			}()
		}
		first := <-done
		for i := 1; i < 8; i++ {
			if id := <-done; id != first || id == "" {
				t.Fatalf("%q and %q", id, first)
			}
		}
		if n, _ := store.CountOAuthClients(context.Background(), st.Q()); n != 1 {
			t.Fatalf("%d clients", n)
		}
	})
}

// --- client ID metadata documents ------------------------------------------------

type cimdServer struct {
	*httptest.Server
	hits atomic.Int32
	doc  func(self string) any
	raw  string
}

func newCIMD(t *testing.T, doc func(self string) any) *cimdServer {
	c := &cimdServer{doc: doc}
	c.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if c.raw != "" {
			w.Write([]byte(c.raw))
			return
		}
		d := c.doc("https://" + r.Host + r.URL.Path)
		if d == nil {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(d)
	}))
	t.Cleanup(c.Close)
	return c
}

func good(redirects ...string) func(string) any {
	return func(self string) any {
		return map[string]any{"client_id": self, "client_name": "Doc Client", "redirect_uris": redirects, "token_endpoint_auth_method": "none"}
	}
}

const cb = "http://localhost:7777/callback"

func fetcherFor(c *cimdServer, now func() time.Time) *cimdFetcher {
	return newCIMDFetcher(c.Client(), now, slog.New(slog.DiscardHandler))
}

func TestMetadataDocumentRefusals(t *testing.T) {
	t.Parallel()
	tests := map[string]func(c *cimdServer){
		"client_id mismatch": func(c *cimdServer) {
			c.doc = func(string) any {
				return map[string]any{"client_id": "https://other.example/client.json", "redirect_uris": []string{cb}}
			}
		},
		"no client_id": func(c *cimdServer) {
			c.doc = func(string) any { return map[string]any{"redirect_uris": []string{cb}} }
		},
		"oversize": func(c *cimdServer) {
			c.doc = func(self string) any {
				return map[string]any{"client_id": self, "client_name": strings.Repeat("x", 12<<10), "redirect_uris": []string{cb}}
			}
		},
		"not JSON":  func(c *cimdServer) { c.raw = "<html>nope</html>" },
		"not found": func(c *cimdServer) { c.doc = func(string) any { return nil } },
		"client secret": func(c *cimdServer) {
			c.doc = func(self string) any {
				return map[string]any{"client_id": self, "client_secret": "s", "redirect_uris": []string{cb}}
			}
		},
		"confidential client": func(c *cimdServer) {
			c.doc = func(self string) any {
				return map[string]any{"client_id": self, "token_endpoint_auth_method": "client_secret_basic", "redirect_uris": []string{cb}}
			}
		},
		"no redirect_uris": func(c *cimdServer) { c.doc = func(self string) any { return map[string]any{"client_id": self} } },
		"redirect javascript": func(c *cimdServer) {
			c.doc = func(self string) any {
				return map[string]any{"client_id": self, "redirect_uris": []string{"javascript:alert(1)"}}
			}
		},
		"redirect http, not local": func(c *cimdServer) {
			c.doc = func(self string) any {
				return map[string]any{"client_id": self, "redirect_uris": []string{"http://evil.example/cb"}}
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			c := newCIMD(t, good(cb))
			setup(c)
			if _, err := fetcherFor(c, time.Now).get(context.Background(), c.URL+"/client.json"); err == nil {
				t.Fatal("the document was taken")
			}
		})
	}
}

func TestMetadataDocumentIsCachedAsItSays(t *testing.T) {
	t.Parallel()
	c := newCIMD(t, good(cb))
	now := time.Now()
	f := fetcherFor(c, func() time.Time { return now })
	id := c.URL + "/client.json"
	info, err := f.get(context.Background(), id)
	if err != nil || info.Name != "Doc Client" || info.RedirectURIs[0] != cb {
		t.Fatalf("%v %+v", err, info)
	}
	f.get(context.Background(), id)
	if c.hits.Load() != 1 {
		t.Fatalf("%d fetches", c.hits.Load())
	}
	now = now.Add(2 * time.Hour)
	f.get(context.Background(), id)
	if c.hits.Load() != 2 {
		t.Fatalf("%d fetches after the cache time", c.hits.Load())
	}
}

func TestMetadataDocumentsRefusePrivateAddresses(t *testing.T) {
	t.Parallel()
	c := newCIMD(t, good(cb))
	f := newCIMDFetcher(nil, time.Now, slog.New(slog.DiscardHandler))
	port := c.Listener.Addr().String()[strings.LastIndex(c.Listener.Addr().String(), ":")+1:]
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "[fe80::1]", "[fd00::1]", "[::ffff:127.0.0.1]"} {
		if _, err := f.get(context.Background(), "https://"+host+":"+port+"/client.json"); err == nil {
			t.Errorf("%s was fetched", host)
		}
	}
	if c.hits.Load() != 0 {
		t.Fatalf("the loopback server was reached %d times", c.hits.Load())
	}
}

func TestMetadataDocumentClientIDShape(t *testing.T) {
	t.Parallel()
	f := newCIMDFetcher(nil, time.Now, slog.New(slog.DiscardHandler))
	for _, id := range []string{"https://example.com", "https://example.com/", "https://example.com/a/../b", "https://user@example.com/c", "https://example.com/c#frag", "http://example.com/c", "https://" + strings.Repeat("a", 600) + ".example/c"} {
		if _, err := f.get(context.Background(), id); err == nil {
			t.Errorf("%q was taken", id)
		}
	}
}
