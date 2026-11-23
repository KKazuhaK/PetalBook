package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authoidc "github.com/KazuhaHub/authcore/oidc"
)

var fast = PasswordParams{MemoryKiB: 64, Time: 1, Threads: 1}

func TestHashFormatAndVerify(t *testing.T) {
	h, err := newHasher(fast, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	enc, err := h.hash(ctx, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("parameters are not in the hash: %s", enc)
	}
	other, _ := h.hash(ctx, "correct horse")
	if other == enc {
		t.Fatal("two hashes of one password are equal: no salt")
	}
	if ok, rehash, err := h.verify(ctx, enc, "correct horse"); !ok || rehash || err != nil {
		t.Fatalf("%v %v %v", ok, rehash, err)
	}
	if ok, _, _ := h.verify(ctx, enc, "wrong horse"); ok {
		t.Fatal("wrong password accepted")
	}
	// Raising the parameters later: old hashes still verify and ask for an upgrade.
	h2, _ := newHasher(PasswordParams{MemoryKiB: 128, Time: 2, Threads: 1}, 2)
	if ok, rehash, err := h2.verify(ctx, enc, "correct horse"); !ok || !rehash || err != nil {
		t.Fatalf("%v %v %v", ok, rehash, err)
	}
	// Damaged or hostile rows are a failure, never a crash or a huge allocation.
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "$argon2id$v=19$m=64,t=1,p=1$!!$!!", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5"} {
		if ok, _, err := h.verify(ctx, bad, "x"); ok || err != nil {
			t.Errorf("%q: %v %v", bad, ok, err)
		}
	}
	if _, err := newHasher(PasswordParams{MemoryKiB: 1, Time: 1, Threads: 4}, 1); err == nil {
		t.Fatal("absurd parameters accepted")
	}
}

func TestDefaultParametersAreStrong(t *testing.T) {
	if DefaultPasswordParams.MemoryKiB < 19*1024 || DefaultPasswordParams.Time < 2 {
		t.Fatalf("%+v is below the OWASP minimum for argon2id", DefaultPasswordParams)
	}
}

func TestDefaultParametersRoundTrip(t *testing.T) {
	// Once, at the real cost: the parameters that production uses must work.
	h, err := newHasher(DefaultPasswordParams, 1)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := h.hash(context.Background(), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, _ := h.verify(context.Background(), enc, "correct horse"); !ok || rehash {
		t.Fatal("the default parameters do not verify their own hash")
	}
}

func TestHashesRunAFewAtATime(t *testing.T) {
	h, _ := newHasher(fast, 2)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.hash(context.Background(), "x")
		}()
	}
	wg.Wait()
	if p := h.peak.Load(); p > 2 || p < 1 {
		t.Fatalf("%d hashes at once with a limit of 2", p)
	}
	// A waiting caller gives up when its context ends.
	h.sem <- struct{}{}
	h.sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.hash(ctx, "x"); err == nil {
		t.Fatal("waited past cancellation")
	}
}

func TestPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"1234567": false, "12345678": true, "密码密码密码密码": true, "密码密码密码": false,
		strings.Repeat("x", 1024): true, strings.Repeat("x", 1025): false, "bad\xff\xff\xff\xff\xff\xff\xff\xff": false,
	} {
		if err := CheckPassword(pw); (err == nil) != ok {
			t.Errorf("%q: %v", pw, err)
		}
	}
}

