package github

// Installations of the App, each connected to its one owner on this server: a
// person installs the App on their own GitHub account, an organisation (a
// tenant) on its GitHub organisation, and each one's spaces sync only through
// what is their own (docs/api.md, "The GitHub provider", "Installations").
//
// Claiming one is the security-sensitive step: the installation_id GitHub's
// redirect carries is only a claim of the browser's. It is taken only when
// GitHub lists the installation for the claimer's own user token (GET
// /user/installations), on the account they started the attempt for (their own
// account, or the organisation they named), and when nobody else on this
// server has it.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// kindInstall is the kind of an attempt to install the App and connect the
// installation to an owner.
const kindInstall = "install"

// More callback failure codes (the connection_error of the page returned to).
const (
	// cbForbidden: the person may not connect an installation for that owner
	// (the feature is off, or they are not the organisation's administrator).
	cbForbidden = "forbidden"
	// cbNotYours: GitHub does not list the installation for the person's own
	// token: the installation_id of the redirect is not theirs to claim.
	cbNotYours = "installation_not_yours"
	// cbWrongAccount: the installation is on another account than the one the
	// attempt was for (an organisation instead of the person's own account,
	// another organisation, or a person's account for an organisation).
	cbWrongAccount = "wrong_account"
	// cbNotOrgAdmin: the claimer is not an active owner of the GitHub
	// organisation (a member who merely reaches one of its repositories).
	cbNotOrgAdmin = connections.CodeNotOrgAdmin
	// cbTaken: another person or organisation on this server has it.
	cbTaken = connections.CodeInstallationTaken
	// cbNotConnected: the App asked for no authorisation and the person has no
	// GitHub connection to check the installation with.
	cbNotConnected = "not_connected"
)

// spaceCtx is whose spaces a request is for: a person's (their own link and
// installations) or an organisation's (the tenant's installations only).
type spaceCtx struct {
	personal bool
	userID   string // the person acting (for a personal space, its person)
	tenantID string
}

// owner is who the installations usable in the context belong to.
func (c spaceCtx) owner() store.InstallOwner {
	if c.personal {
		return store.InstallOwner{UserID: c.userID}
	}
	return store.InstallOwner{TenantID: c.tenantID}
}

func ctxOf(sc connections.SpaceScope, userID string) spaceCtx {
	return spaceCtx{personal: sc.Personal, userID: userID, tenantID: sc.TenantID}
}

// userCtx is the context of the spaces a person makes: their tenant's, or
// their own when they are in the default tenant (no organisation).
func (s *Service) userCtx(ctx context.Context, userID string) (spaceCtx, error) {
	t, err := store.UserTenant(ctx, s.opt.Store.Q(), userID)
	if errors.Is(err, store.ErrNotFound) {
		return spaceCtx{}, errf(http.StatusForbidden, CodeForbidden, "No such user.")
	}
	if err != nil {
		return spaceCtx{}, err
	}
	return ctxOf(connections.ScopeOfSpace(t, false), userID), nil
}

func orgOnly() *connections.Error {
	return errf(http.StatusForbidden, CodeOwnerNotAllowed, "An organisation's space syncs only through the organisation's own GitHub installation, never through a person's own connection.")
}

// allowInstall asks the hooks the caller lent whether the person may connect
// an installation for the owner now; without hooks, nobody may.
func (s *Service) allowInstall(ctx context.Context, o store.InstallOwner) error {
	h := connections.InstallHooksOf(ctx)
	if h.Allow == nil {
		return errf(http.StatusForbidden, CodeForbidden, "GitHub installations cannot be connected here.")
	}
	return h.Allow(ctx, o)
}

