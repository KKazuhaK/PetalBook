package domains

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

const (
	// CodeTTL is how long a verification code is good for.
	CodeTTL = 7 * 24 * time.Hour
	// RecheckEvery is how often a verified domain is looked at again.
	RecheckEvery = 24 * time.Hour
	// LapseAfter is how long a verified domain's record may be missing before
	// the claim lapses.
	LapseAfter = 7 * 24 * time.Hour
	// RetryAfterError is when a recheck DNS could not answer is tried again: a
	// failure to ask is not "missing".
	RetryAfterError = time.Hour
	// PostponeOutbound is when a recheck its tenant's outbound share refused is
	// tried again.
	PostponeOutbound = time.Minute
	// MaxPerTenant is the most claims a tenant may hold.
	MaxPerTenant = 20
	// PerDomainMinute and PerTenantHour are the limits of "Check now".
	PerDomainMinute = 1
	PerTenantHour   = 20
	// lookupTimeout bounds one check's questions to DNS.
	lookupTimeout = 10 * time.Second
	// recheckBatch is how many due domains one pass of the timer takes.
	recheckBatch = 50
	maxLimitKeys = 100_000
)

// Error is a refusal, with the answer's status and code.
type Error struct {
	Status     int
	Code       string
	Msg        string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Msg }

func refuse(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Msg: msg} }

// The refusals.
var (
	ErrInvalid  = refuse(http.StatusBadRequest, "invalid", "That is not a domain that can be claimed: give a domain name such as example.com.")
	ErrPublic   = refuse(http.StatusBadRequest, "domain_public", "That is a public mail service's domain: anyone can have an address there, so no organization can claim it.")
	ErrExists   = refuse(http.StatusConflict, "domain_exists", "This organization has claimed that domain already.")
	ErrTooMany  = refuse(http.StatusConflict, "domain_limit", "An organization can claim at most 20 domains.")
	ErrNotFound = refuse(http.StatusNotFound, "not_found", "No such domain.")
	ErrClaimed  = refuse(http.StatusConflict, "domain_claimed", "Another organization has verified this domain. A site administrator can transfer it.")
	ErrExpired  = refuse(http.StatusConflict, "domain_code_expired", "The verification code has expired: make a new one and publish it.")
	ErrVerified = refuse(http.StatusConflict, "domain_verified", "The domain is verified: its record stays as it is.")
)

// Options configures a Service.
type Options struct {
	Store    *store.Store
	Settings *settings.Service
	// Resolver asks DNS (NetResolver when nil).
	Resolver Resolver
	// SiteHost is the host name of the server's public address: a CNAME
	// record points under it. Without one only TXT records are offered.
	SiteHost string
	Now      func() time.Time
	// After is the recheck's timer: a channel that fires after d, and a stop
	// (default time.NewTimer; tests).
	After  func(d time.Duration) (<-chan time.Time, func())
	Logger *slog.Logger
}

// owner is one verified domain in memory.
type owner struct {
	tenant   string
	restrict bool
}

type book struct {
	byDomain map[string]owner
	byTenant map[string][]string
}

// Service is safe for concurrent use.
type Service struct {
	opt  Options
	log  *slog.Logger
	book atomic.Pointer[book]
	wake chan struct{}
	// minute holds the per-domain minute, the site's minute and each tenant's
	// outbound share; hour each tenant's hour of checks by hand.
	minute, hour *ratelimit.Window
	reloadMu     sync.Mutex

	// dueHook, when set, runs before each pass of the recheck (tests).
	dueHook func()
}

// New makes a service; Load fills its map, Run runs its recheck.
func New(opt Options) *Service {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Resolver == nil {
		opt.Resolver = NetResolver{}
	}
	if opt.Settings == nil && opt.Store != nil {
		opt.Settings = settings.Of(opt.Store)
	}
	if opt.After == nil {
		opt.After = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		}
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	now := ratelimit.Clock(func() time.Time { return opt.Now() })
	s := &Service{opt: opt, log: opt.Logger, wake: make(chan struct{}, 1),
		minute: ratelimit.NewWindow(maxLimitKeys, time.Minute, ratelimit.KeepLive, now),
		hour:   ratelimit.NewWindow(maxLimitKeys, time.Hour, ratelimit.KeepLive, now)}
	s.book.Store(&book{byDomain: map[string]owner{}, byTenant: map[string][]string{}})
	return s
}

