// Package mail is outgoing mail for verification, password reset and
// registration (docs/api.md, "Mail"). It speaks SMTP with the standard library
// only, always verifies the server's certificate, and keeps what waits to be
// sent in a bounded queue in memory. It sends nothing until Configure has been
// given settings that are enabled.
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Security modes.
const (
	StartTLS = "starttls"
	TLS      = "tls"
	None     = "none"
)

// Limits of the queue and of one delivery.
const (
	MaxQueue    = 1000
	MaxAttempts = 3
	MaxAge      = 10 * time.Minute
	// MaxPerSender is how many messages one tenant's sender, or one tenant's
	// use of the site's, may have waiting at once.
	MaxPerSender = 200
)

// Errors a caller maps to API codes.
var (
	ErrNotEnabled = errors.New("mail is not enabled")
	ErrQueueFull  = errors.New("the mail queue is full")
	// ErrSenderFull is a tenant that already has MaxPerSender messages waiting.
	ErrSenderFull = errors.New("the tenant has too many messages waiting")
	ErrInsecure   = errors.New("security none is only allowed for a loopback host")
)

// Settings are the sender's configuration. Password is plain text, in memory only.
type Settings struct {
	Enabled  bool
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
	FromName string
	ReplyTo  string
	// Control, when set, is called on every connection before it is made, with
	// the address about to be dialled: a tenant's sender uses it to refuse
	// addresses that are not public. Not shown to any API.
	Control func(network, address string, c syscall.RawConn) error
}

// Message is one mail: plain text and a simple HTML part. FromName, when set,
// replaces the sender's display name for this message (a tenant's name on the
// site's mailer).
type Message struct {
	To, Subject, Text, HTML string
	FromName                string
}

// Status is the result of the last delivery or test.
type Status struct {
	OK      bool
	Checked time.Time // zero: never
	Message string
}

// Stats are the queue's numbers.
type Stats struct {
	Waiting        int
	SentLastHour   int
	FailedLastHour int
}

// Options are for tests: nothing here reaches the settings people can make.
type Options struct {
	Now func() time.Time
	// Backoff is the wait after the first and the second failure.
	Backoff [2]time.Duration
	// RootCAs replaces the system roots (a test server's certificate). The
	// certificate is verified against them all the same.
	RootCAs *x509.CertPool
	// Hello is the name given in EHLO ("localhost" when empty).
	Hello  string
	Logger *slog.Logger
}

type queued struct {
	// route is whose settings send it: "" for the site's, else a tenant's id;
	// it is kept with the message so that a retry goes the way the first
	// attempt did. owner is the tenant whose allowance it is ("" for none).
	route, owner string
	msg          Message
	attempts     int
	first        time.Time
	next         time.Time
}

// Service is safe for concurrent use.
type Service struct {
	opt  Options
	log  *slog.Logger
	cfg  atomic.Pointer[Settings]
	wake chan struct{}
	// roots, when set, replaces Options.RootCAs (tests that start their server later).
	roots atomic.Pointer[x509.CertPool]

	tmu     sync.RWMutex
	tenants map[string]*Settings // each tenant's own sender

	mu      sync.Mutex
	queue   []*queued
	held    map[string]int // waiting messages by "r:"+route and "o:"+owner
	tstatus map[string]Status
	status  Status
	sent    []time.Time // times of the last hour's deliveries, oldest first
	failed  []time.Time
}

