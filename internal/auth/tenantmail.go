package auth

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/dailyfetch"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// An organisation's own sender (docs/api.md, "Mail: GET/PUT
// /api/admin/tenants/{tenant}/mail"). Its settings that are not secret are the
// tenant's integration record of kind smtp; its password is the tenant's secret
// mail.password. Every enabled one is kept in memory (the mail service holds
// them), read at startup and again for one tenant when that tenant's changes, so
// no message makes a query for it.
//
// The server makes the connections, so a tenant's sender reaches public
// addresses only (checked on the address each name resolves to, as it is
// dialled), only the ports mail uses, and only over TLS: security none is
// refused whatever the host.

// tenantMailPorts are the ports a tenant's sender may use.
var tenantMailPorts = map[int]bool{25: true, 465: true, 587: true, 2525: true}

// maxTenantSenderName is the longest display name made from a tenant's name.
const maxTenantSenderName = 60

// tenantMailStored is the record of kind smtp.
type tenantMailStored struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	From     string `json:"from"`
	FromName string `json:"fromName"`
	ReplyTo  string `json:"replyTo"`
}

func defaultTenantMail() tenantMailStored {
	return tenantMailStored{Port: 587, Security: mail.StartTLS}
}

// TenantMailView is what GET /api/admin/tenants/{tenant}/mail shows. Sender is
// whose mailer the tenant's messages go by now: "tenant" (its own), "site" or
// "" (none: they are not sent).
type TenantMailView struct {
	tenantMailStored
	HasPassword bool
	Status      mail.Status
	Sender      string
}

// TrustTenantMailForTests lets tenants' senders reach any address and port, with
// certificates checked against roots: the fake server of a test is on loopback.
// It is called before the settings it applies to are made.
func (s *Service) TrustTenantMailForTests(roots *x509.CertPool) {
	s.tenantMailOpen.Store(true)
	s.mail.TrustRoots(roots)
}

// loadTenantMail reads every tenant's sender into memory.
func (s *Service) loadTenantMail(ctx context.Context) error {
	rows, err := store.ListIntegrationsOfKind(ctx, s.st.Q(), store.IntegrationSMTP)
	if err != nil {
		return err
	}
	for _, r := range rows {
		m, err := parseTenantMail(r.Config)
		if err != nil {
			s.log.Warn("a tenant's mail settings cannot be read; its own sender is off", "tenant", r.TenantID)
			continue
		}
		s.applyTenantMail(ctx, r.TenantID, m)
	}
	return nil
}

func parseTenantMail(raw string) (tenantMailStored, error) {
	m := defaultTenantMail()
	if raw == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return defaultTenantMail(), err
	}
	return m, nil
}

func (s *Service) readTenantMail(ctx context.Context, tenant string) (m tenantMailStored, found bool, err error) {
	r, err := store.GetIntegration(ctx, s.st.Q(), tenant, store.IntegrationSMTP, "")
	if errors.Is(err, store.ErrNotFound) {
		return defaultTenantMail(), false, nil
	}
	if err != nil {
		return m, false, err
	}
	m, err = parseTenantMail(r.Config)
	if err != nil {
		return defaultTenantMail(), true, nil
	}
	return m, true, nil
}

// tenantSettings is what the mail service sends a tenant's messages with.
func (s *Service) tenantSettings(m tenantMailStored, password string) mail.Settings {
	cfg := mail.Settings{Enabled: m.Enabled && s.cfg.PublicURL != "", Host: m.Host, Port: m.Port, Security: m.Security, Username: m.Username,
		Password: password, From: m.From, FromName: m.FromName, ReplyTo: m.ReplyTo}
	if !s.tenantMailOpen.Load() {
		refuse := dailyfetch.RefuseAddrs(func(ip netip.Addr) bool { return !dailyfetch.Public(ip) })
		cfg.Control = func(network, address string, c syscall.RawConn) error {
			if refuse(network, address, c) != nil {
				return mail.ErrNotPublic // the address is not told
			}
			return nil
		}
	}
	return cfg
}

