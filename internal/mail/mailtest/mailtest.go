// Package mailtest is an SMTP server in this process for tests: it listens on
// a loopback port, keeps what it is given and never relays anything. No test
// of this repository may send mail anywhere else.
package mailtest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is one mail the server accepted.
type Message struct {
	From, To string
	Subject  string
	Text     string
	HTML     string
	Raw      string
	Auth     string // "user:password" of the PLAIN login, "" when none
}

// Options of a server.
type Options struct {
	// StartTLS offers STARTTLS (with a self-signed certificate for 127.0.0.1);
	// ImplicitTLS serves TLS from the first byte.
	StartTLS, ImplicitTLS bool
	// RequireAuth refuses mail without a PLAIN login of this user and password.
	User, Password string
	// FailData answers the end of the message with this code and text instead of accepting it.
	FailData string
}

// Server is a fake SMTP server.
type Server struct {
	t    *testing.T
	ln   net.Listener
	opt  Options
	cert tls.Certificate
	// Roots trusts the server's certificate.
	Roots *x509.CertPool

	mu       sync.Mutex
	msgs     []Message
	conns    int
	failData string
	wg       sync.WaitGroup
}

// New starts a server and stops it when the test ends.
func New(t *testing.T, opt Options) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{t: t, ln: ln, opt: opt, failData: opt.FailData}
	if opt.StartTLS || opt.ImplicitTLS {
		s.cert, s.Roots = selfSigned(t)
	}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() { ln.Close(); s.wg.Wait() })
	return s
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mailtest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// Host and Port are where to point the settings.
func (s *Server) Host() string { return "127.0.0.1" }
func (s *Server) Port() int    { return s.ln.Addr().(*net.TCPAddr).Port }

// SetFailData makes the end of every message answer with this reply ("" to accept again).
func (s *Server) SetFailData(reply string) { s.mu.Lock(); s.failData = reply; s.mu.Unlock() }

// Connections is how many clients connected.
func (s *Server) Connections() int { s.mu.Lock(); defer s.mu.Unlock(); return s.conns }

// Messages is what was accepted.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Wait waits for n messages in all and returns them; it fails the test on timeout.
func (s *Server) Wait(n int) []Message {
	s.t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if m := s.Messages(); len(m) >= n {
			return m
		}
	}
	s.t.Fatalf("waited for %d messages, got %d", n, len(s.Messages()))
	return nil
}

// Quiet fails the test if a message arrives within d, and returns the count.
func (s *Server) Quiet(d time.Duration) int {
	before := len(s.Messages())
	time.Sleep(d)
	if n := len(s.Messages()); n != before {
		s.t.Fatalf("a message was sent that should not have been (%d -> %d)", before, n)
	}
	return before
}

var tokenRE = regexp.MustCompile(`#([A-Za-z0-9_-]{43})`)

// Tokens are the tokens in the links of the text, in order.
func (m Message) Tokens() []string {
	var out []string
	for _, x := range tokenRE.FindAllStringSubmatch(m.Text, -1) {
		out = append(out, x[1])
	}
	return out
}

// Token is the first token of the text; it fails the test when there is none.
func (m Message) Token(t *testing.T) string {
	t.Helper()
	ts := m.Tokens()
	if len(ts) == 0 {
		t.Fatalf("no link in the message:\n%s", m.Text)
	}
	return ts[0]
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handle(c) }()
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	if s.opt.ImplicitTLS {
		c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	}
	r := bufio.NewReader(c)
	w := func(format string, a ...any) { fmt.Fprintf(c, format+"\r\n", a...) }
	w("220 mailtest ready")
	var from, to, user string
	authed, tlsOn := false, s.opt.ImplicitTLS
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			w("250-mailtest")
			if s.opt.StartTLS && !tlsOn {
				w("250-STARTTLS")
			}
			w("250 AUTH PLAIN")
		case verb == "STARTTLS" && s.opt.StartTLS && !tlsOn:
			w("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, tlsOn = tc, bufio.NewReader(tc), true
			w = func(format string, a ...any) { fmt.Fprintf(tc, format+"\r\n", a...) }
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && (s.opt.User == "" || parts[1] == s.opt.User && parts[2] == s.opt.Password) {
				authed, user = true, parts[1]+":"+parts[2]
				w("235 ok")
			} else {
				w("535 authentication failed")
			}
		case strings.HasPrefix(verb, "MAIL FROM:"):
			if s.opt.User != "" && !authed {
				w("530 authentication required")
				continue
			}
			from = strings.Trim(line[len("MAIL FROM:"):], " <>")
			w("250 ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			to = strings.Trim(line[len("RCPT TO:"):], " <>")
			w("250 ok")
		case verb == "DATA":
			w("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			fail := s.failData
			s.mu.Unlock()
			if fail != "" {
				w("%s", fail)
				continue
			}
			m := parse(b.String())
			m.From, m.To, m.Auth = from, to, user
			s.mu.Lock()
			s.msgs = append(s.msgs, m)
			s.mu.Unlock()
			w("250 queued")
		case verb == "QUIT":
			w("221 bye")
			return
		case verb == "RSET" || verb == "NOOP":
			w("250 ok")
		default:
			w("500 unknown command")
		}
	}
}

func parse(raw string) Message {
	m := Message{Raw: raw}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return m
	}
	dec := new(mime.WordDecoder)
	m.Subject, _ = dec.DecodeHeader(msg.Header.Get("Subject"))
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		b, _ := io.ReadAll(msg.Body)
		m.Text = string(b)
		return m
	}
	mr := mimeParts(msg.Body, params["boundary"])
	for _, p := range mr {
		if strings.HasPrefix(p.ctype, "text/html") {
			m.HTML = p.body
		} else {
			m.Text = p.body
		}
	}
	return m
}

type part struct{ ctype, body string }

func mimeParts(body io.Reader, boundary string) []part {
	b, _ := io.ReadAll(body)
	var out []part
	for _, seg := range strings.Split(string(b), "--"+boundary) {
		seg = strings.TrimPrefix(seg, "\r\n")
		if seg == "" || strings.HasPrefix(seg, "--") {
			continue
		}
		hdr, content, ok := strings.Cut(seg, "\r\n\r\n")
		if !ok {
			continue
		}
		p := part{}
		for _, l := range strings.Split(hdr, "\r\n") {
			if strings.HasPrefix(strings.ToLower(l), "content-type:") {
				p.ctype = strings.TrimSpace(l[len("content-type:"):])
			}
		}
		dec, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(content)))
		p.body = string(dec)
		out = append(out, p)
	}
	return out
}

var _ = strconv.Itoa
