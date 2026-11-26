package connections

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/store"
)

// The one token service (consolidation 5b): it keeps the sealed tokens of the
// repository links of every provider that has them (linked_accounts), hands a
// person's access token out (cached, renewed on the way when it has less than
// five minutes left) and renews the refresh tokens in the background, in one
// loop for all kinds. What differs between the platforms is the provider's
// RefreshTokens (a Refresher); everything else is here.
//
// The loop is driven by the database: it sleeps until the soonest renew_at of
// any link, or until it is woken (a link was stored), and then renews what is
// due, a few links at a time. Nothing polls.

// Errors of using a person's tokens.
var (
	// ErrNoAuthorization: the person has not connected an account here.
	ErrNoAuthorization = errors.New("the user has not connected an account")
	// ErrAuthorizationExpired: the refresh token ran out or was revoked; the
	// person must authorise again.
	ErrAuthorizationExpired = errors.New("the authorisation expired or was revoked")
	// ErrRevoked is what a Refresher answers when the host says the grant is
	// dead (revoked, ended, never valid): the link needs the person's attention,
	// there is no use in trying again.
	ErrRevoked = errors.New("the grant was revoked or has ended")
)

// Pair is a pair of tokens as a host hands it out.
type Pair struct {
	Access, Refresh               string
	AccessExpires, RefreshExpires time.Time // zero: it does not end
}

// Refresher is a provider that renews a person's tokens. RefreshTokens trades
// a refresh token for a new pair: ErrRevoked when the host refuses the grant for
// good, any other error when it could not be asked or answered otherwise (the
// token service tries again later; an on-demand caller is told).
type Refresher interface {
	RefreshTokens(ctx context.Context, refresh string) (*Pair, error)
}

const (
	purposeAccess  = "access"
	purposeRefresh = "refresh"
	renewBefore    = 5 * time.Minute
	maxCached      = 10_000
)

// RenewWindow is how close to its end a refresh token must be for the loop to
// renew it, by default.
const RenewWindow = 30 * 24 * time.Hour

// TokenOptions configures Tokens.
type TokenOptions struct {
	Store    *store.Store
	Secrets  *secret.Box
	Registry *Registry
	Now      func() time.Time
	Logger   *slog.Logger
	// After is the loop's clock: a channel that fires after d and a function that
	// stops it (default time.NewTimer; tests).
	After func(d time.Duration) (<-chan time.Time, func())
	// RenewWindow: see the constant (default 30 days).
	RenewWindow time.Duration
	// Concurrency is how many links the loop renews at once (default 4).
	Concurrency int
	// BackoffBase and BackoffMax bound the wait before a failed renewal is tried
	// again: the base doubles each time (defaults 5 minutes and 6 hours).
	BackoffBase, BackoffMax time.Duration
}

// Tokens is the service.
type Tokens struct {
	opt  TokenOptions
	log  *slog.Logger
	wake chan struct{}

	mu       sync.Mutex
	cache    map[string]cachedToken // by Key(provider, person)
	renewing map[string]*renewal
	gens     map[string]uint64 // by key, see tokenGen
	provGens map[string]uint64 // by provider
	allGen   uint64
	backoff  map[string]backoffEntry // by link id
}

type cachedToken struct {
	access  string
	expires time.Time // zero: none
}

type renewal struct {
	done   chan struct{}
	access string
	err    error
}

type backoffEntry struct {
	attempts int
	retryAt  time.Time
}

// NewTokens returns a service. It renews nothing until Run is called.
func NewTokens(opt TokenOptions) *Tokens {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	if opt.After == nil {
		opt.After = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		}
	}
	if opt.RenewWindow <= 0 {
		opt.RenewWindow = RenewWindow
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.BackoffBase <= 0 {
		opt.BackoffBase = 5 * time.Minute
	}
	if opt.BackoffMax <= 0 {
		opt.BackoffMax = 6 * time.Hour
	}
	return &Tokens{opt: opt, log: opt.Logger, wake: make(chan struct{}, 1), cache: map[string]cachedToken{}, renewing: map[string]*renewal{},
		gens: map[string]uint64{}, provGens: map[string]uint64{}, backoff: map[string]backoffEntry{}}
}