func TestUsernames(t *testing.T) {
	good := map[string]string{"Kazuha": "kazuha", "a1": "a1", "tang.lin_2-x": "tang.lin_2-x", "9lives": "9lives", " tang ": "tang"}
	for in, want := range good {
		if got, err := NormalizeUsername(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a", "-ab", ".ab", "_ab", "a b", "a/b", "a@b", strings.Repeat("a", 33), "名前名前", "K"} { // the last is the Kelvin sign
		if _, err := NormalizeUsername(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSafeReturn(t *testing.T) {
	keep := []string{"/", "/s/01JD3K8Q7Z", "/s/01JD3K8Q7Z/p/01JD3K8Q7Z?x=1#top", "/settings"}
	for _, s := range keep {
		if got := SafeReturn(s); got != s {
			t.Errorf("%q became %q", s, got)
		}
	}
	for _, s := range []string{
		"", "evil.example", "//evil.example", "///evil.example", "https://evil.example", "http://evil.example/x", "javascript:alert(1)",
		"/\\evil.example", "\\\\evil.example", "/\tevil", "/\n/evil.example", "/\r/evil.example", "/\x00", "/a\\b", "/%5c", // the last stays: it is not a backslash yet
		"/login", "/login?x=1", "/setup", "/invite", "/api/session", "data:text/html,x", " /x", "/ x", strings.Repeat("/a", 2000),
	} {
		got := SafeReturn(s)
		if s == "/%5c" {
			if got != s {
				t.Errorf("%q changed to %q", s, got)
			}
			continue
		}
		if got != "/" {
			t.Errorf("%q became %q, want /", s, got)
		}
	}
}

func TestClientAddr(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8", "192.168.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustedProxies([]string{"banana"}); err == nil {
		t.Fatal("bad CIDR accepted")
	}
	req := func(remote string, xff ...string) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, x := range xff {
			r.Header.Add("X-Forwarded-For", x)
		}
		return ClientAddr(r, trusted)
	}
	for _, c := range []struct{ got, want string }{
		// An untrusted peer's header is ignored, however it is written.
		{req("203.0.113.9:1", "1.2.3.4"), "203.0.113.9"},
		{req("203.0.113.9:1", "1.2.3.4, 5.6.7.8"), "203.0.113.9"},
		{req("[::ffff:203.0.113.9]:1", "1.2.3.4"), "203.0.113.9"},
		// A trusted proxy: the address it saw, the last one in the list.
		{req("10.1.2.3:1", "198.51.100.5"), "198.51.100.5"},
		{req("10.1.2.3:1", "6.6.6.6, 198.51.100.5"), "198.51.100.5"},       // what the client wrote is not believed
		{req("10.1.2.3:1", "198.51.100.5, 10.9.9.9"), "198.51.100.5"},      // a chain of our proxies
		{req("192.168.1.1:1", "198.51.100.5", "10.0.0.7"), "198.51.100.5"}, // several header lines
		{req("10.1.2.3:1"), "10.1.2.3"},                                    // no header
		{req("10.1.2.3:1", "garbage"), "10.1.2.3"},
		{req("10.1.2.3:1", "198.51.100.5, garbage"), "10.1.2.3"},
		// IPv6 clients are grouped by /64.
		{req("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:1"), "2001:db8:1:2::/64"},
		{req("[2001:db8:1:2::1]:1"), "2001:db8:1:2::/64"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	// Without trusted proxies the header is never believed.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.1.2.3:1"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := ClientAddr(r, nil); got != "10.1.2.3" {
		t.Fatal(got)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestBucketLimiter(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	b := newBucketLimiter(100, 60, 3, clk.now) // one a second, burst of 3
	for range 3 {
		if _, ok := b.take("a"); !ok {
			t.Fatal("burst refused")
		}
	}
	d, ok := b.take("a")
	if ok || d <= 0 || d > time.Second {
		t.Fatalf("%v %v", d, ok)
	}
	if _, ok := b.take("b"); !ok {
		t.Fatal("another key shares the bucket")
	}
	clk.t = clk.t.Add(1100 * time.Millisecond)
	if _, ok := b.take("a"); !ok {
		t.Fatal("no token after waiting")
	}
}

func TestPendingStoreIsOneTimeBoundAndExpires(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	p := newPendingStore(clk.now)
	mk := func(v string) pendingEntry {
		return pendingEntry{state: authoidc.State{Value: v}, stateValue: v, intent: "login", ret: "/"}
	}
	id := p.put(mk("state-1"))
	// A wrong state does not burn the attempt.
	if _, res := p.take(id, "forged"); res != takeBinding {
		t.Fatalf("%v", res)
	}
	if e, res := p.take(id, "state-1"); res != takeOK || e.intent != "login" {
		t.Fatalf("%v", res)
	}
	if _, res := p.take(id, "state-1"); res != takeUnknown {
		t.Fatal("a state was taken twice")
	}
	id = p.put(mk("state-2"))
	clk.t = clk.t.Add(pendingTTL - time.Second)
	if _, res := p.take(id, "state-2"); res != takeOK {
		t.Fatalf("%v", res)
	}
	id = p.put(mk("state-3"))
	clk.t = clk.t.Add(pendingTTL)
	if _, res := p.take(id, "state-3"); res != takeUnknown {
		t.Fatal("an expired state was accepted")
	}
	// Bounded: flooding evicts the oldest, never grows without limit.
	for i := range pendingCapacity + 500 {
		p.put(mk("s" + itoa(i)))
	}
	if n := p.len(); n > pendingCapacity {
		t.Fatalf("%d pending", n)
	}
	clk.t = clk.t.Add(time.Hour)
	p.put(mk("fresh"))
	if n := p.len(); n != 1 {
		t.Fatalf("expired entries were kept: %d", n)
	}
}

func TestSuffixedUsernamesStayValid(t *testing.T) {
	long := strings.Repeat("a", 32)
	got := suffixed(long, 1)
	if len(got) > 32 || !strings.HasSuffix(got, "-2") {
		t.Fatalf("%q", got)
	}
	if _, err := NormalizeUsername(got); err != nil {
		t.Fatal(err)
	}
}

func TestUsernameFromClaims(t *testing.T) {
	for _, c := range []struct {
		in   claimed
		want string
	}{
		{claimed{preferred: "Kazuha.Z"}, "kazuha.z"},
		{claimed{email: "Tang.Lin@example.test"}, "tang.lin"},
		{claimed{preferred: "a b/c"}, "a-b-c"},
		{claimed{preferred: "名前名前"}, "user"},
		{claimed{}, "user"},
		{claimed{preferred: "_.-x"}, "user"},
		{claimed{preferred: strings.Repeat("z", 50)}, strings.Repeat("z", 32)},
	} {
		got := usernameFrom(c.in)
		if got != c.want {
			t.Errorf("%+v: %q, want %q", c.in, got, c.want)
		}
		if _, err := NormalizeUsername(got); err != nil {
			t.Errorf("%q is not a valid username: %v", got, err)
		}
	}
}

func TestUserKeyIsTheUsernameSignInReads(t *testing.T) {
	for _, in := range []string{"kazuha", " kazuha", "KAZUHA\n", "\tKaZuHa "} {
		if got := userKey(in); got != "u:kazuha" {
			t.Errorf("%q -> %q", in, got)
		}
	}
}
