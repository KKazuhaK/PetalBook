package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Provider is one host's OAuth application as a connections.Provider.
type Provider struct {
	m  *Manager
	d  driver
	c  *client
	id string

	mu       sync.Mutex
	row      store.ConnProviderRow // guarded by mu
	secret   string                // the client secret, in memory only (guarded by mu)
	tokens   map[string]cachedUser // by PetalBook user id
	renewing map[string]*renewal
	gens     map[string]uint64 // see tokenGen
	genAll   uint64
	attempts *attemptStore
	onChange func(userID string)
	// partialBusy is set while the host is asked whether it can filter a fetch.
	partialBusy bool
}

var (
	_ connections.Provider     = (*Provider)(nil)
	_ connections.Administered = (*Provider)(nil)
	_ connections.GitUser      = (*Provider)(nil)
	_ connections.Connected    = (*Provider)(nil)
	_ connections.Searcher     = (*Provider)(nil)
)

func (p *Provider) errf(status int, code, format string, a ...any) *connections.Error {
	return connections.Errorf(p.id, status, code, format, a...)
}

func (p *Provider) notConfigured() *connections.Error {
	return p.errf(http.StatusConflict, connections.CodeNotConfigured, "this provider is not set up, or its host did not pass the last check (an administrator looks at it in the administration)")
}

func (p *Provider) notConnected() *connections.Error {
	return p.errf(http.StatusConflict, connections.CodeNotConnected, "connect your account on %s first", p.name())
}

// snap is the provider's row and secret as they are now.
func (p *Provider) snap() (store.ConnProviderRow, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.row, p.secret
}

func (p *Provider) name() string { r, _ := p.snap(); return r.Name }

// configured: the provider passed its last check and has its secret.
func (p *Provider) configured() bool {
	r, s := p.snap()
	return r.State == stateOK && s != ""
}

// need is the provider's configuration, or the "not configured" answer.
func (p *Provider) need() (store.ConnProviderRow, string, error) {
	r, s := p.snap()
	if r.State != stateOK || s == "" {
		return r, s, p.notConfigured()
	}
	return r, s, nil
}

// userAuth: people can connect their accounts (the server has a public URL).
func (p *Provider) userAuth() bool { return p.configured() && p.m.opt.PublicURL != "" }

// CallbackURL is where the host sends people back to; "" without a public URL.
func (p *Provider) CallbackURL() string { return p.m.callbackURL(p.id) }

// GitUsername is the user name Git is given with the token.
func (p *Provider) GitUsername() string { return p.d.gitUser() }

// Descriptor says what this provider is.
func (p *Provider) Descriptor() connections.Descriptor {
	r, _ := p.snap()
	d := connections.Descriptor{ID: p.id, Type: r.Type, Host: r.Host, Name: r.Name, Owner: "site",
		Capabilities: connections.Capabilities{CreateRepo: true, ConnectAccount: true, StorageTarget: true, PartialSync: r.PartialSync}}
	if r.Owner != store.ConnProviderSite {
		d.Owner, d.TenantID = "tenant", r.Owner
	}
	return d
}

// Info is the state of the provider and of the caller's connection.
func (p *Provider) Info(ctx context.Context, userID string) (*connections.Info, error) {
	in := &connections.Info{Configured: p.configured(), UserAuthorization: p.userAuth()}
	var err error
	if in.Connection, err = p.connection(ctx, userID); err != nil {
		return nil, err
	}
	if in.Leftovers, err = p.leftovers(ctx, userID); err != nil {
		return nil, err
	}
	return in, nil
}

// Login is the caller's account name on the host, "" when not connected.
func (p *Provider) Login(ctx context.Context, userID string) string {
	a, err := store.GetProviderAuthorization(ctx, p.m.opt.Store.Q(), p.id, userID)
	if err != nil {
		return ""
	}
	return a.Login
}