// Key is what a link's cache entry and its sealing are bound to: the provider
// and the person, which are what the link is found by without a read.
func Key(providerID, userID string) string { return providerID + ":" + userID }

// RenewAt is when the loop renews a refresh token that ends at expires, made at
// now: in the last tenth of its life, and within window of its end, whichever
// is later. A host whose tokens live 30 days is not asked about them every
// day. Zero when it does not end.
func RenewAt(now, expires time.Time, window time.Duration) time.Time {
	if expires.IsZero() {
		return time.Time{}
	}
	at := expires.Add(-expires.Sub(now) / 10)
	if w := expires.Add(-window); w.After(at) {
		at = w
	}
	return at
}

// Wake tells the loop that a link was stored or changed, so what it is waiting
// for may be sooner.
func (t *Tokens) Wake() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// --- storing and removing -----------------------------------------------------------------

// Account is the person's account at a host, as a connection stores it.
type Account struct {
	ProviderID, UserID string
	Subject, Login     string
	Admin              bool
}

// Put stores a person's link at a provider with the pair a host just gave: the
// pair is sealed, the link replaces the person's earlier one at the provider
// (its tokens, its account), the cache forgets the old pair and the loop is
// woken. store.ErrExists when another person has the account.
func (t *Tokens) Put(ctx context.Context, a Account, p Pair) error {
	now := t.opt.Now()
	l := store.RepoLink{ID: page.NewID(now), ProviderID: a.ProviderID, UserID: a.UserID, Subject: a.Subject, Login: a.Login, Admin: a.Admin,
		AccessExpires: p.AccessExpires, RefreshExpires: p.RefreshExpires, RenewAt: RenewAt(now, p.RefreshExpires, t.opt.RenewWindow),
		State: store.LinkOK, Connected: now, Updated: now}
	var err error
	if l.AccessEnc, err = t.opt.Secrets.Seal(Key(a.ProviderID, a.UserID), purposeAccess, []byte(p.Access)); err != nil {
		return err
	}
	if p.Refresh != "" {
		if l.RefreshEnc, err = t.opt.Secrets.Seal(Key(a.ProviderID, a.UserID), purposeRefresh, []byte(p.Refresh)); err != nil {
			return err
		}
	} else {
		l.RenewAt = time.Time{}
	}
	if _, err = store.PutRepoLink(ctx, t.opt.Store.Q(), l); err != nil {
		return err
	}
	t.bump(a.ProviderID, a.UserID)
	t.Wake()
	return nil
}

// Remove deletes a person's link at a provider; the tokens are gone from the
// database and the cache, and nothing renews them.
func (t *Tokens) Remove(ctx context.Context, providerID, userID string) error {
	if err := store.DeleteRepoLink(ctx, t.opt.Store.Q(), providerID, userID); err != nil {
		return err
	}
	t.bump(providerID, userID)
	return nil
}

// OpenAccess is the access token a stored link holds, for a revocation.
func (t *Tokens) OpenAccess(l *store.RepoLink) (string, error) {
	b, err := t.opt.Secrets.Open(Key(l.ProviderID, l.UserID), purposeAccess, l.AccessEnc)
	return string(b), err
}

// DropProvider forgets every cached token of a provider (its application
// changed, or it went away); what a renewal read before is not cached.
func (t *Tokens) DropProvider(providerID string) {
	t.mu.Lock()
	t.provGens[providerID]++
	for k := range t.cache {
		if len(k) > len(providerID) && k[:len(providerID)+1] == providerID+":" {
			delete(t.cache, k)
		}
	}
	t.mu.Unlock()
}

// --- the cache --------------------------------------------------------------------------

// tokenGen is how many times a person's link was replaced or removed in this
// process (link), and a provider's cache dropped (prov). A renewal notes it
// before it reads the database and caches what it read only if it is unchanged.
type tokenGen struct{ prov, link uint64 }

func (t *Tokens) generation(providerID, userID string) tokenGen {
	t.mu.Lock()
	defer t.mu.Unlock()
	return tokenGen{prov: t.provGens[providerID], link: t.gens[Key(providerID, userID)]}
}

// bump says the link was replaced or removed (after the database was written):
// the cached token goes, and a renewal that read before it does not cache.
func (t *Tokens) bump(providerID, userID string) {
	k := Key(providerID, userID)
	t.mu.Lock()
	t.gens[k]++
	delete(t.cache, k)
	t.mu.Unlock()
}

