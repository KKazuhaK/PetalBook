package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	netmail "net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/usage"
)

// Mail settings (docs/api.md, "Mail"): one sender for the whole server, in a
// site record; its password is the secret mail.password. Nothing is sent until
// they are enabled.

const mailKey = "mail"

type mailStored struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	From     string `json:"from"`
	FromName string `json:"fromName"`
	ReplyTo  string `json:"replyTo"`
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
	s.applyMail(ctx, m)
	return nil
}

func (s *Service) readMail(ctx context.Context) (mailStored, error) {
	m := defaultMail()
	raw, _, err := store.GetRecord(ctx, s.st.Q(), mailKey)
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

func (s *Service) applyMail(ctx context.Context, m mailStored) {
	cfg := mail.Settings{Enabled: m.Enabled && s.cfg.PublicURL != "", Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username,
		From: m.From, FromName: m.FromName, ReplyTo: m.ReplyTo}
	pw, _, err := s.vault.Open(ctx, s.st.Q(), "", secrets.MailPassword)
	if err != nil {
		s.log.Warn("the stored mail password cannot be opened with this server's key; mail is off")
		cfg.Enabled = false
	}
	cfg.Password = pw
	s.mail.Configure(cfg)
}

// ReloadMail makes the stored mail settings and password the ones in force:
// after the mail password was changed as a secret.
func (s *Service) ReloadMail(ctx context.Context) error {
	s.mailMu.Lock()
	defer s.mailMu.Unlock()
	return s.loadMail(ctx)
}

// MailEnabled says whether mail can be sent: enabled, with a public address.
func (s *Service) MailEnabled() bool { return s.cfg.PublicURL != "" && s.mail.Enabled() }

// GetMail is the settings and the state of the queue.
func (s *Service) GetMail(ctx context.Context) (*MailView, error) {
	m, err := s.readMail(ctx)
	if err != nil {
		return nil, err
	}
	has, err := s.vault.Has(ctx, s.st.Q(), "", secrets.MailPassword)
	if err != nil {
		return nil, err
	}
	return &MailView{mailStored: m, HasPassword: has, Status: s.mail.LastStatus(), Queue: s.mail.Stats()}, nil
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
	if passwordGiven {
		changed["password"] = "changed" // never the value
	}
	if len(changed) > 0 {
		raw, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		err = s.st.Tx(ctx, func(q store.Q) error {
			if _, err := store.PutRecord(ctx, q, mailKey, string(raw), s.now()); err != nil {
				return err
			}
			switch {
			case !passwordGiven:
				return nil
			case *p.Password == "":
				return s.vault.Clear(ctx, q, "", secrets.MailPassword)
			}
			return s.vault.Set(ctx, q, "", secrets.MailPassword, *p.Password, actorID)
		})
		if err != nil {
			return nil, err
		}
		s.applyMail(ctx, next)
		s.AuditIn(ctx, AuditEvent{Kind: "mail_changed", Actor: actorID, IP: ip, Tenant: "-", Detail: changed})
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
	if wait, ok := s.testCaps.TakeAll(ratelimit.Cost{Key: "mailtest", Limit: 10}); !ok {
		return nil, retryAfter(wait)
	}
	cfg := mail.Settings{Enabled: m.Enabled, Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username, From: m.From, FromName: m.FromName, ReplyTo: m.ReplyTo}
	if cfg.Password, _, err = s.vault.Open(ctx, s.st.Q(), "", secrets.MailPassword); err != nil {
		return &MailTestResult{false, "the stored password cannot be opened with this server's key"}, nil
	}
	msg := mail.Render(mail.KindTest, lang, mail.Vars{Site: s.siteName()})
	msg.To = to
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = s.mail.Test(tctx, cfg, msg)
	s.AuditIn(ctx, AuditEvent{Kind: "mail_test", Actor: actorID, IP: ip, Tenant: "-", Detail: map[string]any{"to": to, "ok": err == nil}})
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

// newHourCaps is a table of counts per key in a fixed window of an hour that
// starts at the key's first event. A full table refuses a new key and never
// forgets a live one (that would give a capped key its allowance back); a key
// whose hour is over makes room.
func newHourCaps(max int, now ratelimit.Clock) *ratelimit.Window {
	return ratelimit.NewWindow(max, time.Hour, ratelimit.KeepLive, now)
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
	to := ratelimit.Cost{Key: "to:" + key, Limit: mailCapPerAddress}
	if ip == "" {
		return s.mailCaps.TakeAll(to)
	}
	return s.mailCaps.TakeAll(to, ratelimit.Cost{Key: "by:" + ip, Limit: mailCapPerCaller})
}

// UseUsage gives the service the meter that counts the mail of each tenant.
func (s *Service) UseUsage(m *usage.Meter) { s.meter.Store(m) }

// securityMail is mail a person needs to get into or keep their account: a
// verification, a reset, a registration's confirmation. It is counted but never
// held to rate.mailPerDay, so that anonymous requests cannot use up a tenant's
// allowance and with it lock people out of their passwords.
var securityMail = map[string]bool{
	mail.KindVerify: true, mail.KindEmailClaim: true, mail.KindReset: true, mail.KindResetSSO: true,
	mail.KindRegister: true, mail.KindRegisterDup: true, mail.KindRegisterOrg: true, mail.KindRegisterOrgRefused: true,
	mail.KindRegisterJoin: true, mail.KindRegisterJoinRequest: true,
}

// reserveMail takes a message of the tenant's day: counted, and, unless it is
// security mail, within rate.mailPerDay. The slot is taken in memory in one step
// (the meter's lock), on top of what the database has of today, which is read
// here, outside any transaction, and only for a tenant that has a limit. The
// release gives the slot back when the message is not queued after all. A message
// of no tenant ("") is the site's own and is neither counted nor held.
func (s *Service) reserveMail(tenant, kind string) (release func(), err error) {
	m, st := s.meter.Load(), s.settings.Load()
	if tenant == "" || m == nil {
		return func() {}, nil
	}
	limit, limited := int64(0), false
	if st != nil && !securityMail[kind] {
		limit, limited = st.LimitOf(context.Background(), tenant, settings.RateMailPerDay)
	}
	if !limited {
		d := m.Today()
		m.AddAt(d, tenant, usage.Mail, 1)
		return func() { m.AddAt(d, tenant, usage.Mail, -1) }, nil
	}
	now := s.now().UTC()
	for range 3 {
		gen := m.Gen() // before the day's row is read
		sent, err := store.UsageDailyValue(context.Background(), s.st.Q(), tenant, store.UsageDay(now), store.UsageKeyMail)
		if err != nil {
			return nil, err
		}
		d, ok, stale := m.Reserve(tenant, usage.Mail, 1, sent, limit, gen)
		if stale {
			continue
		}
		if ok {
			// Given back on the day it was taken, if the message is not queued after all.
			return func() { m.AddAt(d, tenant, usage.Mail, -1) }, nil
		}
		s.OverLimit(tenant, settings.RateMailPerDay, limit)
		return nil, retryAfter(now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now))
	}
	// A flush ended each time we looked: hold the message back rather than guess.
	return nil, retryAfter(time.Second)
}

// chargeMail counts one message against a tenant's day without sending one (""
// is no tenant: nothing is counted). It is for a request charged by what was
// asked, whatever is sent for it.
func (s *Service) chargeMail(tenant string) {
	if m := s.meter.Load(); m != nil && tenant != "" {
		m.Add(tenant, usage.Mail, 1)
	}
}

// enqueueFor queues a message for a tenant's account of the day: every message of
// the site's mailer goes through here, so that it is counted and held to the
// tenant's limit whichever flow sends it.
func (s *Service) enqueueFor(tenant, kind string, msg mail.Message) error {
	release, err := s.reserveMail(tenant, kind)
	if err != nil {
		return err
	}
	if err := s.mail.Enqueue(msg); err != nil {
		release()
		return err
	}
	return nil
}

// OverLimit writes quota_exceeded, at most once a day for a tenant and key.
func (s *Service) OverLimit(tenant, key string, limit int64) {
	day := store.UsageDay(s.now())
	if prev, loaded := s.overDay.Swap(tenant+"/"+key, day); loaded && prev == day {
		return
	}
	s.Audit(AuditEvent{Kind: "quota_exceeded", Tenant: tenant, Detail: map[string]any{"quota": key, "limit": limit, "scope": "tenant"}})
}

// sendMail renders a message and queues it, for a tenant ("" for none): the
// tenant's mail is held to rate.mailPerDay and counted (see enqueueFor).
func (s *Service) sendMail(tenant, kind, lang, to string, v mail.Vars) error {
	v.Site = s.siteName()
	m := mail.Render(kind, lang, v)
	m.To = to
	switch err := s.enqueueFor(tenant, kind, m); {
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