func (p *Provider) connection(ctx context.Context, userID string) (*connections.Connection, error) {
	db := p.m.opt.Store.Q()
	a, err := store.GetProviderAuthorization(ctx, db, p.id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n, err := store.CountUserProviderSpaces(ctx, db, p.id, userID)
	if err != nil {
		return nil, err
	}
	return &connections.Connection{Login: a.Login, AccountID: a.AccountID, State: a.State, Connected: a.Connected, RefreshExpires: a.RefreshExpires, Spaces: n}, nil
}

func (p *Provider) leftovers(ctx context.Context, userID string) ([]connections.Leftover, error) {
	rows, err := store.ListProviderLeftovers(ctx, p.m.opt.Store.Q(), p.id, userID)
	if err != nil {
		return nil, err
	}
	out := make([]connections.Leftover, len(rows))
	for i, r := range rows {
		out[i] = connections.Leftover{RepoID: r.RepoID, FullName: r.FullName, HTMLURL: r.HTMLURL, SpaceName: r.SpaceName, Created: r.Created}
	}
	return out, nil
}

const leftoverTimeout = 10 * time.Second

// RecordLeftover remembers, for the user, a repository that was made for a
// space that then could not be made. It is written even when ctx has run out.
func (p *Provider) RecordLeftover(ctx context.Context, userID string, repo *connections.Repo, spaceName string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leftoverTimeout)
	defer cancel()
	return store.PutProviderLeftover(ctx, p.m.opt.Store.Q(), store.ProviderLeftover{Provider: p.id, UserID: userID, RepoID: repo.ID, FullName: repo.FullName,
		HTMLURL: repo.HTMLURL, SpaceName: spaceName, Created: p.m.opt.Now()})
}

// DismissLeftover forgets the user's reminder of a repository; there being none is fine.
func (p *Provider) DismissLeftover(ctx context.Context, userID string, repoID int64) error {
	_, err := store.DeleteProviderLeftover(ctx, p.m.opt.Store.Q(), p.id, userID, repoID)
	return err
}

// OnChange registers what is told when something that spaces wait on is fixed.
func (p *Provider) OnChange(f func(userID string)) {
	p.mu.Lock()
	p.onChange = f
	p.mu.Unlock()
}

func (p *Provider) changed(userID string) {
	p.mu.Lock()
	f := p.onChange
	p.mu.Unlock()
	if f != nil {
		f(userID)
	}
}

// visibleTo refuses a person outside the tenant a tenant's own provider
// belongs to: it is as good as not there for them.
func (p *Provider) visibleTo(ctx context.Context, userID string) error {
	r, _ := p.snap()
	if r.Owner == store.ConnProviderSite {
		return nil
	}
	t, err := store.UserTenant(ctx, p.m.opt.Store.Q(), userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err != nil || t != r.Owner {
		return p.errf(http.StatusNotFound, connections.CodeNotFound, "no such provider")
	}
	return nil
}

// wrap turns an error of the host into the answer for the caller: rate limits
// and an unreachable host; anything else is returned as it came.
func (p *Provider) wrap(err error) error {
	if err == nil {
		return nil
	}
	var he *connections.Error
	if errors.As(err, &he) {
		return err
	}
	name := p.name()
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.RateLimited:
			return &connections.Error{Provider: p.id, Code: connections.CodeRateLimited, Status: http.StatusTooManyRequests, Msg: name + " is limiting this server; try again in a moment",
				RetryAfter: se.RetryAfter(p.m.opt.Now()), Err: err}
		case se.Status >= 500:
			return &connections.Error{Provider: p.id, Code: connections.CodeUnavailable, Status: http.StatusServiceUnavailable, Msg: name + " cannot be reached right now", Err: err}
		}
		return err
	}
	var oe *OAuthError
	if errors.As(err, &oe) || errors.Is(err, context.Canceled) {
		return err
	}
	return &connections.Error{Provider: p.id, Code: connections.CodeUnavailable, Status: http.StatusServiceUnavailable, Msg: name + " cannot be reached right now", Err: err}
}

// Check reads the provider's configuration from the host again.
func (p *Provider) Check(ctx context.Context) error {
	_, err := p.AdminCheck(ctx)
	return err
}

// --- what an administrator sees -----------------------------------------------------