func (t *Tokens) cacheToken(providerID, userID string, gen tokenGen, access string, expires time.Time) {
	k := Key(providerID, userID)
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != (tokenGen{prov: t.provGens[providerID], link: t.gens[k]}) {
		return // the link changed after this was read
	}
	if len(t.cache) >= maxCached {
		for c := range t.cache {
			delete(t.cache, c)
			break
		}
	}
	t.cache[k] = cachedToken{access: access, expires: expires}
}

func (t *Tokens) dropToken(providerID, userID string) {
	t.mu.Lock()
	delete(t.cache, Key(providerID, userID))
	t.mu.Unlock()
}

// --- using tokens -----------------------------------------------------------------------

// Token returns the person's access token at the provider, renewing it first
// when it has less than five minutes left. ErrNoAuthorization or
// ErrAuthorizationExpired when there is nothing to use; the refresher's error
// (an *Error) when the host cannot be asked.
func (t *Tokens) Token(ctx context.Context, providerID, userID string) (string, error) {
	now := t.opt.Now()
	k := Key(providerID, userID)
	t.mu.Lock()
	if c, ok := t.cache[k]; ok && (c.expires.IsZero() || now.Add(renewBefore).Before(c.expires)) {
		t.mu.Unlock()
		return c.access, nil
	}
	t.mu.Unlock()
	return t.renew(ctx, providerID, userID, false, 0)
}