// StartInstallFor begins installing the App for an owner: the person's own
// GitHub account (o.UserID, which must be the caller), or the GitHub
// organisation named account for a tenant (o.TenantID). It answers the App's
// installation page and the binding of the attempt; the callback connects the
// installation only after GitHub showed it is the caller's to claim.
func (s *Service) StartInstallFor(ctx context.Context, userID string, o store.InstallOwner, account, ret string) (installPage, binding string, err error) {
	st, err := s.needUserAuth(ctx)
	if err != nil {
		return "", "", err
	}
	if !o.Valid() || (o.UserID != "" && o.UserID != userID) || o.TenantID == store.DefaultTenantID {
		return "", "", errf(http.StatusForbidden, CodeForbidden, "An installation is connected to yourself or to your organisation.")
	}
	if err := s.allowInstall(ctx, o); err != nil {
		return "", "", err
	}
	account = strings.TrimSpace(account)
	if o.TenantID != "" {
		if !validOwner(account) {
			return "", "", errf(http.StatusBadRequest, CodeInvalid, "Give the name of the GitHub organisation to install the App on.")
		}
	} else {
		account = ""
	}
	page := installURL(st.rec.HTMLURL)
	if page == "" {
		return "", "", errf(http.StatusConflict, CodeNotConfigured, "GitHub did not say where the App is installed.")
	}
	state := githubapp.RandomState()
	binding = s.attempts.put(attempt{kind: kindInstall, state: state, userID: userID, ret: safeReturn(ret), owner: o, account: account})
	return page + "?state=" + url.QueryEscape(state), binding, nil
}

// withError is ret with connection_error=code added.
func withError(ret, code string) string {
	sep := "?"
	if strings.Contains(ret, "?") {
		sep = "&"
	}
	return ret + sep + "connection_error=" + url.QueryEscape(code)
}

// completeInstall finishes an installation attempt from GitHub's redirect.
func (s *Service) completeInstall(ctx context.Context, a *attempt, code string, one func(string) (string, bool)) (string, bool) {
	fail := func(c string) (string, bool) {
		if a.owner.TenantID != "" {
			return withError(a.ret, c), true
		}
		return failRedirect + c, true
	}
	action, _ := one("setup_action")
	if action == "request" {
		return fail(cbRequested)
	}
	raw, ok := one("installation_id")
	id, perr := strconv.ParseInt(raw, 10, 64)
	if !ok || perr != nil || id <= 0 {
		return fail(cbError)
	}
	// Asked again: the person may have lost the right, or the feature gone off,
	// since the start.
	if err := s.allowInstall(ctx, a.owner); err != nil {
		return fail(cbForbidden)
	}
	st, err := s.state(ctx)
	if err != nil || st == nil || !st.userAuth(s.opt.PublicURL) {
		return fail(cbError)
	}
	var tok string
	var who githubapp.User
	if code != "" {
		t, err := s.c.ExchangeCode(ctx, st.rec.ClientID, st.secret, code, s.CallbackURL(), a.verifier)
		if err != nil {
			s.log.Warn("GitHub refused the authorisation code of an installation", "reason", reason(err))
			return fail(cbError)
		}
		acct, err := s.c.User(ctx, t.Access)
		if err != nil {
			s.log.Warn("could not read the GitHub account", "reason", reason(err))
			return fail(cbError)
		}
		// A person's installation is on their own account, which is their link:
		// it is stored as connecting would. An organisation's needs no link of
		// the administrator: the token only proves what they reach.
		if a.owner.UserID != "" {
			if c := s.storeLink(ctx, a.userID, t, acct); c != "" {
				return fail(c)
			}
		}
		tok, who = t.Access, *acct
	} else {
		t, err := s.UserToken(ctx, a.userID)
		if err != nil {
			return fail(cbNotConnected)
		}
		row, err := store.GetRepoLink(ctx, s.opt.Store.Q(), ProviderID, a.userID)
		if err != nil {
			return fail(cbNotConnected)
		}
		who.ID, _ = strconv.ParseInt(row.Subject, 10, 64)
		who.Login = row.Login
		tok = t
	}
	in, err := s.userInstallation(ctx, tok, id)
	if err != nil {
		s.log.Warn("could not list the person's GitHub installations", "reason", reason(err))
		return fail(cbError)
	}
	if in == nil {
		s.log.Warn("an installation was claimed that GitHub does not list for the person", "installation", id)
		return fail(cbNotYours)
	}
	switch {
	case a.owner.UserID != "":
		if in.Account.Type != "User" || who.ID == 0 || in.Account.ID != who.ID {
			return fail(cbWrongAccount)
		}
	default:
		if in.Account.Type != "Organization" || !strings.EqualFold(in.Account.Login, a.account) {
			return fail(cbWrongAccount)
		}
		// GitHub lists an organisation's installation for any member who reaches
		// one of its repositories: only the organisation's owners may connect it.
		m, err := s.c.UserOrgMembership(ctx, tok, in.Account.Login)
		switch {
		case githubapp.IsPermissionRefusal(err):
			// The App (or this installation) lacks the organisation's Members
			// permission, which GitHub needs to answer: an administrator's fix,
			// not the person's standing.
			s.autoRecheck(ctx, st)
			return fail(CodeMissingPermission)
		case githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err):
			return fail(cbNotOrgAdmin)
		case err != nil:
			s.log.Warn("could not read the person's membership of the organisation", "reason", reason(err))
			return fail(cbError)
		case !m.ActiveOwner():
			return fail(cbNotOrgAdmin)
		}
	}
	now := s.opt.Now()
	err = store.ClaimGitHubInstallation(ctx, s.opt.Store.Q(), store.GitHubInstallation{ID: in.ID, Account: in.Account.Login, AccountID: in.Account.ID,
		AccountType: in.Account.Type, OwnerUser: a.owner.UserID, OwnerTenant: a.owner.TenantID, ClaimedBy: a.userID, Created: now, Updated: now})
	switch {
	case errors.Is(err, store.ErrInstallationTaken):
		return fail(cbTaken)
	case err != nil:
		s.log.Error("could not connect a GitHub installation", "error", err)
		return fail(cbError)
	}
	if h := connections.InstallHooksOf(ctx); h.Claimed != nil {
		h.Claimed(ctx, a.owner, in.ID, in.Account.Login)
	}
	s.forgetNoApp(in.Account.Login)
	s.changed("") // spaces that lost access through it are fetched again
	return a.ret, true
}