// SiteHost is the host a CNAME record points under ("" when there is none).
func (s *Service) SiteHost() string { return s.opt.SiteHost }

// Load reads every verified domain into the map: at start, and after every
// change of one (Changed).
func (s *Service) Load(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	rows, err := store.VerifiedDomains(ctx, s.opt.Store.Q())
	if err != nil {
		return err
	}
	b := &book{byDomain: make(map[string]owner, len(rows)), byTenant: map[string][]string{}}
	for _, r := range rows {
		b.byDomain[r.Domain] = owner{r.TenantID, r.RestrictOrgs}
		b.byTenant[r.TenantID] = append(b.byTenant[r.TenantID], r.Domain)
	}
	s.book.Store(b)
	return nil
}

// Changed reads the map again after a change, and wakes the recheck (a domain
// verified now is due in a day; one removed is not due at all).
func (s *Service) Changed(ctx context.Context) {
	if err := s.Load(ctx); err != nil {
		s.log.Error("could not read the verified domains", "error", err)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// on says whether the tenant has the feature: a domain of a tenant that does
// not is kept, and has no effect.
func (s *Service) on(ctx context.Context, tenant string) bool {
	if s.opt.Settings == nil {
		return true
	}
	on, err := s.opt.Settings.Feature(ctx, tenant, settings.FeatureDomains)
	return err == nil && on
}

// OfTenant is the verified domains of a tenant, from memory (auth.Config.OrgDomains).
func (s *Service) OfTenant(ctx context.Context, tenant string) []string {
	ds := s.book.Load().byTenant[tenant]
	if len(ds) == 0 || !s.on(ctx, tenant) {
		return nil
	}
	return append([]string(nil), ds...)
}

// Holder is the tenant that holds a domain (a key) verified, and whether it
// asked that nobody make an organisation on it; ok false when none does, or
// its tenant does not have the feature now.
func (s *Service) Holder(ctx context.Context, domain string) (tenant string, restrict, ok bool) {
	o, ok := s.book.Load().byDomain[domain]
	if !ok || !s.on(ctx, o.tenant) {
		return "", false, false
	}
	return o.tenant, o.restrict, true
}

// Managed is the tenant that holds the domain of an address and keeps others
// from making organisations on it ("" when none does).
func (s *Service) Managed(ctx context.Context, email string) string {
	if t, restrict, ok := s.Holder(ctx, EmailDomain(email)); ok && restrict {
		return t
	}
	return ""
}

// Count is how many domains the map holds (tests, status).
func (s *Service) Count() int { return len(s.book.Load().byDomain) }

// Claim is a new claim with its code, which is shown this once.
type Claim struct {
	Row  store.DomainRow
	Code string
}

// Claim records a tenant's claim of a domain. Whether another tenant claimed
// or holds it is not told here: only verifying says, to whoever can publish
// the record.
func (s *Service) Claim(ctx context.Context, tenant, input, by string) (Claim, error) {
	key, ok := Key(input)
	if !ok {
		return Claim{}, ErrInvalid
	}
	if Public(key) {
		return Claim{}, ErrPublic
	}
	q := s.opt.Store.Q()
	if n, err := store.CountTenantDomains(ctx, q, tenant); err != nil {
		return Claim{}, err
	} else if n >= MaxPerTenant {
		return Claim{}, ErrTooMany
	}
	now := s.opt.Now()
	code := NewCode()
	r := store.DomainRow{TenantID: tenant, Domain: key, CodeHash: HashCode(code), CodeExpires: now.Add(CodeTTL), CreatedBy: by, Created: now, Updated: now}
	if err := store.InsertDomain(ctx, q, r); errors.Is(err, store.ErrExists) {
		return Claim{}, ErrExists
	} else if err != nil {
		return Claim{}, err
	}
	r, err := store.GetDomain(ctx, q, tenant, key)
	return Claim{Row: r, Code: code}, err
}

// Get is a tenant's claim of a domain (given in any spelling).
func (s *Service) Get(ctx context.Context, tenant, input string) (store.DomainRow, error) {
	key, ok := Key(input)
	if !ok {
		return store.DomainRow{}, ErrNotFound
	}
	r, err := store.GetDomain(ctx, s.opt.Store.Q(), tenant, key)
	if errors.Is(err, store.ErrNotFound) {
		return r, ErrNotFound
	}
	return r, err
}

// NewCode gives a claim that is not verified a new code (the old one stops
// counting).
func (s *Service) NewCode(ctx context.Context, tenant, input string) (Claim, error) {
	r, err := s.Get(ctx, tenant, input)
	if err != nil {
		return Claim{}, err
	}
	if r.Verified() {
		return Claim{}, ErrVerified
	}
	now := s.opt.Now()
	code := NewCode()
	if ok, err := store.SetDomainCode(ctx, s.opt.Store.Q(), tenant, r.Domain, HashCode(code), now.Add(CodeTTL), now); err != nil {
		return Claim{}, err
	} else if !ok {
		return Claim{}, ErrVerified // verified meanwhile
	}
	r, err = store.GetDomain(ctx, s.opt.Store.Q(), tenant, r.Domain)
	return Claim{Row: r, Code: code}, err
}

// SetRestrict sets whether others may make organisations on the domain.
func (s *Service) SetRestrict(ctx context.Context, tenant, input string, restrict bool) (store.DomainRow, error) {
	r, err := s.Get(ctx, tenant, input)
	if err != nil {
		return r, err
	}
	if _, err := store.SetDomainRestrict(ctx, s.opt.Store.Q(), tenant, r.Domain, restrict, s.opt.Now()); err != nil {
		return r, err
	}
	if r.Verified() {
		s.Changed(ctx)
	}
	return store.GetDomain(ctx, s.opt.Store.Q(), tenant, r.Domain)
}

// Remove deletes a tenant's claim.
func (s *Service) Remove(ctx context.Context, tenant, input string) (store.DomainRow, error) {
	r, err := s.Get(ctx, tenant, input)
	if err != nil {
		return r, err
	}
	if ok, err := store.DeleteDomain(ctx, s.opt.Store.Q(), tenant, r.Domain); err != nil {
		return r, err
	} else if !ok {
		return r, ErrNotFound
	}
	if r.Verified() {
		s.Changed(ctx)
	}
	return r, nil
}

// Assign moves a verified domain to another tenant (a site administrator's
// transfer). It returns the tenant that held it.
func (s *Service) Assign(ctx context.Context, input, to, by string) (string, store.DomainRow, error) {
	key, ok := Key(input)
	if !ok {
		return "", store.DomainRow{}, ErrNotFound
	}
	from, err := store.AssignDomain(ctx, s.opt.Store.Q(), key, to, by, s.opt.Now())
	if errors.Is(err, store.ErrNotFound) {
		return "", store.DomainRow{}, refuse(http.StatusNotFound, "not_found", "No organization has verified that domain.")
	}
	if err != nil {
		return "", store.DomainRow{}, err
	}
	s.Changed(ctx)
	r, err := store.GetDomain(ctx, s.opt.Store.Q(), to, key)
	return from, r, err
}

// takeManual spends one check by hand of the domain: the domain's minute, the
// tenant's hour, the site's minute (domains.checksPerMinute) and the tenant's
// outbound share (rate.outboundPerMinute), all or none of the minute's. The
// hour is spent last, so a refusal of a minute does not cost one of its 20.
func (s *Service) takeManual(ctx context.Context, tenant, domain string) error {
	site := int64(60)
	if s.opt.Settings != nil {
		if e, err := s.opt.Settings.Get(ctx, "", settings.DomainsChecksPerMinute); err == nil {
			if v, ok := e.Value.(int64); ok {
				site = v
			}
		}
	}
	costs := []ratelimit.Cost{{Key: "d:" + tenant + ":" + domain, Limit: PerDomainMinute}, {Key: "site", Limit: int(site)}}
	if c, ok, err := s.outboundCost(ctx, tenant); err != nil {
		return err
	} else if ok {
		if c.Limit <= 0 {
			return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "This organization may not call out to other servers now.", RetryAfter: time.Minute}
		}
		costs = append(costs, c)
	}
	if wait, ok := s.minute.TakeAll(costs...); !ok {
		return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "Checked a moment ago: try again shortly.", RetryAfter: wait}
	}
	if wait, ok := s.hour.TakeAll(ratelimit.Cost{Key: "t:" + tenant, Limit: PerTenantHour}); !ok {
		return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "This organization has checked its domains 20 times this hour: try again later.", RetryAfter: wait}
	}
	return nil
}