// applyTenantMail makes the stored settings and password of a tenant the ones in
// force. A password this server's key cannot open turns the sender off.
func (s *Service) applyTenantMail(ctx context.Context, tenant string, m tenantMailStored) {
	pw, _, err := s.vault.Open(ctx, s.st.Q(), tenant, secrets.MailPassword)
	if err != nil {
		// Not a reason to drop what waits: the sender in force stays as it was (none,
		// at startup) and the next change or restart reads the password again.
		s.log.Warn("a tenant's stored mail password cannot be read now; its sender in force is left as it was", "tenant", tenant, "error", err)
		return
	}
	cfg := s.tenantSettings(m, pw)
	s.mail.SetTenant(tenant, &cfg)
}

// ReloadTenantMail makes a tenant's stored settings and password the ones in
// force: after its mail password was changed as a secret.
func (s *Service) ReloadTenantMail(ctx context.Context, tenant string) error {
	s.tenantMailMu.Lock()
	defer s.tenantMailMu.Unlock()
	m, found, err := s.readTenantMail(ctx, tenant)
	if err != nil {
		return err
	}
	if !found {
		s.mail.SetTenant(tenant, nil)
		return nil
	}
	s.applyTenantMail(ctx, tenant, m)
	return nil
}

// ForgetTenantMail drops a deleted tenant's sender from memory.
func (s *Service) ForgetTenantMail(tenant string) { s.mail.SetTenant(tenant, nil) }

// GetTenantMail is a tenant's own sender as an administrator sees it.
func (s *Service) GetTenantMail(ctx context.Context, tenant string) (*TenantMailView, error) {
	m, _, err := s.readTenantMail(ctx, tenant)
	if err != nil {
		return nil, err
	}
	has, err := s.vault.Has(ctx, s.st.Q(), tenant, secrets.MailPassword)
	if err != nil {
		return nil, err
	}
	return &TenantMailView{tenantMailStored: m, HasPassword: has, Status: s.mail.TenantStatus(tenant), Sender: s.MailSender(tenant)}, nil
}

// TenantMailPatch is a PUT of a tenant's sender; nil fields are left alone.
type TenantMailPatch = MailPatch

// validPublicHostName refuses a host that cannot be a public mail server
// before any connection: an address that is not public, and the names of this
// machine. A name that resolves to one is refused when it is dialled.
func validPublicHostName(h string) bool {
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return false
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return dailyfetch.Public(ip)
	}
	return true
}

// siteDomain says whether a domain is the server's own (or under it): a tenant
// may not send as the site.
func (s *Service) siteDomain(domain string) bool {
	d := strings.ToLower(domain)
	under := func(h string) bool { return h != "" && (d == h || strings.HasSuffix(d, "."+h)) }
	h := strings.ToLower(s.publicHost())
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h, "]") {
		h = h[:i]
	}
	if under(h) {
		return true
	}
	// The domain of the address the site itself sends from.
	if site, err := s.readMail(context.Background()); err == nil {
		if i := strings.LastIndex(site.From, "@"); i >= 0 {
			return d == strings.ToLower(site.From[i+1:])
		}
	}
	return false
}

