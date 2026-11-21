package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseTrustedProxies reads --trusted-proxy values: CIDRs or single addresses.
func ParseTrustedProxies(in []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, &net.ParseError{Type: "CIDR address", Text: s}
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// ClientAddr is the address a request is attributed to, for rate limits: the
// TCP peer, unless the peer is one of the trusted proxies, in which case the
// client is the last address in X-Forwarded-For that is not itself a trusted
// proxy (the proxy appends the address it saw; anything to the left of that was
// supplied by the client and is not believed). IPv6 addresses are grouped by
// /64, because one host usually controls a whole /64.
func ClientAddr(r *http.Request, trusted []netip.Prefix) string {
	peer := parseHost(r.RemoteAddr)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	addr := peer
	if inAny(trusted, peer) {
		fwd := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(fwd) - 1; i >= 0; i-- {
			ip, err := netip.ParseAddr(strings.TrimSpace(fwd[i]))
			if err != nil {
				break // junk: stop believing anything further left
			}
			ip = ip.Unmap()
			addr = ip
			if !inAny(trusted, ip) {
				break
			}
		}
	}
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

func parseHost(hostport string) netip.Addr {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host, _, _ = strings.Cut(host, "%")
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func inAny(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