type adminView struct {
	ID                string   `json:"id"`
	Type              string   `json:"type"`
	Host              string   `json:"host"`
	Name              string   `json:"name"`
	Owner             any      `json:"owner"`
	Configured        bool     `json:"configured"`
	ClientID          string   `json:"clientId"`
	HasSecret         bool     `json:"hasSecret"`
	CallbackURL       string   `json:"callbackUrl"`
	Scopes            []string `json:"scopes"`
	UserAuthorization bool     `json:"userAuthorization"`
	Version           string   `json:"version"`
	Checked           string   `json:"checked,omitempty"`
	PartialSync       string   `json:"partialSync"`
	Connections       int      `json:"connections"`
	Spaces            int      `json:"spaces"`
	Updated           string   `json:"updated"`
}

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// AdminView is the provider's settings as an administrator sees them. The
// client secret is never in it.
func (p *Provider) AdminView(ctx context.Context) (any, error) {
	r, s := p.snap()
	n, sp, err := p.AdminCounts(ctx)
	if err != nil {
		return nil, err
	}
	v := adminView{ID: p.id, Type: r.Type, Host: r.Host, Name: r.Name, Owner: "site", Configured: r.State == stateOK && s != "",
		ClientID: r.ClientID, HasSecret: s != "", CallbackURL: p.CallbackURL(), Scopes: p.d.scopes(), UserAuthorization: p.userAuth(),
		Version: r.PlatformVersion, PartialSync: r.PartialSync, Connections: n, Spaces: sp, Updated: rfc3339(r.Updated)}
	if v.Scopes == nil {
		v.Scopes = []string{}
	}
	if r.Owner != store.ConnProviderSite {
		v.Owner = map[string]string{"tenant": r.Owner}
	}
	if !r.Checked.IsZero() {
		v.Checked = rfc3339(r.Checked)
	}
	return v, nil
}

// AdminCounts is how many people are connected and spaces sync through the provider.
func (p *Provider) AdminCounts(ctx context.Context) (int, int, error) {
	db := p.m.opt.Store.Q()
	n, err := store.CountProviderAuthorizations(ctx, db, p.id)
	if err != nil {
		return 0, 0, err
	}
	sp, err := store.CountProviderSpaces(ctx, db, p.id)
	return n, sp, err
}

