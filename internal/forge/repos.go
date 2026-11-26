package forge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/store"
)

// connected is the user's connection when it is usable, with its token; nil
// when there is none or it has expired.
func (p *Provider) connected(ctx context.Context, userID string) (*store.ProviderAuthorization, string, error) {
	row, err := store.GetProviderAuthorization(ctx, p.m.opt.Store.Q(), p.id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if row.State != store.GitHubOK {
		return row, "", nil
	}
	tok, err := p.UserToken(ctx, userID)
	switch {
	case errors.Is(err, ErrNoAuthorization), errors.Is(err, ErrAuthorizationExpired):
		return nil, "", nil
	case err != nil:
		return nil, "", err
	}
	return row, tok, nil
}

// userFailed turns an error of a call made with a user's token (after asUser
// has had its say) into an answer.
func (p *Provider) userFailed(err error) error {
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusUnauthorized && !se.RateLimited {
		return p.notConnected()
	}
	return p.wrap(err)
}

func isUnauthorized(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusUnauthorized && !se.RateLimited
}

// asUser makes a call with a person's token. A 401 does not by itself end the
// connection: the access token may be one the host dropped, or one a parallel
// request has just replaced. So the token is renewed (once, one renewal at a
// time) and the call made again, once. The connection ends only when the
// renewal itself is refused as revoked, and then only the very row the token
// came from (see afterUnauthorized).
func (p *Provider) asUser(ctx context.Context, userID, tok string, call func(tok string) error) error {
	err := call(tok)
	if !isUnauthorized(err) {
		return err
	}
	fresh, rerr := p.afterUnauthorized(ctx, userID, tok)
	if rerr != nil {
		return rerr
	}
	return call(fresh)
}

// afterUnauthorized is the token to try again with after the host refused tok.
// If the stored token is no longer tok, someone renewed meanwhile: the stored
// one is used and nothing is spent. Otherwise the stored pair is renewed; a
// refresh token the host refuses ends that version of the connection, and a
// token that has no refresh token to renew with is ended too, as it can do
// nothing more.
func (p *Provider) afterUnauthorized(ctx context.Context, userID, tok string) (string, error) {
	row, err := store.GetProviderAuthorization(ctx, p.m.opt.Store.Q(), p.id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return "", p.notConnected()
	}
	if err != nil {
		return "", err
	}
	if row.State != store.GitHubOK {
		return "", p.notConnected()
	}
	cur, err := p.m.opt.Secrets.Open(p.sealBinding(userID), purposeAccess, row.AccessEnc)
	if err != nil {
		return "", err
	}
	var fresh string
	switch {
	case string(cur) != tok:
		fresh, err = p.UserToken(ctx, userID)
	case row.RefreshEnc == "":
		p.expire(ctx, row) // only if the row is still the one the token came from
		return "", p.notConnected()
	default:
		fresh, err = p.renew(ctx, userID, true)
	}
	switch {
	case errors.Is(err, ErrNoAuthorization), errors.Is(err, ErrAuthorizationExpired):
		return "", p.notConnected()
	case err != nil:
		return "", p.wrap(err)
	}
	return fresh, nil
}

// ready is what every call made for a person needs: the provider is set up and
// theirs to use, and they are connected; it returns their token.
func (p *Provider) ready(ctx context.Context, userID string) (*store.ProviderAuthorization, string, error) {
	if _, _, err := p.need(); err != nil {
		return nil, "", err
	}
	if err := p.visibleTo(ctx, userID); err != nil {
		return nil, "", err
	}
	row, tok, err := p.connected(ctx, userID)
	if err != nil {
		return nil, "", p.wrap(err)
	}
	if tok == "" {
		return nil, "", p.notConnected()
	}
	return row, tok, nil
}

func (p *Provider) pageOf(cursor string) (int, error) {
	if cursor == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 1 || n > 100_000 {
		return 0, p.errf(http.StatusBadRequest, connections.CodeInvalid, "The cursor is not one this server made.")
	}
	return n, nil
}

// checkQ bounds a search text: it is passed on to the host.
func (p *Provider) checkQ(q string) error {
	if len(q) > 100 || strings.ContainsAny(q, "\x00\r\n") {
		return p.errf(http.StatusBadRequest, connections.CodeInvalid, "Q is at most 100 characters on one line.")
	}
	return nil
}

func nextCursor(next int) string {
	if next <= 0 {
		return ""
	}
	return strconv.Itoa(next)
}

// Owners lists the places the caller can create a repository in.
func (p *Provider) Owners(ctx context.Context, userID, cursor string) ([]connections.Owner, string, error) {
	return p.OwnersQ(ctx, userID, cursor, "")
}

// OwnersQ is Owners narrowed to the places whose name contains q: the caller's
// own namespace first (on the first page), then their groups or organisations.
func (p *Provider) OwnersQ(ctx context.Context, userID, cursor, q string) ([]connections.Owner, string, error) {
	row, tok, err := p.ready(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if err := p.checkQ(q); err != nil {
		return nil, "", err
	}
	page, err := p.pageOf(cursor)
	if err != nil {
		return nil, "", err
	}
	var out []connections.Owner
	if page == 1 && (q == "" || strings.Contains(strings.ToLower(row.Login), strings.ToLower(q))) {
		out = append(out, connections.Owner{Login: row.Login, Type: "User", Via: connections.ViaUser})
	}
	var list []ownerInfo
	var next int
	err = p.asUser(ctx, userID, tok, func(tok string) (err error) {
		list, next, err = p.d.owners(ctx, p.c, tok, &account{ID: row.AccountID, Login: row.Login}, page, q)
		return err
	})
	if err != nil {
		return nil, "", p.userFailed(err)
	}
	for _, o := range list {
		out = append(out, connections.Owner{Login: o.Login, Type: "Organization", Via: connections.ViaUser})
	}
	return out, nextCursor(next), nil
}

// Repos lists the repositories the caller can connect a space to: those their
// connection can push to, a page of the host's list (one request) at a time.
func (p *Provider) Repos(ctx context.Context, userID string, q connections.RepoQuery) ([]connections.RepoItem, string, error) {
	_, tok, err := p.ready(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if q.Installation != 0 {
		return nil, "", p.errf(http.StatusBadRequest, connections.CodeInvalid, "This provider has no installations.")
	}
	page, err := p.pageOf(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	if err := p.checkQ(q.Q); err != nil {
		return nil, "", err
	}
	var list []repoInfo
	var next int
	err = p.asUser(ctx, userID, tok, func(tok string) (err error) {
		list, next, err = p.d.repos(ctx, p.c, tok, page, strings.TrimSpace(q.Q))
		return err
	})
	if err != nil {
		return nil, "", p.userFailed(err)
	}
	out := make([]connections.RepoItem, len(list))
	for i, r := range list {
		out[i] = connections.RepoItem{ID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL, CanPush: r.CanPush}
	}
	return out, nextCursor(next), nil
}

func repoOf(r *repoInfo) *connections.Repo {
	return &connections.Repo{ID: r.ID, Owner: r.Owner, Name: r.Name, FullName: r.FullName, HTMLURL: r.HTMLURL, Private: r.Private}
}

// PlanCreate decides, without creating anything, whose token creates a
// repository for owner: always the caller's own, in their own namespace or a
// group or organisation they may create in.
func (p *Provider) PlanCreate(ctx context.Context, userID, owner string) (*connections.Plan, error) {
	row, tok, err := p.ready(ctx, userID)
	if err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if !p.d.validOwner(owner) {
		return nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "Owner is not an account or group name on %s.", p.name())
	}
	if strings.EqualFold(row.Login, owner) {
		return &connections.Plan{Owner: row.Login, Via: connections.ViaUser, UserID: userID}, nil
	}
	var o *ownerInfo
	err = p.asUser(ctx, userID, tok, func(tok string) (err error) {
		o, err = p.d.findOwner(ctx, p.c, tok, &account{ID: row.AccountID, Login: row.Login}, owner)
		return err
	})
	if err != nil {
		return nil, p.userFailed(err)
	}
	if o == nil {
		return nil, p.errf(http.StatusForbidden, connections.CodeOwnerNotAllowed, "You cannot create a repository for %s.", owner)
	}
	return &connections.Plan{Owner: o.Login, Org: o.Login, Via: connections.ViaUser, UserID: userID, Ref: o.NamespaceID}, nil
}

// CreateRepo creates the empty repository the plan describes, with the token of
// the person the plan is for.
func (p *Provider) CreateRepo(ctx context.Context, plan *connections.Plan, n connections.NewRepoSpec) (*connections.Repo, error) {
	if _, _, err := p.need(); err != nil {
		return nil, err
	}
	if err := p.visibleTo(ctx, plan.UserID); err != nil {
		return nil, err
	}
	tok, err := p.UserToken(ctx, plan.UserID)
	if err != nil {
		return nil, p.tokenError(err)
	}
	var o *ownerInfo
	if plan.Org != "" {
		o = &ownerInfo{Login: plan.Org, Group: true, NamespaceID: plan.Ref}
	}
	var r *repoInfo
	err = p.asUser(ctx, plan.UserID, tok, func(tok string) (err error) {
		r, err = p.d.createRepo(ctx, p.c, tok, o, n.Name, n.Description, n.Private)
		return err
	})
	if err != nil {
		return nil, p.createError(plan, err)
	}
	return repoOf(r), nil
}

// tokenError maps what UserToken fails with.
func (p *Provider) tokenError(err error) error {
	switch {
	case errors.Is(err, ErrNoAuthorization), errors.Is(err, ErrAuthorizationExpired):
		return p.notConnected()
	}
	return p.wrap(err)
}

func (p *Provider) createError(plan *connections.Plan, err error) error {
	var se *StatusError
	if !errors.As(err, &se) || se.RateLimited {
		return p.wrap(err)
	}
	said := se.Message
	name := p.name()
	switch se.Status {
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		if l := strings.ToLower(said); strings.Contains(l, "already been taken") || strings.Contains(l, "already exists") {
			return p.errf(http.StatusConflict, connections.CodeNameTaken, "%s already has a repository with that name.", plan.Owner)
		}
		return p.errf(http.StatusBadRequest, connections.CodeInvalid, "%s refused the repository: %s.", name, quote(said))
	case http.StatusForbidden, http.StatusNotFound:
		return p.errf(http.StatusForbidden, connections.CodeRefused, "%s refused to create the repository: %s.", name, quote(said))
	case http.StatusUnauthorized:
		return p.userFailed(err)
	}
	return p.wrap(err)
}

// ChooseCredential picks how a space syncs with a repository the plan has just
// created: with the token of the person who created it, as every OAuth token of
// a host reaches whatever the person does.
func (p *Provider) ChooseCredential(ctx context.Context, plan *connections.Plan, repo *connections.Repo) (*connections.Cred, error) {
	if _, _, err := p.need(); err != nil {
		return nil, err
	}
	if tok, err := p.UserToken(ctx, plan.UserID); err == nil {
		p.detectPartial(ctx, repo, tok)
	}
	return &connections.Cred{Provider: p.id, Via: connections.ViaUser, UserID: plan.UserID, RepoID: repo.ID}, nil
}

// ResolveExistingIn decides how a space of the given tenant syncs with a
// repository that exists. Only the caller's own connection can: via is "user",
// or "" (the server picks it). The caller's connection must be able to push.
func (p *Provider) ResolveExistingIn(ctx context.Context, tenantID, userID, owner, name, via string) (*connections.Cred, *connections.Repo, error) {
	r, _, err := p.need()
	if err != nil {
		return nil, nil, err
	}
	if r.Owner != store.ConnProviderSite && r.Owner != tenantID {
		return nil, nil, p.errf(http.StatusNotFound, connections.CodeNotFound, "No such provider.")
	}
	switch via {
	case "", connections.ViaUser:
	case connections.ViaApp:
		return nil, nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "%s has no App: connect through your own account (via user).", p.name())
	default:
		return nil, nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "credential.via is user for this provider.")
	}
	_, tok, err := p.ready(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if !p.d.validOwner(owner) || !nameRe.MatchString(name) {
		return nil, nil, p.errf(http.StatusBadRequest, connections.CodeInvalid, "The repository is written owner/name.")
	}
	var repo *repoInfo
	err = p.asUser(ctx, userID, tok, func(tok string) (err error) {
		repo, err = p.d.getRepo(ctx, p.c, tok, owner, name)
		return err
	})
	if err != nil {
		if isStatus(err, http.StatusNotFound, http.StatusForbidden) && !isLimited(err) {
			return nil, nil, p.errf(http.StatusForbidden, connections.CodeOwnerNotAllowed, "Your connection cannot push to %s/%s.", owner, name)
		}
		return nil, nil, p.userFailed(err)
	}
	if !repo.CanPush {
		return nil, nil, p.errf(http.StatusForbidden, connections.CodeOwnerNotAllowed, "Your connection cannot push to %s.", repo.FullName)
	}
	cr := repoOf(repo)
	p.detectPartial(ctx, cr, tok)
	return &connections.Cred{Provider: p.id, Via: connections.ViaUser, UserID: userID, RepoID: repo.ID}, cr, nil
}

func isLimited(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.RateLimited
}

// --- the credential of a space --------------------------------------------------------

func credErr(code, msg string, err error) *gitstore.CredentialError {
	return &gitstore.CredentialError{Code: code, Msg: msg, Err: err}
}

func (p *Provider) offlineErr(err error) *gitstore.CredentialError {
	msg := "could not reach " + p.name()
	var he *connections.Error
	if errors.As(err, &he) && he.Code == connections.CodeRateLimited {
		msg = p.name() + " is limiting this server for a moment"
	}
	return &gitstore.CredentialError{Msg: msg, Offline: true, Err: err}
}

// GitToken is the token Git is given for a space's remote: the access token of
// the user the remote names, never another person's. Every error is a
// *gitstore.CredentialError: with a Code when the credential cannot be had
// until someone fixes something, Offline when the host could not be asked.
// staging is a space that is being made: it has no member yet (it is made for
// the user the remote names), so that is not asked.
func (p *Provider) GitToken(ctx context.Context, r *store.Remote, staging bool) (string, error) {
	if r.Via != connections.ViaUser || r.CredUserID == "" {
		return "", credErr(connections.CodeNotConfigured, "this space's credential is not understood by this server", nil)
	}
	if !p.configured() {
		return "", credErr(connections.CodeNotConfigured, p.name()+" is not set up any more; an administrator must set it up again", nil)
	}
	name := p.name()
	if !staging {
		// Manage may come directly or through a group: the union counts.
		roles, disabled, err := store.MemberStanding(ctx, p.m.opt.Store.Q(), r.SpaceID, r.CredUserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", p.offlineErr(err)
		}
		var ps perm.Set
		for _, role := range roles {
			rp, _ := perm.ForRole(role)
			ps |= rp
		}
		if errors.Is(err, store.ErrNotFound) || disabled || !ps.Has(perm.Manage) {
			return "", credErr(connections.CodeConnectionMissing, "the "+name+" connection this space syncs with belongs to someone who can no longer manage it; connect another", nil)
		}
	}
	tok, err := p.UserToken(ctx, r.CredUserID)
	switch {
	case err == nil:
		return tok, nil
	case errors.Is(err, ErrNoAuthorization):
		return "", credErr(connections.CodeConnectionMissing, "the person whose "+name+" connection this space syncs with has disconnected it; they must connect again", err)
	case errors.Is(err, ErrAuthorizationExpired):
		return "", credErr(connections.CodeConnectionExpired, "the "+name+" authorisation this space syncs with has expired or was revoked; its owner must connect again", err)
	}
	var he *connections.Error
	if errors.As(err, &he) && he.Code == connections.CodeNotConfigured {
		return "", credErr(connections.CodeNotConfigured, name+" is not set up any more; an administrator must set it up again", err)
	}
	return "", p.offlineErr(err)
}

// --- partial sync -------------------------------------------------------------------

// partialEvery is how long what was found about a host is believed.
const partialEvery = 30 * 24 * time.Hour

// detectPartial finds out, once per host and then once a month, whether the
// host can filter a fetch (Git protocol v2 "fetch" with "filter"), by asking
// for the advertisement of a repository the caller can reach, and records the
// answer on the provider. It is quick and never fails the caller: a host that
// cannot be asked stays "unknown".
func (p *Provider) detectPartial(ctx context.Context, repo *connections.Repo, tok string) {
	now := p.m.opt.Now()
	p.mu.Lock()
	if p.partialBusy || (p.row.PartialSync != "unknown" && now.Sub(p.row.PartialChecked) < partialEvery) {
		p.mu.Unlock()
		return
	}
	p.partialBusy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.partialBusy = false; p.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	body, status, err := p.c.getRaw(ctx, "/"+repo.FullName+".git/info/refs", url.Values{"service": {"git-upload-pack"}},
		p.d.gitUser(), tok, map[string]string{"Git-Protocol": "version=2"})
	if err != nil || status != http.StatusOK {
		return
	}
	answer := "no"
	if advertisesFilter(string(body)) {
		answer = "yes"
	}
	if err := store.SetConnProviderPartial(ctx, p.m.opt.Store.Q(), p.id, answer, now); err != nil {
		p.m.log.Warn("could not record whether a host supports partial sync", "provider", p.id, "error", err)
		return
	}
	p.mu.Lock()
	p.row.PartialSync, p.row.PartialChecked = answer, now
	p.mu.Unlock()
}

// advertisesFilter reads a git-upload-pack advertisement: protocol v2's
// "fetch=" capability lists "filter", and protocol v0's first line lists it
// among the capabilities.
func advertisesFilter(adv string) bool {
	if strings.Contains(adv, "version 2") {
		for _, line := range strings.Split(adv, "\n") {
			if i := strings.Index(line, "fetch="); i >= 0 {
				return strings.Contains(line[i:], "filter")
			}
		}
		return false
	}
	return strings.Contains(adv, " filter")
}

// getRaw makes a GET and returns the body (at most 1 MiB) and the status.
func (c *client) getRaw(ctx context.Context, path string, q url.Values, user, pw string, header map[string]string) ([]byte, int, error) {
	target := c.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.SetBasicAuth(user, pw)
	req.Header.Set("User-Agent", "git/petalbook")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode, nil
}