// userInstallation finds an installation among those GitHub lists for a user
// token, a page at a time (at most maxOwnerPages); nil when it is not there.
func (s *Service) userInstallation(ctx context.Context, tok string, id int64) (*githubapp.Installation, error) {
	for page := 1; page > 0 && page <= maxOwnerPages; {
		list, next, err := s.c.UserInstallations(ctx, tok, page)
		if err != nil {
			return nil, err
		}
		for i := range list {
			if list[i].ID == id {
				return &list[i], nil
			}
		}
		page = next
	}
	return nil, nil
}

// --- an owner's installations --------------------------------------------------------

// OwnedInstallation is an installation as its owner sees it.
type OwnedInstallation struct {
	ID          int64
	Account     string
	AccountType string
	Owner       store.InstallOwner
	ClaimedBy   string
	Connected   string // RFC 3339
	Spaces      int
	// ManageURL is the installation's settings on GitHub, where its
	// repositories are chosen and where it is uninstalled.
	ManageURL string
}

func (s *Service) manageURL(in *store.GitHubInstallation) string {
	base := strings.TrimRight(s.c.Web(), "/")
	if in.AccountType == "Organization" {
		return base + "/organizations/" + url.PathEscape(in.Account) + "/settings/installations/" + strconv.FormatInt(in.ID, 10)
	}
	return base + "/settings/installations/" + strconv.FormatInt(in.ID, 10)
}

// Installations lists the owner's installations, with how many spaces sync
// through each: two queries, nothing asked of GitHub.
func (s *Service) Installations(ctx context.Context, o store.InstallOwner) ([]OwnedInstallation, error) {
	q := s.opt.Store.Q()
	rows, err := store.ListGitHubInstallationsOwned(ctx, q, o)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	counts, err := store.GitHubSpaceCounts(ctx, q, o, ids)
	if err != nil {
		return nil, err
	}
	out := make([]OwnedInstallation, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = OwnedInstallation{ID: r.ID, Account: r.Account, AccountType: r.AccountType, Owner: r.Owner(), ClaimedBy: r.ClaimedBy,
			Connected: r.Created.UTC().Format("2006-01-02T15:04:05Z"), Spaces: counts[r.ID], ManageURL: s.manageURL(r)}
	}
	return out, nil
}