// AdminSave changes what an administrator may change of the provider: its name,
// and its client id and secret (the host is checked with them before they are
// kept). Its id, type and host are fixed.
func (p *Provider) AdminSave(ctx context.Context, body []byte) (any, error) {
	var req struct {
		ID           *string `json:"id"`
		Type         *string `json:"type"`
		Host         *string `json:"host"`
		Name         *string `json:"name"`
		ClientID     *string `json:"clientId"`
		ClientSecret *string `json:"clientSecret"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "the body is not what this takes: %s", err.Error())
	}
	row, secret := p.snap()
	if (req.ID != nil && *req.ID != p.id) || (req.Type != nil && *req.Type != row.Type) || (req.Host != nil && p.sameHost(*req.Host) != nil) {
		return nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "a provider's id, type and host cannot be changed: make a new provider and remove this one")
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if !validName(n) {
			return nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "name is at most 80 characters, without control characters")
		}
		row.Name = n
	}
	newClient := false
	if req.ClientID != nil {
		row.ClientID, newClient = *req.ClientID, true
	}
	if req.ClientSecret != nil {
		secret, newClient = *req.ClientSecret, true
	}
	if !validClient(row.ClientID, secret) {
		return nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "clientId and clientSecret are required")
	}
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	version, err := p.m.checkHost(ctx, p.d, p.c, row.ClientID, secret, p.CallbackURL())
	if err != nil {
		return nil, withProvider(err, p.id)
	}
	enc, err := p.m.opt.Secrets.Seal(p.id, secretPurpose(row.Owner), []byte(secret))
	if err != nil {
		return nil, err
	}
	row.SecretEnc, row.State, row.PlatformVersion = enc, stateOK, version
	row.Checked, row.Updated, row.UpdatedBy = p.m.opt.Now(), p.m.opt.Now(), connections.ActorOf(ctx)
	if err := store.UpdateConnProviderRow(ctx, p.m.opt.Store.Q(), row); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.row.Name, p.row.ClientID, p.row.SecretEnc, p.row.State, p.row.PlatformVersion = row.Name, row.ClientID, row.SecretEnc, row.State, row.PlatformVersion
	p.row.Checked, p.row.Updated, p.row.UpdatedBy = row.Checked, row.Updated, row.UpdatedBy
	p.secret = secret
	p.mu.Unlock()
	if newClient {
		p.dropAllTokens() // tokens of another application are not this one's to use
	}
	p.changed("")
	return p.AdminView(ctx)
}

func (p *Provider) sameHost(raw string) error {
	hs, err := p.m.parseHost(raw)
	r, _ := p.snap()
	if err != nil || hs.host != r.Host || hs.scheme != r.Scheme {
		return errors.New("another host")
	}
	return nil
}

// AdminCheck reads the provider's configuration from the host again: its
// version, and that it still accepts the client. A provider whose host fails
// the check is not set up (everything of it answers 409 provider_not_configured)
// until a check passes.
func (p *Provider) AdminCheck(ctx context.Context) (any, error) {
	row, secret := p.snap()
	if secret == "" {
		return nil, p.notConfigured()
	}
	version, err := p.m.checkHost(ctx, p.d, p.c, row.ClientID, secret, p.CallbackURL())
	now := p.m.opt.Now()
	if err != nil {
		var he *connections.Error
		if errors.As(err, &he) && he.Reason != ReasonUnreachable { // a host that cannot be reached right now is not a verdict
			p.setState(ctx, stateError, row.PlatformVersion, now)
		}
		return nil, withProvider(err, p.id)
	}
	p.setState(ctx, stateOK, version, now)
	return p.AdminView(ctx)
}

func (p *Provider) setState(ctx context.Context, state, version string, now time.Time) {
	p.mu.Lock()
	p.row.State, p.row.PlatformVersion, p.row.Checked = state, version, now
	row := p.row
	p.mu.Unlock()
	if err := store.UpdateConnProviderRow(ctx, p.m.opt.Store.Q(), row); err != nil {
		p.m.log.Error("could not store the result of a provider's check", "provider", p.id, "error", err)
	}
}

// AdminDelete removes the provider; it must not be in use.
func (p *Provider) AdminDelete(ctx context.Context) error {
	p.m.mu.Lock()
	defer p.m.mu.Unlock()
	n, sp, err := p.AdminCounts(ctx)
	if err != nil {
		return err
	}
	if n > 0 || sp > 0 {
		e := p.errf(http.StatusConflict, connections.CodeInUse, "%d people are connected and %d spaces sync through %s: disconnect them and move the spaces first", n, sp, p.name())
		e.Counts = map[string]int{"connections": n, "spaces": sp}
		return e
	}
	if err := store.DeleteConnProviderRow(ctx, p.m.opt.Store.Q(), p.id); err != nil {
		return err
	}
	delete(p.m.provs, p.id)
	p.m.opt.Registry.Remove(p.id)
	p.dropAllTokens()
	return nil
}

// --- addresses -------------------------------------------------------------------------

func (p *Provider) webBase() string { return p.c.base }

// RepoURL is the https address of a repository as Git is given it.
func (p *Provider) RepoURL(owner, name string) string {
	return p.webBase() + "/" + owner + "/" + name + ".git"
}

// ValidRepoName checks a repository name for this platform.
func (p *Provider) ValidRepoName(name string) bool { return p.d.validName(name) }

// ParseRepoURL reads the address of a repository on this host: https, ssh and
// the scp form (git@host:owner/name.git), with or without ".git". On GitLab the
// owner may be a group with subgroups ("group/sub").
func (p *Provider) ParseRepoURL(raw string) (owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	r, _ := p.snap()
	host := strings.ToLower(r.Host)
	var path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || strings.ToLower(u.Host) != host {
			return "", "", false
		}
		switch u.Scheme {
		case "https":
		case "http":
			if r.Scheme != "http" {
				return "", "", false
			}
		case "ssh":
			if u.User != nil && u.User.Username() != "git" {
				return "", "", false
			}
		default:
			return "", "", false
		}
		if u.Scheme != "ssh" && u.User != nil {
			return "", "", false
		}
		path = strings.TrimPrefix(u.Path, "/")
	case strings.HasPrefix(raw, "git@") || strings.Contains(raw, ":"):
		h, rest, found := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !found || strings.ToLower(h) != strings.ToLower(strings.Split(host, ":")[0]) {
			return "", "", false
		}
		path = rest
	default:
		return "", "", false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	o, n := splitFull(path)
	if o == "" || strings.Count(o, "/")+1 > p.d.maxOwnerDepth() || !p.d.validOwner(o) || !nameRe.MatchString(n) || n == "." || n == ".." {
		return "", "", false
	}
	return o, n, true
}

// MarkError puts the provider in the state a failed check leaves it in (tests).
func (p *Provider) MarkError() {
	p.mu.Lock()
	p.row.State = stateError
	p.mu.Unlock()
}