// New makes a service with mail off.
func New(opt Options) *Service {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Backoff == ([2]time.Duration{}) {
		opt.Backoff = [2]time.Duration{time.Minute, 5 * time.Minute}
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{opt: opt, log: opt.Logger, wake: make(chan struct{}, 1), tenants: map[string]*Settings{}, held: map[string]int{}, tstatus: map[string]Status{}}
}

// TrustRoots replaces the certificate roots (tests).
func (s *Service) TrustRoots(p *x509.CertPool) { s.roots.Store(p) }

func (s *Service) rootCAs() *x509.CertPool {
	if p := s.roots.Load(); p != nil {
		return p
	}
	return s.opt.RootCAs
}

// SetTenant sets the sender of a tenant in force; nil, or settings that are
// not enabled, remove it and drop what waits for it. Safe to call at any time.
func (s *Service) SetTenant(id string, c *Settings) {
	s.tmu.Lock()
	if c == nil || !c.Enabled {
		delete(s.tenants, id)
	} else {
		cc := *c
		s.tenants[id] = &cc
	}
	s.tmu.Unlock()
	if c == nil || !c.Enabled {
		s.drop(func(q *queued) bool { return q.route == id })
		s.mu.Lock()
		delete(s.tstatus, id)
		s.mu.Unlock()
	}
}

// TenantEnabled says whether the tenant has a sender of its own in force.
func (s *Service) TenantEnabled(id string) bool {
	s.tmu.RLock()
	defer s.tmu.RUnlock()
	_, ok := s.tenants[id]
	return ok
}

// TenantStatus is the result of the tenant's last delivery or test.
func (s *Service) TenantStatus(id string) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tstatus[id]
}

// cfgOf is the settings of a route: nil when there are none in force.
func (s *Service) cfgOf(route string) *Settings {
	if route == "" {
		if c := s.cfg.Load(); c != nil && c.Enabled {
			return c
		}
		return nil
	}
	s.tmu.RLock()
	defer s.tmu.RUnlock()
	return s.tenants[route]
}

// drop removes what waits and matches, and counts again.
func (s *Service) drop(match func(*queued) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.queue[:0]
	for _, q := range s.queue {
		if !match(q) {
			kept = append(kept, q)
		}
	}
	for i := len(kept); i < len(s.queue); i++ {
		s.queue[i] = nil
	}
	s.queue = kept
	s.held = map[string]int{}
	for _, q := range s.queue {
		s.count(q, 1)
	}
}

// count adds d to the counts of the message's route and owner (s.mu held).
func (s *Service) count(q *queued, d int) {
	if q.route != "" {
		s.held["r:"+q.route] += d
	}
	if q.owner != "" {
		s.held["o:"+q.owner] += d
	}
}

// Configure sets the settings in force. Switching mail off drops what waits.
func (s *Service) Configure(c Settings) {
	s.cfg.Store(&c)
	if !c.Enabled {
		s.drop(func(q *queued) bool { return q.route == "" })
	}
}

// Enabled says whether mail can be sent now.
func (s *Service) Enabled() bool {
	c := s.cfg.Load()
	return c != nil && c.Enabled
}

// Enqueue adds a mail to the queue, to go by the site's settings.
func (s *Service) Enqueue(m Message) error { return s.EnqueueVia("", "", m) }

// EnqueueVia adds a mail that goes by the settings of route ("" for the
// site's, else a tenant's id) and counts against owner's share of the queue
// ("" for none). The queue remembers the route, so a retry uses the same one.
func (s *Service) EnqueueVia(route, owner string, m Message) error {
	if s.cfgOf(route) == nil {
		return ErrNotEnabled
	}
	s.mu.Lock()
	if len(s.queue) >= MaxQueue {
		s.mu.Unlock()
		return ErrQueueFull
	}
	q := &queued{route: route, owner: owner, msg: m}
	if route != "" && s.held["r:"+route] >= MaxPerSender || owner != "" && s.held["o:"+owner] >= MaxPerSender {
		s.mu.Unlock()
		return ErrSenderFull
	}
	now := s.opt.Now()
	q.first, q.next = now, now
	s.queue = append(s.queue, q)
	s.count(q, 1)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Waiting is how many mails are queued.
func (s *Service) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Stats returns the queue's numbers.
func (s *Service) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := s.opt.Now().Add(-time.Hour)
	s.sent, s.failed = trim(s.sent, cut), trim(s.failed, cut)
	return Stats{Waiting: len(s.queue), SentLastHour: len(s.sent), FailedLastHour: len(s.failed)}
}

