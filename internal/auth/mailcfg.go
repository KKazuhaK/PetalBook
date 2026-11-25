package auth

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"net"
	netmail "net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/mail"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Mail settings (docs/api.md, "Mail"): one sender for the whole server, in the
// site settings, with the password sealed. Nothing is sent until they are
// enabled.

const mailKey = "mail"

type mailStored struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	PasswordEnc string `json:"passwordEnc,omitempty"`
	From        string `json:"from"`
	FromName    string `json:"fromName"`
	ReplyTo     string `json:"replyTo"`
}

func defaultMail() mailStored { return mailStored{Port: 587, Security: mail.StartTLS} }

// MailView is what GET /api/admin/mail shows.
type MailView struct {
	mailStored
	HasPassword bool
	Status      mail.Status
	Queue       mail.Stats
}

// MailPatch is a PUT of the mail settings; nil fields are left alone.
type MailPatch struct {
	Enabled  *bool
	Host     *string
	Port     *int
	Security *string
	Username *string
	Password *string
	From     *string
	FromName *string
	ReplyTo  *string
}

// loadMail reads the stored settings and makes them the ones in force.
func (s *Service) loadMail(ctx context.Context) error {
	m, err := s.readMail(ctx)
	if err != nil {
		return err
	}
	s.applyMail(m)
	return nil
}

func (s *Service) readMail(ctx context.Context) (mailStored, error) {
	m := defaultMail()
	raw, _, err := store.GetSetting(ctx, s.st.Q(), mailKey)
	if errors.Is(err, store.ErrNotFound) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return defaultMail(), nil
	}
	return m, nil
}

func (s *Service) applyMail(m mailStored) {
	cfg := mail.Settings{Enabled: m.Enabled && s.cfg.PublicURL != "", Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username,
		From: m.From, FromName: m.FromName, ReplyTo: m.ReplyTo}
	if m.PasswordEnc != "" {
		pw, err := s.open("mail", m.PasswordEnc)
		if err != nil {
			s.log.Warn("the stored mail password cannot be opened with this server's key; mail is off")
			cfg.Enabled = false
		}
		cfg.Password = pw
	}
	s.mail.Configure(cfg)
}

// MailEnabled says whether mail can be sent: enabled, with a public address.
func (s *Service) MailEnabled() bool { return s.cfg.PublicURL != "" && s.mail.Enabled() }

// GetMail is the settings and the state of the queue.
func (s *Service) GetMail(ctx context.Context) (*MailView, error) {
	m, err := s.readMail(ctx)
	if err != nil {
		return nil, err
	}
	return &MailView{mailStored: m, HasPassword: m.PasswordEnc != "", Status: s.mail.LastStatus(), Queue: s.mail.Stats()}, nil
}

func plainAddress(a string) bool {
	p, err := netmail.ParseAddress(a)
	return err == nil && p.Address == a && len(a) <= 254
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 || strings.ContainsAny(h, " \t/@:?#") && net.ParseIP(strings.Trim(h, "[]")) == nil {
		return false
	}
	return !strings.ContainsFunc(h, func(r rune) bool { return r <= 0x20 || r == 0x7f })
}