// UpdateTenantMail applies a patch to a tenant's sender. Everything is checked
// before anything is stored.
func (s *Service) UpdateTenantMail(ctx context.Context, actorID, ip, tenant string, p TenantMailPatch) (*TenantMailView, error) {
	s.tenantMailMu.Lock()
	defer s.tenantMailMu.Unlock()
	cur, _, err := s.readTenantMail(ctx, tenant)
	if err != nil {
		return nil, err
	}
	next := cur
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
			return nil, errf(Invalid, "Security is starttls or tls.")
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
	// Rules that need more than one field. A tenant's sender is always over TLS.
	if next.Security == mail.None {
		return nil, coded(400, "mail_insecure", "Security none is not allowed for an organisation's sender; use starttls or tls.")
	}
	if err := s.checkTenantTarget(next); err != nil {
		return nil, err
	}
	if next.Enabled {
		if next.Host == "" {
			return nil, errf(Invalid, "Host is required to enable the sender.")
		}
		if next.From == "" {
			return nil, coded(400, "mail_from_required", "Give the address mail is sent from.")
		}
		if s.cfg.PublicURL == "" {
			return nil, coded(400, "mail_needs_public_url", "The links in the messages point to --public-url; start the server with it.")
		}
	}
	changed := map[string]any{}
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
			err := store.PutIntegration(ctx, q, store.IntegrationRow{TenantID: tenant, Kind: store.IntegrationSMTP, Config: string(raw), UpdatedBy: actorID, Updated: s.now()})
			switch {
			case err != nil || !passwordGiven:
				return err
			case *p.Password == "":
				return s.vault.Clear(ctx, q, tenant, secrets.MailPassword)
			}
			return s.vault.Set(ctx, q, tenant, secrets.MailPassword, *p.Password, actorID)
		})
		if err != nil {
			return nil, err
		}
		s.applyTenantMail(ctx, tenant, next)
		s.AuditIn(ctx, AuditEvent{Kind: "tenant_mail_changed", Actor: actorID, IP: ip, Tenant: tenant, Detail: changed})
	}
	return s.GetTenantMail(ctx, tenant)
}

// checkTenantTarget refuses a host, port or sender address a tenant's sender may
// not have.
func (s *Service) checkTenantTarget(m tenantMailStored) error {
	if m.Host != "" && !s.tenantMailOpen.Load() {
		if !validPublicHostName(m.Host) {
			return coded(400, "mail_host_not_public", "The mail server must be at a public address; one on this machine or a private network cannot be used.")
		}
		if !tenantMailPorts[m.Port] {
			return errf(Invalid, "Port is 25, 465, 587 or 2525.")
		}
	}
	if i := strings.LastIndex(m.From, "@"); i >= 0 && s.siteDomain(m.From[i+1:]) {
		return errf(Invalid, "From may not be an address on the server's own domain or the domain the site sends from.")
	}
	return nil
}

// DeleteTenantMail removes a tenant's sender and its password.
func (s *Service) DeleteTenantMail(ctx context.Context, actorID, ip, tenant string) error {
	s.tenantMailMu.Lock()
	defer s.tenantMailMu.Unlock()
	_, found, err := s.readTenantMail(ctx, tenant)
	if err != nil {
		return err
	}
	hadPassword, err := s.vault.Has(ctx, s.st.Q(), tenant, secrets.MailPassword)
	if err != nil {
		return err
	}
	if !found && !hadPassword {
		return nil
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.DeleteIntegration(ctx, q, tenant, store.IntegrationSMTP, ""); err != nil {
			return err
		}
		return s.vault.Clear(ctx, q, tenant, secrets.MailPassword)
	})
	if err != nil {
		return err
	}
	s.mail.SetTenant(tenant, nil)
	s.AuditIn(ctx, AuditEvent{Kind: "tenant_mail_changed", Actor: actorID, IP: ip, Tenant: tenant, Detail: map[string]any{"removed": true}})
	return nil
}