// LastStatus is the result of the last delivery or test.
func (s *Service) LastStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func trim(ts []time.Time, cut time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cut) {
		i++
	}
	return ts[i:]
}

func (s *Service) note(route string, ok bool, err error, c *Settings) {
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{OK: ok, Checked: now}
	if !ok {
		st.Message = Describe(err, c)
	}
	if route != "" {
		s.tstatus[route] = st // a tenant's own sender: the site's numbers are not its
		return
	}
	s.status = st
	if ok {
		s.sent = append(trim(s.sent, now.Add(-time.Hour)), now)
		return
	}
	s.failed = append(trim(s.failed, now.Add(-time.Hour)), now)
}

// Run delivers the queue until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		q, wait := s.pick()
		if q != nil {
			s.deliver(ctx, q)
			continue
		}
		var timer <-chan time.Time
		var t *time.Timer
		if wait > 0 {
			t = time.NewTimer(wait)
			timer = t.C
		}
		select {
		case <-ctx.Done():
			if t != nil {
				t.Stop()
			}
			return
		case <-s.wake:
		case <-timer:
		}
		if t != nil {
			t.Stop()
		}
	}
}

// pick takes the next mail that is due, dropping those too old; wait is how
// long until the next one is due (0: nothing queued).
func (s *Service) pick() (*queued, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	var wait time.Duration
	for i := 0; i < len(s.queue); {
		q := s.queue[i]
		if now.Sub(q.first) > MaxAge {
			s.log.Warn("dropped a mail that could not be sent in time", "attempts", q.attempts)
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.count(q, -1)
			continue
		}
		if !q.next.After(now) {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.count(q, -1)
			return q, 0
		}
		if d := q.next.Sub(now); wait == 0 || d < wait {
			wait = d
		}
		i++
	}
	return nil, wait
}

