package gitstore

import (
	"errors"
	"testing"
)

func TestParseURL(t *testing.T) {
	t.Parallel()
	p := Policy{Hosts: DefaultHosts}
	good := map[string]Remote{
		"https://github.com/kazuha/notes.git":          {"https://github.com/kazuha/notes.git", "https", "github.com"},
		"https://github.com/kazuha/notes":              {"https://github.com/kazuha/notes", "https", "github.com"},
		"  https://github.com/kazuha/notes.git/  ":     {"https://github.com/kazuha/notes.git", "https", "github.com"},
		"https://GitHub.COM/Kazuha/Notes.git":          {"https://github.com/Kazuha/Notes.git", "https", "github.com"},
		"https://github.com./kazuha/notes.git":         {"https://github.com/kazuha/notes.git", "https", "github.com"},
		"https://gitlab.com:443/group/sub/notes.git":   {"https://gitlab.com:443/group/sub/notes.git", "https", "gitlab.com"},
		"git@github.com:kazuha/notes.git":              {"git@github.com:kazuha/notes.git", "ssh", "github.com"},
		"git@GitHub.com.:kazuha/notes.git":             {"git@github.com:kazuha/notes.git", "ssh", "github.com"},
		"github.com:kazuha/notes.git":                  {"github.com:kazuha/notes.git", "ssh", "github.com"},
		"ssh://git@github.com/kazuha/notes.git":        {"ssh://git@github.com/kazuha/notes.git", "ssh", "github.com"},
		"ssh://git@codeberg.org:2222/kazuha/notes.git": {"ssh://git@codeberg.org:2222/kazuha/notes.git", "ssh", "codeberg.org"},
		"ssh://bitbucket.org/kazuha/notes.git":         {"ssh://bitbucket.org/kazuha/notes.git", "ssh", "bitbucket.org"},
		"https://github.com/日本/ノート.git":                {"https://github.com/日本/ノート.git", "https", "github.com"},
	}
	for in, want := range good {
		got, err := ParseURL(in, p)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
		}
	}

	invalid := []string{
		"", "   ", "-oProxyCommand=evil", "--upload-pack=x", "-", "git@-oProxyCommand=x:path",
		"http://github.com/a/b.git", "git://github.com/a/b.git", "file:///etc", "/srv/repo.git", "./repo", "../repo", "C:\\repo",
		"ext::sh -c touch% /tmp/x", "ext::sh", "fd::3", "ftp://github.com/a/b",
		"https://user:pass@github.com/a/b.git", "https://token@github.com/a/b.git", "https://:@github.com/a/b.git",
		"ssh://git:pw@github.com/a/b.git", "ssh://-oProxyCommand=x@github.com/a/b.git", "ssh://git@-oProxyCommand=x/a/b.git",
		"https://github.com/a/b.git?x=1", "https://github.com/a/b.git#frag", "https://github.com/a/b.git?",
		"https://github.com", "https://github.com/", "ssh://git@github.com", "git@github.com:", "git@github.com:/",
		"https://github.com/a/../b.git", "https://github.com/a/./b.git", "https://github.com//a.git", "git@github.com:a/../b",
		"https://github.com/a b/c.git", "https://github.com/a\\b", "https://github.com/a\nb", "https://github.com/a\x00b",
		"https://github.com%2f@evil.com/a", "https://github.com/%2e%2e/x", "https://git%68ub.com/a/b", "git@github.com:a%20b/c",
		"https://github.com:0/a/b", "https://github.com:99999/a/b", "https://github.com:abc/a/b", "https://github.com:/a/b",
		"https://[::1/a/b", "https://[zz::1]/a/b", "https://bad_host.com/a/b", "https://-x.com/a/b", "https://a..b/c/d",
		"https://exämple.com/a/b", "ssh://git@github.com:kazuha/notes.git", "just-a-word",
		"git@github.com:ext::sh", "git@github.com::x",
	}
	for _, in := range invalid {
		_, err := ParseURL(in, Policy{Hosts: []string{"*"}})
		var ue *URLError
		if !errors.As(err, &ue) {
			t.Errorf("%q accepted or wrong error (with * hosts): %v", in, err)
		}
	}
}

func TestParseURLHostPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url   string
		hosts []string
		ok    bool
	}{
		{"https://example.com/a/b", nil, false}, // nothing allowed unless listed
		{"https://example.com/a/b", []string{"example.com"}, true},
		{"https://EXAMPLE.com/a/b", []string{"example.COM"}, true},
		{"https://example.com./a/b", []string{"example.com."}, true},
		{"https://example.com/a/b", []string{"*"}, true},
		{"https://example.com:8443/a/b", []string{"example.com"}, true},
		{"https://sub.example.com/a/b", []string{"example.com"}, false},
		{"https://example.com.evil.net/a/b", []string{"example.com"}, false},
		{"https://notexample.com/a/b", []string{"example.com"}, false},
		{"git@example.com:a/b", []string{"github.com"}, false},
		{"https://[2001:db8::1]/a/b", []string{"2001:db8::1"}, true},
		{"https://[2001:DB8::1]/a/b", []string{"[2001:db8::1]"}, true},
		{"https://[2001:db8::1]/a/b", []string{"github.com"}, false},
		{"git@[2001:db8::1]:a/b", []string{"*"}, true},
		{"ssh://git@[2001:db8::1]:2222/a/b", []string{"2001:db8::1"}, true},
		{"https://127.0.0.1/a/b", []string{"github.com"}, false},
		{"https://127.0.0.1/a/b", []string{"127.0.0.1"}, true},
	}
	for _, c := range cases {
		_, err := ParseURL(c.url, Policy{Hosts: c.hosts})
		var he *HostError
		switch {
		case c.ok && err != nil:
			t.Errorf("%s with %v refused: %v", c.url, c.hosts, err)
		case !c.ok && !errors.As(err, &he):
			t.Errorf("%s with %v: %v, want a host error", c.url, c.hosts, err)
		}
	}
	// The host error names the host in its canonical form.
	_, err := ParseURL("https://Evil.Example.:8080/x/y", Policy{Hosts: DefaultHosts})
	var he *HostError
	if !errors.As(err, &he) || he.Host != "evil.example" {
		t.Fatalf("%v", err)
	}
	// IPv6 keeps its brackets in the address handed to git.
	r, err := ParseURL("https://[2001:DB8::1]:8443/a/b", Policy{Hosts: []string{"*"}})
	if err != nil || r.URL != "https://[2001:db8::1]:8443/a/b" || r.Host != "2001:db8::1" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestParseURLUnsafeOnlyInTests(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"/tmp/repo.git", "file:///tmp/repo.git", "http://127.0.0.1:8080/r.git"} {
		if _, err := ParseURL(in, Policy{Hosts: []string{"*"}}); err == nil {
			t.Errorf("%q accepted without the test option", in)
		}
		if r, err := ParseURL(in, Policy{AllowUnsafe: true}); err != nil || r.URL != in {
			t.Errorf("%q with the test option: %+v %v", in, r, err)
		}
	}
	// Even then a credential in the address is refused.
	if _, err := ParseURL("http://u:p@127.0.0.1/r.git", Policy{AllowUnsafe: true}); err == nil {
		t.Error("credentials in an http address accepted")
	}
	if _, err := ParseURL("ext::sh -c x", Policy{AllowUnsafe: true}); err == nil {
		t.Error("a transport helper accepted")
	}
}

func TestEveryAcceptedAddressIsSafeForTheRunner(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"https://github.com/a/b.git", "git@github.com:a/b.git", "ssh://git@github.com:22/a/b"} {
		r, err := ParseURL(in, Policy{Hosts: DefaultHosts})
		if err != nil {
			t.Fatal(err)
		}
		if err := refuseDestructive([]string{"remote", "add", "origin", r.URL}); err != nil {
			t.Errorf("%s: %v", in, err)
		}
	}
}