// owned is the owner's installation, or not_found (another owner's is as
// good as not there).
func (s *Service) owned(ctx context.Context, o store.InstallOwner, id int64) (*store.GitHubInstallation, error) {
	in, err := store.GetGitHubInstallation(ctx, s.opt.Store.Q(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.Owner() != o) {
		return nil, errf(http.StatusNotFound, CodeNotFound, "No such installation.")
	}
	return in, err
}

// InstallationSpaces lists the spaces that sync through the owner's
// installation (at most 200 by name, and how many in all): what disconnecting it affects.
func (s *Service) InstallationSpaces(ctx context.Context, o store.InstallOwner, id int64) ([]store.SpaceRef, int, error) {
	if _, err := s.owned(ctx, o, id); err != nil {
		return nil, 0, err
	}
	return store.SpacesOnGitHubInstallation(ctx, s.opt.Store.Q(), o, id)
}

// InstallationRepos lists the repositories the owner's installation reaches,
// one page of GitHub's list with the installation's own token.
func (s *Service) InstallationRepos(ctx context.Context, o store.InstallOwner, id int64, cur string) ([]connections.RepoItem, string, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, "", err
	}
	if _, err := s.owned(ctx, o, id); err != nil {
		return nil, "", err
	}
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	return s.installationRepos(ctx, st, id, c.Page)
}

func (s *Service) installationRepos(ctx context.Context, st *appState, id int64, page int) ([]connections.RepoItem, string, error) {
	tok, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: id, Perms: map[string]string{"metadata": "read"}})
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden, http.StatusUnprocessableEntity) && !limited(err) {
			return nil, "", errf(http.StatusNotFound, CodeNotFound, "The App is no longer installed there.")
		}
		return nil, "", s.listError(err)
	}
	list, next, err := s.c.InstallationRepos(ctx, tok, page)
	if err != nil {
		return nil, "", s.listError(err)
	}
	out := make([]connections.RepoItem, len(list))
	for i, r := range list {
		out[i] = connections.RepoItem{ID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL, CanPush: true}
	}
	nc := ""
	if next > 0 {
		nc = encodeCursor(cursor{Kind: "p", Page: next})
	}
	return out, nc, nil
}

// lostMessage is what a space that lost its installation says.
const lostMessage = "this space lost GitHub access: the installation it synced through is no longer connected to its owner on this server; connect GitHub again, or connect the space to another repository"

// DisconnectInstallation removes the owner's installation from this server
// (the App stays installed on GitHub, where it is uninstalled): the spaces that
// synced through it keep their notes and show that they lost GitHub access.
// It reports their ids.
func (s *Service) DisconnectInstallation(ctx context.Context, o store.InstallOwner, id int64) ([]string, error) {
	if _, err := s.owned(ctx, o, id); err != nil {
		return nil, err
	}
	ids, err := store.DisconnectGitHubInstallation(ctx, s.opt.Store.Q(), id, o, lostMessage, s.opt.Now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errf(http.StatusNotFound, CodeNotFound, "No such installation.")
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	f := s.onLost
	s.mu.Unlock()
	if f != nil && len(ids) > 0 {
		f(ids)
	}
	return ids, nil
}

// OnAccessLost registers what is told the spaces that lost GitHub access.
func (s *Service) OnAccessLost(f func(spaceIDs []string)) {
	s.mu.Lock()
	s.onLost = f
	s.mu.Unlock()
}

// claimOf is the installation's claim when it belongs to the context's owner;
// nil when nobody here has it or someone else does.
func (s *Service) claimOf(ctx context.Context, c spaceCtx, id int64) (*store.GitHubInstallation, error) {
	if id <= 0 {
		return nil, nil
	}
	in, err := store.GetGitHubInstallation(ctx, s.opt.Store.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if in.Owner() != c.owner() {
		return nil, nil
	}
	return in, nil
}