// outboundCost is the tenant's outbound share (rate.outboundPerMinute) as a
// cost of the minute's table; ok false when it has no limit.
func (s *Service) outboundCost(ctx context.Context, tenant string) (ratelimit.Cost, bool, error) {
	if s.opt.Settings == nil {
		return ratelimit.Cost{}, false, nil
	}
	e, err := s.opt.Settings.Get(ctx, tenant, settings.RateOutboundPerMinute)
	if err != nil {
		return ratelimit.Cost{}, false, err
	}
	v, ok := e.Value.(int64)
	if !ok {
		return ratelimit.Cost{}, false, nil
	}
	return ratelimit.Cost{Key: "o:" + tenant, Limit: int(min(v, 1<<30))}, true, nil
}

// Result is what a check by hand did.
type Result struct {
	Check Check
	Row   store.DomainRow
	// Verified: this check verified the claim.
	Verified bool
	// Claimed: the record was found, but another tenant holds the domain.
	Claimed bool
}

// Verify is "Check now": it asks DNS for the claim's record (within the
// limits) and, when it finds it on a claim that is not verified, verifies it,
// unless another tenant holds the domain: then the answer is ErrClaimed, with
// the check in the Result. A check of a verified claim records what it saw and
// changes nothing of the daily recheck (only a record found clears "missing").
func (s *Service) Verify(ctx context.Context, tenant, input string) (Result, error) {
	r, err := s.Get(ctx, tenant, input)
	if err != nil {
		return Result{}, err
	}
	now := s.opt.Now()
	if !r.Verified() && !r.CodeExpires.After(now) {
		return Result{Row: r}, ErrExpired
	}
	if err := s.takeManual(ctx, tenant, r.Domain); err != nil {
		return Result{Row: r}, err
	}
	c := s.ask(ctx, r, ByManual)
	res := Result{Check: c, Row: r}
	q := s.opt.Store.Q()
	if c.Result == Found && !r.Verified() {
		ok, err := store.VerifyDomain(ctx, q, tenant, r.Domain, c.JSON(), c.At, c.At.Add(RecheckEvery))
		if errors.Is(err, store.ErrDomainClaimed) {
			res.Claimed = true
			_ = store.RecordDomainCheck(ctx, q, tenant, r.Domain, c.JSON(), c.At, false)
			res.Row, _ = store.GetDomain(ctx, q, tenant, r.Domain)
			return res, ErrClaimed
		}
		if err != nil {
			return res, err
		}
		if ok {
			res.Verified = true
			s.Changed(ctx)
		}
	} else if err := store.RecordDomainCheck(ctx, q, tenant, r.Domain, c.JSON(), c.At, c.Result == Found); err != nil {
		return res, err
	}
	res.Row, err = store.GetDomain(ctx, q, tenant, r.Domain)
	if errors.Is(err, store.ErrNotFound) {
		return res, ErrNotFound
	}
	return res, err
}