// TestTenantMail sends one message now through a tenant's stored sender (enabled
// or not) to an address the administrator typed, waiting up to 15 seconds. It is
// held to the site's test limit, 10 an hour, for each tenant.
func (s *Service) TestTenantMail(ctx context.Context, actorID, ip, tenant, to, lang string) (*MailTestResult, error) {
	to = strings.TrimSpace(to)
	if !plainAddress(to) {
		return nil, errf(Invalid, "To is an address like admin@example.com.")
	}
	m, _, err := s.readTenantMail(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if m.Host == "" {
		return nil, coded(409, "mail_not_configured", "Set up the mail server first.")
	}
	if m.From == "" {
		return nil, coded(400, "mail_from_required", "Give the address mail is sent from.")
	}
	if wait, ok := s.testCaps.TakeAll(ratelimit.Cost{Key: "mailtest:" + tenant, Limit: 10}); !ok {
		return nil, retryAfter(wait)
	}
	pw, _, err := s.vault.Open(ctx, s.st.Q(), tenant, secrets.MailPassword)
	if err != nil {
		return &MailTestResult{false, "the stored password cannot be opened with this server's key"}, nil
	}
	cfg := s.tenantSettings(m, pw)
	cfg.Enabled = true
	msg := mail.Render(mail.KindTest, lang, mail.Vars{Site: s.siteName()})
	msg.To = to
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = s.mail.TestFor(tctx, tenant, cfg, msg)
	s.AuditIn(ctx, AuditEvent{Kind: "tenant_mail_test", Actor: actorID, IP: ip, Tenant: tenant, Detail: map[string]any{"to": to, "ok": err == nil}})
	if err != nil {
		return &MailTestResult{false, mail.Describe(err, &cfg)}, nil
	}
	return &MailTestResult{OK: true}, nil
}

// --- which sender a message uses ----------------------------------------------------

// Whose mailer a tenant's messages go by.
const (
	SenderTenant = "tenant"
	SenderSite   = "site"
)

// MailSender says whose mailer the messages of a tenant go by now: "tenant"
// when it has a sender of its own in force (and the site lets it, mail.ownSender),
// else "site" when the site's mail is on and mail.useSite is in effect for it,
// else "" (not sent). "" asks for the site's own. From memory.
func (s *Service) MailSender(tenant string) string {
	return s.mailSender(context.Background(), tenant)
}

func (s *Service) mailSender(ctx context.Context, tenant string) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	set := s.settings.Load()
	if tenant != "" && s.mail.TenantEnabled(tenant) && (set == nil || set.On(ctx, tenant, settings.MailOwnSender)) {
		return SenderTenant
	}
	if !s.mail.Enabled() {
		return ""
	}
	if tenant == "" || set == nil || set.On(ctx, tenant, settings.MailUseSite) {
		return SenderSite
	}
	return ""
}

// MailReadyFor says whether a message for the tenant can be sent now, by its own
// sender or the site's.
func (s *Service) MailReadyFor(ctx context.Context, tenant string) bool {
	return s.mailSender(ctx, tenant) != ""
}

// mailRoute is where a message of the tenant goes: the tenant's own sender ("tenant",
// which is the route's name), or the site's (""); ok is false for none.
func (s *Service) mailRoute(ctx context.Context, tenant string) (route string, ok bool) {
	switch s.mailSender(ctx, tenant) {
	case SenderTenant:
		return tenant, true
	case SenderSite:
		return "", true
	}
	return "", false
}

// noSender writes mail_not_sent, at most once a day for a tenant: the way a
// message over a limit is recorded (OverLimit).
func (s *Service) noSender(tenant string) {
	day := store.UsageDay(s.now())
	if prev, loaded := s.overDay.Swap(tenant+"/mail.sender", day); loaded && prev == day {
		return
	}
	s.Audit(AuditEvent{Kind: "mail_not_sent", Tenant: tenant, Detail: map[string]any{"reason": "no_sender"}})
}

// siteFromName is the display name of a message the site's mailer sends for a
// tenant: "<tenant name> via <site name>" (the site's own name for the default
// tenant, and for a name that looks like an address or has characters a display
// name must not).
func (s *Service) siteFromName(tenant string) string {
	if tenant == "" || tenant == store.DefaultTenantID {
		return ""
	}
	n := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(s.tenantName(tenant)))
	if n == "" || strings.ContainsAny(n, "@<>\"") {
		return ""
	}
	if utf8.RuneCountInString(n) > maxTenantSenderName {
		n = string([]rune(n)[:maxTenantSenderName])
	}
	site := s.mail.SiteFromName()
	if site == "" {
		site = s.siteName()
	}
	return n + " via " + site
}