// renew renews one link's tokens, one renewal at a time per link; a caller that
// arrives meanwhile waits for it. force renews even if the token is still good.
func (t *Tokens) renew(ctx context.Context, providerID, userID string, force bool, ifVersion int64) (string, error) {
	k := Key(providerID, userID)
	t.mu.Lock()
	if r, ok := t.renewing[k]; ok {
		t.mu.Unlock()
		select {
		case <-r.done:
			return r.access, r.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	r := &renewal{done: make(chan struct{})}
	t.renewing[k] = r
	t.mu.Unlock()
	// Once the host has handed out a new refresh token the old one is dead: the
	// new pair must reach the database even if this request goes away.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	r.access, r.err = t.doRenew(wctx, providerID, userID, force, ifVersion)
	cancel()
	t.mu.Lock()
	delete(t.renewing, k)
	t.mu.Unlock()
	close(r.done)
	return r.access, r.err
}

func (t *Tokens) doRenew(ctx context.Context, providerID, userID string, force bool, ifVersion int64) (string, error) {
	db := t.opt.Store.Q()
	box := t.opt.Secrets
	bind := Key(providerID, userID)
	for pass := 0; pass < 3; pass++ {
		gen := t.generation(providerID, userID)
		row, err := store.GetRepoLink(ctx, db, providerID, userID)
		if errors.Is(err, store.ErrNotFound) {
			t.dropToken(providerID, userID)
			return "", ErrNoAuthorization
		}
		if err != nil {
			return "", err
		}
		if row.State != store.LinkOK {
			t.dropToken(providerID, userID)
			return "", ErrAuthorizationExpired
		}
		if ifVersion > 0 && row.Version != ifVersion {
			force = false // someone renewed it since it was found due: what is there now is good
		}
		now := t.opt.Now()
		access, err := box.Open(bind, purposeAccess, row.AccessEnc)
		if err != nil {
			return "", err
		}
		good := row.AccessExpires.IsZero() || now.Add(renewBefore).Before(row.AccessExpires)
		if good && !force {
			t.cacheToken(providerID, userID, gen, string(access), row.AccessExpires)
			return string(access), nil
		}
		if row.RefreshEnc == "" {
			if row.AccessExpires.IsZero() || now.Before(row.AccessExpires) {
				t.cacheToken(providerID, userID, gen, string(access), row.AccessExpires) // no refresh token: use it to the end
				return string(access), nil
			}
			t.expire(ctx, row)
			return "", ErrAuthorizationExpired
		}
		prov, ok := t.opt.Registry.Get(providerID)
		rf, isRefresher := prov.(Refresher)
		if !ok || !isRefresher {
			return "", Errorf(providerID, http.StatusConflict, CodeNotConfigured, "This provider is not set up.")
		}
		refresh, err := box.Open(bind, purposeRefresh, row.RefreshEnc)
		if err != nil {
			return "", err
		}
		pair, err := rf.RefreshTokens(ctx, string(refresh))
		if err != nil {
			if errors.Is(err, ErrRevoked) {
				t.log.Info("an authorisation expired or was revoked", "provider", providerID, "user", userID)
				t.expire(ctx, row)
				return "", ErrAuthorizationExpired
			}
			return "", err
		}
		if pair.Refresh == "" { // a host that does not rotate: the refresh token stays
			pair.Refresh, pair.RefreshExpires = string(refresh), row.RefreshExpires
		}
		accessEnc, err := box.Seal(bind, purposeAccess, []byte(pair.Access))
		if err != nil {
			return "", err
		}
		refreshEnc, err := box.Seal(bind, purposeRefresh, []byte(pair.Refresh))
		if err != nil {
			return "", err
		}
		// The new pair is saved before it is used, and replaces only the pair it
		// was made from.
		var replaced bool
		for try := 0; try < 3; try++ {
			if replaced, err = store.ReplaceLinkTokens(ctx, db, row.ID, row.Version, accessEnc, refreshEnc, pair.AccessExpires, pair.RefreshExpires,
				RenewAt(t.opt.Now(), pair.RefreshExpires, t.opt.RenewWindow), t.opt.Now()); err == nil {
				break
			}
			time.Sleep(time.Duration(try+1) * 100 * time.Millisecond)
		}
		if err != nil {
			// The old refresh token is dead and the new one is not stored: this
			// link cannot go on.
			t.log.Error("could not store a renewed token", "provider", providerID, "user", userID, "error", err)
			t.expire(ctx, row)
			return "", ErrAuthorizationExpired
		}
		if !replaced {
			continue // reconnected or renewed elsewhere meanwhile: use what is there now
		}
		t.cacheToken(providerID, userID, gen, pair.Access, pair.AccessExpires)
		return pair.Access, nil
	}
	return "", Errorf(providerID, http.StatusServiceUnavailable, CodeUnavailable, "The authorisation keeps changing; try again.")
}

// expire marks a link as needing the person to authorise again, and tells the
// person, once: the write that moves the link from ok is the one that notifies,
// so however many callers give up on it together, one notification is made, in
// the same transaction.
func (t *Tokens) expire(ctx context.Context, l *store.RepoLink) {
	now := t.opt.Now()
	err := store.Atomically(ctx, t.opt.Store.Q(), func(q store.Q) error {
		ok, err := store.ExpireLink(ctx, q, l.ID, l.Version, now)
		if err != nil || !ok {
			return err
		}
		name := l.ProviderID
		if p, found := t.opt.Registry.Get(l.ProviderID); found {
			name = p.Descriptor().Name
		}
		return store.NotifyUser(ctx, q, l.UserID, store.NotifyLinkReauth, l.ProviderID,
			map[string]string{"provider": l.ProviderID, "name": name, "login": l.Login}, now)
	})
	if err != nil {
		t.log.Error("could not mark an authorisation expired", "provider", l.ProviderID, "user", l.UserID, "error", err)
	}
	t.dropToken(l.ProviderID, l.UserID)
}

// --- the loop ---------------------------------------------------------------------------

// RenewDue renews, a few at a time, the links whose renewal time has come, and
// returns how many it renewed. A link whose host refuses the grant for good is
// marked as needing the person to authorise again (and the person told); one
// whose host could not be asked is tried again after a wait that doubles with
// each failure, until its refresh token ends.
func (t *Tokens) RenewDue(ctx context.Context) int {
	now := t.opt.Now()
	var cur store.DueLinkCursor
	var renewed int
	var mu sync.Mutex
	sem := make(chan struct{}, t.opt.Concurrency)
	for {
		due, err := store.DueLinkRenewals(ctx, t.opt.Store.Q(), now, cur, 50)
		if err != nil {
			t.log.Error("could not look for authorisations to renew", "error", err)
			break
		}
		var wg sync.WaitGroup
		for _, l := range due {
			cur = store.DueLinkCursor{RenewAt: l.RenewAt, ID: l.ID}
			if ctx.Err() != nil {
				break
			}
			if !t.mayTry(l.ID, now) {
				continue
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				if t.renewOne(ctx, l, now) {
					mu.Lock()
					renewed++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(due) < 50 || ctx.Err() != nil {
			break
		}
	}
	t.pruneBackoff(now)
	return renewed
}

func (t *Tokens) renewOne(ctx context.Context, l store.RepoLink, now time.Time) bool {
	_, err := t.renew(ctx, l.ProviderID, l.UserID, true, l.Version)
	switch {
	case err == nil:
		t.clearBackoff(l.ID)
		return true
	case errors.Is(err, ErrAuthorizationExpired), errors.Is(err, ErrNoAuthorization):
		t.clearBackoff(l.ID) // ended, or unlinked meanwhile: nothing to try again
		return false
	case ctx.Err() != nil:
		return false
	}
	if !l.RefreshExpires.IsZero() && !now.Before(l.RefreshExpires) {
		// It ended while the host could not be asked: it is over.
		if cur, gerr := store.GetRepoLink(ctx, t.opt.Store.Q(), l.ProviderID, l.UserID); gerr == nil {
			t.expire(ctx, cur)
		}
		t.clearBackoff(l.ID)
		return false
	}
	at := t.failed(l.ID, now)
	t.log.Info("could not renew an authorisation; trying again later", "provider", l.ProviderID, "user", l.UserID, "retry", at, "reason", err.Error())
	return false
}

func (t *Tokens) mayTry(id string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.backoff[id]
	return !ok || !now.Before(b.retryAt)
}

func (t *Tokens) failed(id string, now time.Time) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.backoff[id]
	b.attempts++
	d := t.opt.BackoffBase
	for i := 1; i < b.attempts && d < t.opt.BackoffMax; i++ {
		d *= 2
	}
	if d > t.opt.BackoffMax {
		d = t.opt.BackoffMax
	}
	b.retryAt = now.Add(d)
	t.backoff[id] = b
	return b.retryAt
}

func (t *Tokens) clearBackoff(id string) {
	t.mu.Lock()
	delete(t.backoff, id)
	t.mu.Unlock()
}

// pruneBackoff forgets the waits that are over: the link was tried again (and
// has a new wait, or none) or is gone.
func (t *Tokens) pruneBackoff(now time.Time) {
	t.mu.Lock()
	for id, b := range t.backoff {
		if !now.Before(b.retryAt) {
			delete(t.backoff, id)
		}
	}
	t.mu.Unlock()
}

func (t *Tokens) earliestRetry() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var at time.Time
	for _, b := range t.backoff {
		if at.IsZero() || b.retryAt.Before(at) {
			at = b.retryAt
		}
	}
	return at, !at.IsZero()
}

// Run renews what is due, then sleeps until the soonest of the next renewal
// time in the database and the soonest retry of a failed one, or until woken,
// and does so again, until ctx ends.
func (t *Tokens) Run(ctx context.Context) {
	for {
		// A wake-up that came before this pass is answered by it: what it was
		// about is in the database already.
		select {
		case <-t.wake:
		default:
		}
		if n := t.RenewDue(ctx); n > 0 {
			t.log.Info("renewed authorisations", "count", n)
		}
		if ctx.Err() != nil {
			return
		}
		now := t.opt.Now()
		next, ok, err := store.NextLinkRenewal(ctx, t.opt.Store.Q(), now)
		if err != nil {
			t.log.Error("could not look for the next authorisation to renew", "error", err)
			next, ok = now.Add(t.opt.BackoffBase), true
		}
		if r, has := t.earliestRetry(); has && (!ok || r.Before(next)) {
			next, ok = r, true
		}
		var fire <-chan time.Time
		stop := func() {}
		if ok {
			fire, stop = t.opt.After(max(next.Sub(now), 0))
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-t.wake:
		case <-fire:
		}
		stop()
	}
}

// Renew renews the person's tokens now, even if the access token is still
// good (a host refused it: it was dropped there). The errors are Token's.
func (t *Tokens) Renew(ctx context.Context, providerID, userID string) (string, error) {
	return t.renew(ctx, providerID, userID, true, 0)
}

// Expire ends a link whose tokens cannot be used any more (only if it is still
// the one l was read as: another renewal or a reconnection since wins), and
// tells the person.
func (t *Tokens) Expire(ctx context.Context, l *store.RepoLink) { t.expire(ctx, l) }
