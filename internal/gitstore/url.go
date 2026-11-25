package gitstore

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// DefaultHosts are the hosts a remote may be on unless the administrator says
// otherwise.
var DefaultHosts = []string{"github.com", "gitlab.com", "codeberg.org", "bitbucket.org"}

// Policy decides which remote addresses are accepted.
type Policy struct {
	// Hosts are the host names a remote may be on; "*" allows any. Matching is
	// case-insensitive and ignores a trailing dot and the port.
	Hosts []string
	// AllowUnsafe also accepts local paths, file:// and http:// addresses
	// without a host check. Tests only: a server never sets it.
	AllowUnsafe bool
}

// URLError is an address that is not accepted (a mistake in its form).
type URLError struct{ Msg string }

func (e *URLError) Error() string { return e.Msg }

// HostError is an address whose host the administrator has not allowed.
type HostError struct{ Host string }

func (e *HostError) Error() string {
	return fmt.Sprintf("the host %s is not allowed on this server (the administrator can allow it with --remote-host)", e.Host)
}

// Remote is a validated address.
type Remote struct {
	// URL is the address in the form handed to git: the host in lower case
	// without a trailing dot, nothing else changed.
	URL    string
	Scheme string // "https", "ssh", and under AllowUnsafe "http" or "file"
	Host   string // lower case, no brackets, no trailing dot; "" for a local path
}

func urlErr(format string, a ...any) error { return &URLError{fmt.Sprintf(format, a...)} }

var (
	scpRe      = regexp.MustCompile(`^(?:([^@\s/:]+)@)?(\[[^\]\s/]*\]|[^:/@\s\[\]]+):(.*)$`)
	helperRe   = regexp.MustCompile(`^[A-Za-z0-9+.-]+::`)
	userRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	labelRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	segmentRe  = regexp.MustCompile(`^[\p{L}\p{N}._~+@=,-]+$`)
	badTextRe  = regexp.MustCompile(`[\x00-\x20\x7f\\]`)
	trailingRe = regexp.MustCompile(`/+$`)
)

// ParseURL checks a remote address: https://host/path, git@host:path or
// ssh://[user@]host[:port]/path, with no password or token in it and a host the
// policy allows. Everything else (git://, http://, file://, ext::, local paths,
// anything that starts with a dash) is refused.
func ParseURL(raw string, p Policy) (Remote, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return Remote{}, urlErr("The remote address is empty.")
	case strings.HasPrefix(raw, "-"):
		return Remote{}, urlErr("The remote address must not start with a dash.")
	case badTextRe.MatchString(raw):
		return Remote{}, urlErr("The remote address must not contain spaces, backslashes or control characters.")
	case strings.Contains(raw, "%"):
		return Remote{}, urlErr("percent-encoding is not accepted in a remote address; write the characters themselves.")
	case helperRe.MatchString(raw):
		return Remote{}, urlErr("Transport helpers (ext::, fd:: and the like) are not accepted.")
	}
	if p.AllowUnsafe && filepath.IsAbs(raw) {
		return Remote{URL: raw, Scheme: "file"}, nil
	}
	var r Remote
	var err error
	if strings.Contains(raw, "://") {
		r, err = parseSchemeURL(raw, p)
	} else if m := scpRe.FindStringSubmatch(raw); m != nil {
		r, err = parseSCP(m)
	} else {
		return Remote{}, urlErr("The remote address must look like https://host/path, git@host:path or ssh://git@host/path.")
	}
	if err != nil {
		return Remote{}, err
	}
	if r.Scheme == "file" || (p.AllowUnsafe && r.Scheme == "http") {
		return r, nil
	}
	if !hostAllowed(r.Host, p.Hosts) {
		return Remote{}, &HostError{Host: r.Host}
	}
	return r, nil
}