// ask asks DNS for a claim's record, bounded in time.
func (s *Service) ask(ctx context.Context, r store.DomainRow, by string) Check {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	c := lookup(ctx, s.opt.Resolver, r.Domain, r.CodeHash, s.opt.SiteHost, s.opt.Now())
	c.By = by
	return c
}

// Run is the daily recheck: one timer that sleeps until the soonest verified
// domain is due (or until a change wakes it), rechecks what is due, and sleeps
// again. Nothing polls. It returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-s.wake:
		default:
		}
		if s.dueHook != nil {
			s.dueHook()
		}
		more := s.RecheckDue(ctx)
		if ctx.Err() != nil {
			return
		}
		var fire <-chan time.Time
		stop := func() {}
		if more {
			fire, stop = s.opt.After(0)
		} else if next, ok, err := store.NextDomainCheck(ctx, s.opt.Store.Q()); err != nil {
			s.log.Error("could not look for the next domain to check", "error", err)
			fire, stop = s.opt.After(RetryAfterError)
		} else if ok {
			fire, stop = s.opt.After(max(next.Sub(s.opt.Now()), 0))
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-s.wake:
		case <-fire:
		}
		stop()
	}
}

// RecheckDue rechecks the verified domains due now, at most a batch of them;
// it reports whether more may be due.
func (s *Service) RecheckDue(ctx context.Context) bool {
	q := s.opt.Store.Q()
	due, err := store.DueDomains(ctx, q, s.opt.Now(), recheckBatch)
	if err != nil {
		s.log.Error("could not read the domains due for a check", "error", err)
		return false
	}
	changed := false
	for _, r := range due {
		if ctx.Err() != nil {
			return false
		}
		lapsed, err := s.recheck(ctx, r)
		if err != nil {
			s.log.Error("could not record a domain's check", "domain", r.Domain, "error", err)
			// Not again at once: the next pass is in an hour for this one.
			_ = store.PostponeDomainCheck(ctx, q, r, s.opt.Now().Add(RetryAfterError))
		}
		changed = changed || lapsed
	}
	if changed {
		if err := s.Load(ctx); err != nil {
			s.log.Error("could not read the verified domains", "error", err)
		}
	}
	return len(due) == recheckBatch
}