// UpdateMail applies a patch. Everything is checked before anything is stored.
func (s *Service) UpdateMail(ctx context.Context, actorID, ip string, p MailPatch) (*MailView, error) {
	s.mailMu.Lock()
	defer s.mailMu.Unlock()
	cur, err := s.readMail(ctx)
	if err != nil {
		return nil, err
	}
	next := cur
	changed := map[string]any{}
	set := func(name string, dst *string, v *string, max int) error {
		if v == nil {
			return nil
		}
		t := strings.TrimSpace(*v)
		if utf8.RuneCountInString(t) > max || strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) || !utf8.ValidString(t) {
			return errf(Invalid, "%s is at most %d characters.", name, max)
		}
		*dst = t
		return nil
	}
	if p.Host != nil {
		h := strings.TrimSpace(*p.Host)
		if h != "" && !validHost(h) {
			return nil, errf(Invalid, "Host is a name or address, with no port.")
		}
		next.Host = h
	}
	if p.Port != nil {
		if *p.Port < 1 || *p.Port > 65535 {
			return nil, errf(Invalid, "Port is 1 to 65535.")
		}
		next.Port = *p.Port
	}
	if p.Security != nil {
		if *p.Security != mail.StartTLS && *p.Security != mail.TLS && *p.Security != mail.None {
			return nil, errf(Invalid, "Security is starttls, tls or none.")
		}
		next.Security = *p.Security
	}
	if err := set("username", &next.Username, p.Username, 255); err != nil {
		return nil, err
	}
	if err := set("fromName", &next.FromName, p.FromName, 60); err != nil {
		return nil, err
	}
	if p.From != nil {
		f := strings.TrimSpace(*p.From)
		if f != "" && !plainAddress(f) {
			return nil, errf(Invalid, "From is an address like notes@example.com.")
		}
		next.From = f
	}
	if p.ReplyTo != nil {
		r := strings.TrimSpace(*p.ReplyTo)
		if r != "" && !plainAddress(r) {
			return nil, errf(Invalid, "replyTo is an address like help@example.com.")
		}
		next.ReplyTo = r
	}
	passwordGiven := p.Password != nil
	if passwordGiven && len(*p.Password) > 1024 {
		return nil, errf(Invalid, "Password is at most 1024 bytes.")
	}
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	// Rules that need more than one field.
	if next.Security == mail.None && next.Host != "" && !mail.IsLoopback(next.Host) {
		return nil, coded(400, "mail_insecure", "Security none is only allowed when host is a loopback address (a relay on this machine); use starttls or tls.")
	}
	if next.Enabled {
		if next.Host == "" {
			return nil, errf(Invalid, "Host is required to enable mail.")
		}
		if next.From == "" {
			return nil, coded(400, "mail_from_required", "Give the address mail is sent from.")
		}
		if s.cfg.PublicURL == "" {
			return nil, coded(400, "mail_needs_public_url", "The links in the messages point to --public-url; start the server with it.")
		}
	}
	if passwordGiven {
		if *p.Password == "" {
			next.PasswordEnc = ""
		} else {
			enc, err := s.seal("mail", *p.Password)
			if err != nil {
				return nil, err
			}
			next.PasswordEnc = enc
		}
	}
	diff := func(k string, a, b any) {
		if a != b {
			changed[k] = b
		}
	}
	diff("enabled", cur.Enabled, next.Enabled)
	diff("host", cur.Host, next.Host)
	diff("port", cur.Port, next.Port)
	diff("security", cur.Security, next.Security)
	diff("username", cur.Username, next.Username)
	diff("from", cur.From, next.From)
	diff("fromName", cur.FromName, next.FromName)
	diff("replyTo", cur.ReplyTo, next.ReplyTo)
	if passwordGiven && cur.PasswordEnc != next.PasswordEnc || passwordGiven && next.PasswordEnc != "" {
		changed["password"] = "changed"
	}
	if len(changed) > 0 {
		raw, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		if _, err := store.PutSetting(ctx, s.st.Q(), mailKey, string(raw), s.now()); err != nil {
			return nil, err
		}
		s.applyMail(next)
		s.Audit(AuditEvent{Kind: "mail_changed", Actor: actorID, IP: ip, Tenant: "-", Detail: changed})
	}
	return s.GetMail(ctx)
}

// MailTestResult is the answer of a test message.
type MailTestResult struct {
	OK      bool
	Message string
}

// TestMail sends one message now to an address the administrator typed, with
// the stored settings (enabled or not), waiting up to 15 seconds.
func (s *Service) TestMail(ctx context.Context, actorID, ip, to, lang string) (*MailTestResult, error) {
	to = strings.TrimSpace(to)
	if !plainAddress(to) {
		return nil, errf(Invalid, "To is an address like admin@example.com.")
	}
	m, err := s.readMail(ctx)
	if err != nil {
		return nil, err
	}
	if m.Host == "" {
		return nil, coded(409, "mail_not_configured", "Set up the mail server first.")
	}
	if m.From == "" {
		return nil, coded(400, "mail_from_required", "Give the address mail is sent from.")
	}
	if wait, ok := s.testCaps.take(s.now(), capKey{"mailtest", 10}); !ok {
		return nil, retryAfter(wait)
	}
	cfg := mail.Settings{Enabled: m.Enabled, Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username, From: m.From, FromName: m.FromName, ReplyTo: m.ReplyTo}
	if m.PasswordEnc != "" {
		if cfg.Password, err = s.open("mail", m.PasswordEnc); err != nil {
			return &MailTestResult{false, "the stored password cannot be opened with this server's key"}, nil
		}
	}
	msg := mail.Render(mail.KindTest, lang, mail.Vars{Site: s.siteName()})
	msg.To = to
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = s.mail.Test(tctx, cfg, msg)
	s.Audit(AuditEvent{Kind: "mail_test", Actor: actorID, IP: ip, Tenant: "-", Detail: map[string]any{"to": to, "ok": err == nil}})
	if err != nil {
		return &MailTestResult{false, mail.Describe(err, &cfg)}, nil
	}
	return &MailTestResult{OK: true}, nil
}