func parseSchemeURL(raw string, p Policy) (Remote, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Remote{}, urlErr("The remote address is not a valid URL.")
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == "https" || scheme == "ssh":
	case p.AllowUnsafe && (scheme == "http" || scheme == "file"):
	default:
		return Remote{}, urlErr("Only https and ssh remote addresses are accepted, not %s.", scheme)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "?#") {
		return Remote{}, urlErr("The remote address must not have a query or a fragment.")
	}
	if scheme == "file" {
		if u.Host != "" || u.Path == "" {
			return Remote{}, urlErr("A file:// address needs a path and no host.")
		}
		return Remote{URL: raw, Scheme: "file"}, nil
	}
	if u.User != nil {
		if _, has := u.User.Password(); has || scheme != "ssh" {
			return Remote{}, urlErr("The remote address must not contain a user name, password or token; give the credential separately.")
		}
		if !userRe.MatchString(u.User.Username()) {
			return Remote{}, urlErr("The user name in the remote address is not valid.")
		}
	}
	host, err := canonHost(u.Hostname())
	if err != nil {
		return Remote{}, err
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Remote{}, urlErr("The port in the remote address is not valid.")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return Remote{}, urlErr("The port in the remote address is empty.")
	}
	path, err := checkPath(u.Path, true)
	if err != nil {
		return Remote{}, err
	}
	out := scheme + "://"
	if u.User != nil {
		out += u.User.Username() + "@"
	}
	out += bracket(host)
	if port != "" {
		out += ":" + port
	}
	return Remote{URL: out + path, Scheme: scheme, Host: host}, nil
}

func parseSCP(m []string) (Remote, error) {
	user, hostPart, path := m[1], m[2], m[3]
	if user != "" && !userRe.MatchString(user) {
		return Remote{}, urlErr("The user name in the remote address is not valid.")
	}
	host, err := canonHost(strings.Trim(hostPart, "[]"))
	if err != nil {
		return Remote{}, err
	}
	if path == "" || strings.HasPrefix(path, ":") {
		return Remote{}, urlErr("The remote address has no repository path.")
	}
	path, err = checkPath(path, false)
	if err != nil {
		return Remote{}, err
	}
	out := bracket(host) + ":" + path
	if user != "" {
		out = user + "@" + out
	}
	return Remote{URL: out, Scheme: "ssh", Host: host}, nil
}

// checkPath validates the path of a repository and returns it without a
// trailing slash. leading says the path starts with a slash (URL form).
func checkPath(p string, leading bool) (string, error) {
	p = trailingRe.ReplaceAllString(p, "")
	body := p
	if leading {
		if !strings.HasPrefix(p, "/") {
			return "", urlErr("The remote address has no repository path.")
		}
		body = p[1:]
	} else {
		body = strings.TrimPrefix(p, "/")
	}
	if body == "" {
		return "", urlErr("The remote address has no repository path.")
	}
	for _, seg := range strings.Split(body, "/") {
		if seg == "" || seg == "." || seg == ".." || !segmentRe.MatchString(seg) {
			return "", urlErr("The repository path in the remote address is not valid.")
		}
		for _, r := range seg {
			if unicode.IsControl(r) {
				return "", urlErr("The repository path in the remote address is not valid.")
			}
		}
	}
	return p, nil
}

// canonHost lower-cases a host and drops one trailing dot. A host is a DNS
// name of ASCII labels, an IPv4 address or an IPv6 literal.
func canonHost(h string) (string, error) {
	h = strings.ToLower(h)
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", urlErr("The remote address has no host.")
	}
	if strings.Contains(h, ":") {
		ip := net.ParseIP(h)
		if ip == nil {
			return "", urlErr("The IPv6 address in the remote address is not valid.")
		}
		return ip.String(), nil
	}
	if len(h) > 253 {
		return "", urlErr("The host name in the remote address is too long.")
	}
	for _, l := range strings.Split(h, ".") {
		if !labelRe.MatchString(l) {
			return "", urlErr("The host name in the remote address is not valid.")
		}
	}
	return h, nil
}

func bracket(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func hostAllowed(host string, allowed []string) bool {
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "*" {
			return true
		}
		a = strings.TrimSuffix(strings.Trim(a, "[]"), ".")
		if ip := net.ParseIP(a); ip != nil {
			a = ip.String()
		}
		if a != "" && a == host {
			return true
		}
	}
	return false
}