func (s *Service) deliver(ctx context.Context, q *queued) {
	// The route the first attempt took: the site's, or the tenant's own. A
	// sender that is gone since drops the message.
	c := s.cfgOf(q.route)
	if c == nil {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := s.send(dctx, *c, q.msg)
	cancel()
	if err == nil {
		s.note(q.route, true, nil, c)
		return
	}
	s.note(q.route, false, err, c)
	q.attempts++
	if q.attempts >= MaxAttempts {
		// The address and the text are not logged: they hold a link.
		s.log.Warn("dropped a mail after the last attempt", "reason", Describe(err, c))
		return
	}
	q.next = s.opt.Now().Add(s.opt.Backoff[q.attempts-1])
	s.mu.Lock()
	if s.cfgOf(q.route) != nil && len(s.queue) < MaxQueue {
		s.queue = append(s.queue, q)
		s.count(q, 1)
	}
	s.mu.Unlock()
}

// Test sends one message now with the given settings (enabled or not),
// recording the result as the status.
func (s *Service) Test(ctx context.Context, c Settings, m Message) error {
	return s.TestFor(ctx, "", c, m)
}

// TestFor is Test for the sender of a tenant ("" for the site's): the result
// is that sender's status.
func (s *Service) TestFor(ctx context.Context, route string, c Settings, m Message) error {
	err := s.send(ctx, c, m)
	s.note(route, err == nil, err, &c)
	return err
}

// IsLoopback says whether a host is a loopback address or "localhost".
func IsLoopback(host string) bool {
	h := strings.Trim(strings.ToLower(host), "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Describe is an error as a person may read it: the server's answer or the
// reason, with control characters gone, shortened, and the password never in it.
func Describe(err error, c *Settings) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if c != nil && c.Password != "" {
		msg = strings.ReplaceAll(msg, c.Password, "***")
	}
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// send makes one delivery over a connection of its own.
func (s *Service) send(ctx context.Context, c Settings, m Message) error {
	if c.Host == "" || c.From == "" {
		return errors.New("mail is not configured")
	}
	if c.Security == None && !IsLoopback(c.Host) {
		return ErrInsecure
	}
	to, err := address(m.To)
	if err != nil {
		return fmt.Errorf("bad recipient: %w", err)
	}
	from, err := address(c.From)
	if err != nil {
		return fmt.Errorf("bad sender: %w", err)
	}
	body, err := s.build(c, to, m)
	if err != nil {
		return err
	}
	port := c.Port
	if port == 0 {
		port = map[string]int{StartTLS: 587, TLS: 465, None: 25}[c.Security]
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	var d net.Dialer
	d.Timeout = 10 * time.Second
	d.Control = c.Control
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if c.Security == None {
		// The name may resolve elsewhere than loopback: the peer decides.
		if ta, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() {
			return ErrInsecure
		}
	}
	tlsCfg := &tls.Config{ServerName: strings.Trim(c.Host, "[]"), MinVersion: tls.VersionTLS12, RootCAs: s.rootCAs()}
	secure := false
	if c.Security == TLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return err
		}
		conn, secure = tc, true
	}
	cl, err := smtp.NewClient(conn, tlsCfg.ServerName)
	if err != nil {
		return err
	}
	defer cl.Close()
	hello := s.opt.Hello
	if hello == "" {
		hello = "localhost"
	}
	if err := cl.Hello(hello); err != nil {
		return err
	}
	if c.Security == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return err
		}
		secure = true
	}
	if c.Username != "" {
		if err := cl.Auth(plainAuth{c.Username, c.Password, secure || c.Security == None}); err != nil {
			return err
		}
	}
	if err := cl.Mail(from); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// plainAuth is PLAIN, sent only inside TLS or to a loopback relay.
type plainAuth struct {
	user, pass string
	ok         bool
}

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	if !a.ok {
		return "", nil, errors.New("refusing to send the password without TLS")
	}
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

// address checks a plain local@domain address (no name, no header injection).
func address(a string) (string, error) {
	if len(a) > 254 || strings.ContainsAny(a, "\r\n") {
		return "", errors.New("not an address")
	}
	p, err := netmail.ParseAddress(a)
	if err != nil || p.Address != a {
		return "", errors.New("not an address")
	}
	return a, nil
}

func header(v string) string {
	v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
	return mime.QEncoding.Encode("utf-8", v)
}

func (s *Service) build(c Settings, to string, m Message) ([]byte, error) {
	var id [12]byte
	rand.Read(id[:])
	domain := "localhost"
	if i := strings.LastIndex(c.From, "@"); i >= 0 {
		domain = c.From[i+1:]
	}
	var b strings.Builder
	from := c.From
	name := c.FromName
	if m.FromName != "" {
		name = m.FromName
	}
	if name != "" {
		from = (&netmail.Address{Name: name, Address: c.From}).String()
	}
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from)
	hdr("To", to)
	if c.ReplyTo != "" {
		if r, err := address(c.ReplyTo); err == nil {
			hdr("Reply-To", r)
		}
	}
	hdr("Subject", header(m.Subject))
	hdr("Date", s.opt.Now().UTC().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Auto-Submitted", "auto-generated")
	boundary := "pb" + hex.EncodeToString(id[:])
	hdr("Content-Type", "multipart/alternative; boundary="+boundary)
	b.WriteString("\r\n")
	part := func(ctype, body string) {
		b.WriteString("--" + boundary + "\r\nContent-Type: " + ctype + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		w := quotedprintable.NewWriter(&b)
		w.Write([]byte(strings.ReplaceAll(body, "\r\n", "\n")))
		w.Close()
		b.WriteString("\r\n")
	}
	part("text/plain", m.Text)
	part("text/html", m.HTML)
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String()), nil
}