// recheck looks at one verified domain again. Found: it is not missing, and is
// looked at again in a day. Not found, or a wrong value: missing since the
// first such check, and lapsed once that is LapseAfter ago. A DNS error counts
// for neither, and is tried again in an hour. It reports whether it lapsed.
func (s *Service) recheck(ctx context.Context, r store.DomainRow) (bool, error) {
	q := s.opt.Store.Q()
	now := s.opt.Now()
	if !s.on(ctx, r.TenantID) {
		return false, store.PostponeDomainCheck(ctx, q, r, now.Add(RecheckEvery))
	}
	if c, ok, err := s.outboundCost(ctx, r.TenantID); err != nil {
		return false, err
	} else if ok {
		if c.Limit <= 0 {
			return false, store.PostponeDomainCheck(ctx, q, r, now.Add(PostponeOutbound))
		}
		if _, took := s.minute.TakeAll(c); !took {
			return false, store.PostponeDomainCheck(ctx, q, r, now.Add(PostponeOutbound))
		}
	}
	c := s.ask(ctx, r, ByDaily)
	missing := r.MissingSince
	next := c.At.Add(RecheckEvery)
	switch c.Result {
	case Found:
		missing = time.Time{}
	case Failed:
		next = c.At.Add(RetryAfterError)
	default:
		if missing.IsZero() {
			missing = c.At
			s.audit(ctx, r, notify.DomainMissing, map[string]any{"domain": r.Domain, "check": c}, c.At)
		}
		if c.At.Sub(missing) >= LapseAfter {
			ok, err := store.LapseDomain(ctx, q, r, c.JSON(), c.At)
			if err != nil || !ok {
				return false, err
			}
			s.lapsed(ctx, r, missing, c)
			return true, nil
		}
	}
	_, err := store.RecheckDomain(ctx, q, r, c.JSON(), c.At, missing, next)
	return false, err
}

// audit writes a row of the recheck's own (no request: no actor, no address).
func (s *Service) audit(ctx context.Context, r store.DomainRow, ev notify.Event, detail map[string]any, at time.Time) {
	err := notify.WithAudit(ctx, s.opt.Store.Q(), ev, notify.Entry{Tenant: r.TenantID, Detail: detail, Now: at})
	if err != nil {
		s.log.Error("could not audit a domain's check", "kind", ev.Audit, "error", err)
	}
}

// lapsed tells the tenant's administrators that the claim lapsed, with its
// audit row, in one transaction.
func (s *Service) lapsed(ctx context.Context, r store.DomainRow, missing time.Time, c Check) {
	q := s.opt.Store.Q()
	admins, _, err := store.TenantAdmins(ctx, q, store.InTenant(r.TenantID), 200)
	if err != nil {
		s.log.Error("could not find whom to tell of a lapsed domain", "error", err)
	}
	to := make([]string, 0, len(admins))
	for _, a := range admins {
		if !a.Disabled {
			to = append(to, a.ID)
		}
	}
	sort.Strings(to)
	err = notify.WithAudit(ctx, q, notify.DomainLapsed, notify.Entry{Tenant: r.TenantID, Now: c.At,
		Detail:  map[string]any{"domain": r.Domain, "missingSince": missing.UTC().Format(time.RFC3339), "check": c},
		To:      to,
		Ref:     r.Domain,
		Payload: map[string]any{"domain": Display(r.Domain), "missingSince": missing.UTC().Format(time.RFC3339)}})
	if err != nil {
		s.log.Error("could not record a lapsed domain", "domain", r.Domain, "error", err)
	}
}