// siteName is how a message names the server: the host of the public address.
func (s *Service) siteName() string {
	if h := s.publicHost(); h != "" {
		return h
	}
	return "PetalBook"
}

// RunMail delivers the queue until ctx ends (Run starts it).
func (s *Service) RunMail(ctx context.Context) { s.mail.Run(ctx) }

// --- caps --------------------------------------------------------------------

// capKey is one count and its limit for an hour.
type capKey struct {
	key   string
	limit int
}

// hourCaps counts events per key in a fixed window of an hour that starts at
// the key's first event. In memory and bounded: when the table is full a new
// key is refused, never an old one forgotten (that would give a capped key its
// allowance back); a key not used for an hour is over and makes room.
type hourCaps struct {
	mu      sync.Mutex
	entries map[string]*hourEntry
	order   *list.List
	max     int
}

type hourEntry struct {
	key   string
	start time.Time
	last  time.Time // when it was last asked for: the list is in this order
	n     int
	elem  *list.Element
}

func newHourCaps(max int) *hourCaps {
	return &hourCaps{entries: map[string]*hourEntry{}, order: list.New(), max: max}
}

// take spends one of each key's allowance, all or none; when one is spent it
// returns how long until its hour is over.
func (h *hourCaps) take(now time.Time, keys ...capKey) (wait time.Duration, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	es := make([]*hourEntry, len(keys))
	for i, k := range keys {
		e := h.entries[k.key]
		if e != nil && now.Sub(e.start) >= time.Hour {
			h.drop(e)
			e = nil
		}
		if e != nil && e.n >= k.limit {
			h.touch(e, now)
			return e.start.Add(time.Hour).Sub(now), false
		}
		es[i] = e
	}
	need := 0
	for _, e := range es {
		if e == nil {
			need++
		}
	}
	// Room for the new keys: only keys not asked for in an hour go, and those
	// are over (their window started no later). Else nothing is spent.
	for need > 0 && len(h.entries)+need > h.max {
		back := h.order.Back()
		if back == nil || now.Sub(back.Value.(*hourEntry).last) < time.Hour {
			wait := time.Minute
			if back != nil {
				wait = back.Value.(*hourEntry).last.Add(time.Hour).Sub(now)
			}
			return wait, false
		}
		h.drop(back.Value.(*hourEntry))
	}
	for i, k := range keys {
		e := es[i]
		if e == nil {
			e = &hourEntry{key: k.key, start: now, last: now}
			e.elem = h.order.PushFront(e)
			h.entries[k.key] = e
		} else {
			h.touch(e, now)
		}
		e.n++
	}
	return 0, true
}

func (h *hourCaps) touch(e *hourEntry, now time.Time) {
	e.last = now
	h.order.MoveToFront(e.elem)
}

func (h *hourCaps) drop(e *hourEntry) {
	h.order.Remove(e.elem)
	delete(h.entries, e.key)
}

func (h *hourCaps) size() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// MailCapPerAddress and MailCapPerCaller are the caps of docs/api.md ("Abuse basics").
const (
	mailCapPerAddress = 3
	mailCapPerCaller  = 5
)

// mailSlot spends one message of the address's allowance and of the caller's
// (ip may be "" for a message nobody's address caused). false: nothing may be sent.
func (s *Service) mailSlot(email, ip string) (time.Duration, bool) {
	key, ok := store.EmailKey(email)
	if !ok {
		key = email
	}
	keys := []capKey{{"to:" + key, mailCapPerAddress}}
	if ip != "" {
		keys = append(keys, capKey{"by:" + ip, mailCapPerCaller})
	}
	return s.mailCaps.take(s.now(), keys...)
}

// sendMail renders a message and queues it.
func (s *Service) sendMail(kind, lang, to string, v mail.Vars) error {
	v.Site = s.siteName()
	m := mail.Render(kind, lang, v)
	m.To = to
	switch err := s.mail.Enqueue(m); {
	case err == nil:
		return nil
	case errors.Is(err, mail.ErrQueueFull):
		return coded(503, "unavailable", "The mail queue is full; try again later.")
	case errors.Is(err, mail.ErrNotEnabled):
		return coded(409, "mail_not_configured", "Mail is not enabled.")
	default:
		return err
	}
}

// link is an address in the web app that carries a token in its fragment.
func (s *Service) link(path, token string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + path + "#" + token
}
