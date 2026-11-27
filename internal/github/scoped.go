package github

// What a space may sync with, by whose space it is (spaceCtx): a person's
// space syncs through the person's own link or their own installation, an
// organisation's through the organisation's own installation only.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// Owners lists the accounts the caller can create a repository in for a space
// they make, one page at a time. For a person: their own account, then the
// organisations among the installations GitHub lists for their token (one
// request a page), which their own token creates in. For a member of an
// organisation: the organisation's own installations on this server (one query),
// whose token creates.
func (s *Service) Owners(ctx context.Context, userID, cur string) ([]connections.Owner, string, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, "", err
	}
	sc, err := s.userCtx(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	if c.Kind != "" && c.Kind != "p" {
		return nil, "", errf(http.StatusBadRequest, CodeInvalid, "The cursor is not one this server made.")
	}
	if !sc.personal {
		if cur != "" {
			return nil, "", nil
		}
		rows, err := store.ListGitHubInstallationsOwned(ctx, s.opt.Store.Q(), sc.owner())
		if err != nil {
			return nil, "", err
		}
		out := []connections.Owner{}
		for _, r := range rows {
			if r.AccountType == "Organization" {
				out = append(out, connections.Owner{Login: r.Account, Type: r.AccountType, Via: ViaApp, InstallationID: r.ID})
			}
		}
		return out, "", nil
	}
	row, tok, err := s.connected(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if tok == "" {
		return []connections.Owner{}, "", nil
	}
	var out []connections.Owner
	if cur == "" {
		own := connections.Owner{Login: row.Login, Type: "User", Via: ViaUser}
		// One JWT request, remembered while the App is there; a person with the App is not asked twice.
		own.NeedsInstall = s.notInstalledFor(ctx, st, row.Login)
		out = append(out, own)
	}
	list, next, err := s.c.UserInstallations(ctx, tok, c.Page)
	if err != nil {
		return nil, "", s.userFailed(ctx, userID, err)
	}
	for _, in := range list {
		if in.Account.Type != "Organization" || in.Suspended() {
			continue
		}
		out = append(out, connections.Owner{Login: in.Account.Login, Type: in.Account.Type, Via: ViaUser, InstallationID: in.ID})
	}
	nc := ""
	if next > 0 {
		nc = encodeCursor(cursor{Kind: "p", Page: next})
	}
	return out, nc, nil
}

// Repos lists repositories the caller can connect a space they make to. For a
// person: those of an installation of their own (all of them, with its token),
// or those their own connection reaches through an installation or, with mine,
// their own. For a member of an organisation: those of one of the
// organisation's own installations only. One request per page.
func (s *Service) Repos(ctx context.Context, userID string, q connections.RepoQuery) ([]connections.RepoItem, string, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, "", err
	}
	sc, err := s.userCtx(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	c, err := decodeCursor(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	if c.Kind != "" && c.Kind != "p" {
		return nil, "", errf(http.StatusBadRequest, CodeInvalid, "The cursor is not one this server made.")
	}
	var list []githubapp.Repo
	var next int
	switch {
	case q.Mine:
		if !sc.personal {
			return nil, "", orgOnly()
		}
		_, tok, err := s.connected(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		if tok == "" {
			return nil, "", notConnected()
		}
		if list, next, err = s.c.UserRepos(ctx, tok, c.Page); err != nil {
			return nil, "", s.userFailed(ctx, userID, err)
		}
	case q.Installation > 0:
		claim, err := s.claimOf(ctx, sc, q.Installation)
		if err != nil {
			return nil, "", err
		}
		if claim != nil {
			return s.installationRepos(ctx, st, q.Installation, c.Page)
		}
		if !sc.personal {
			return nil, "", errf(http.StatusNotFound, CodeNotFound, "No such installation.")
		}
		_, tok, err := s.connected(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		if tok == "" {
			return nil, "", errf(http.StatusNotFound, CodeNotFound, "No such installation.")
		}
		if list, next, err = s.c.UserInstallationRepos(ctx, tok, q.Installation, c.Page); err != nil {
			if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
				return nil, "", errf(http.StatusNotFound, CodeNotFound, "No such installation.")
			}
			return nil, "", s.userFailed(ctx, userID, err)
		}
	default:
		return nil, "", errf(http.StatusBadRequest, CodeInvalid, "Give installation or mine=true.")
	}
	out := make([]connections.RepoItem, len(list))
	for i, r := range list {
		out[i] = connections.RepoItem{ID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL, CanPush: r.Permissions.Push}
	}
	nc := ""
	if next > 0 {
		nc = encodeCursor(cursor{Kind: "p", Page: next})
	}
	return out, nc, nil
}

// PlanCreate is PlanCreateFor a space the caller makes (of their tenant).
func (s *Service) PlanCreate(ctx context.Context, userID, owner string) (*connections.Plan, error) {
	if _, err := s.need(ctx); err != nil {
		return nil, err
	}
	sc, err := s.userCtx(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.planCreate(ctx, sc, owner)
}

// PlanCreateFor decides, without creating anything, whose token creates a
// repository for owner for a space of the scope. A person's: their own token,
// on their account or an organisation GitHub lists for them (it needs their
// connection: not_connected when it has expired). An organisation's: the
// token of the organisation's own installation on that GitHub organisation;
// anything else is owner_not_allowed.
func (s *Service) PlanCreateFor(ctx context.Context, sc connections.SpaceScope, userID, owner string) (*connections.Plan, error) {
	if _, err := s.need(ctx); err != nil {
		return nil, err
	}
	return s.planCreate(ctx, ctxOf(sc, userID), owner)
}

func (s *Service) planCreate(ctx context.Context, sc spaceCtx, owner string) (*connections.Plan, error) {
	owner = strings.TrimSpace(owner)
	if !validOwner(owner) {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "Owner is not a GitHub account name.")
	}
	userID := sc.userID
	if !sc.personal {
		in, err := store.GitHubInstallationOwnedOn(ctx, s.opt.Store.Q(), sc.owner(), owner)
		if err == nil && in.AccountType == "Organization" {
			return &connections.Plan{Owner: in.Account, Org: in.Account, Via: ViaApp, UserID: userID, InstallationID: in.ID}, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "%s is not a GitHub organisation your organisation has connected on this server.", owner)
	}
	row, tok, err := s.connected(ctx, userID)
	if err != nil {
		return nil, err
	}
	if row != nil && strings.EqualFold(row.Login, owner) {
		if tok == "" {
			return nil, notConnected()
		}
		return &connections.Plan{Owner: row.Login, Via: ViaUser, UserID: userID}, nil
	}
	if tok == "" {
		return nil, notConnected()
	}
	for page := 1; page > 0 && page <= maxOwnerPages; {
		list, next, err := s.c.UserInstallations(ctx, tok, page)
		if err != nil {
			return nil, s.userFailed(ctx, userID, err)
		}
		for _, in := range list {
			if in.Account.Type == "Organization" && !in.Suspended() && strings.EqualFold(in.Account.Login, owner) {
				return &connections.Plan{Owner: in.Account.Login, Org: in.Account.Login, Via: ViaUser, UserID: userID, InstallationID: in.ID}, nil
			}
		}
		page = next
	}
	return nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "You cannot create a repository for %s.", owner)
}

// ChooseCredential picks and checks how a space syncs with a repository the
// plan has just created. An organisation's plan (via the App) syncs through
// that installation, which must reach the repository; nothing else is tried.
// A person's syncs through their own installation on their own account when
// they have one here and it reaches the repository (one of "selected
// repositories" gets it added with their token), else through their own token.
func (s *Service) ChooseCredential(ctx context.Context, p *connections.Plan, repo *connections.Repo) (*connections.Cred, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, err
	}
	if p.Via == ViaApp {
		ok, err := s.reaches(ctx, st, p.InstallationID, repo.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			e := errf(http.StatusConflict, CodeRepoNotInInstall, "The App's installation on %s does not reach the new repository %s; add it in the installation's settings on GitHub.", p.Owner, repo.FullName)
			e.Repo = repo.Ref()
			return nil, e
		}
		return &connections.Cred{Provider: ProviderID, Via: ViaApp, InstallationID: p.InstallationID, RepoID: repo.ID}, nil
	}
	if p.Org == "" {
		in, err := store.GitHubInstallationOwnedOn(ctx, s.opt.Store.Q(), store.InstallOwner{UserID: p.UserID}, p.Owner)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if in != nil {
			ok, err := s.reaches(ctx, st, in.ID, repo.ID)
			if err != nil {
				return nil, err
			}
			if !ok {
				if _, tok, cerr := s.connected(ctx, p.UserID); cerr == nil && tok != "" {
					if aerr := s.c.AddRepoToInstallation(ctx, tok, in.ID, repo.ID); aerr != nil && !githubapp.IsStatus(aerr, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity) {
						return nil, s.userFailed(ctx, p.UserID, aerr)
					} else if aerr == nil {
						if ok, err = s.reaches(ctx, st, in.ID, repo.ID); err != nil {
							return nil, err
						}
					}
				}
			}
			if ok {
				return &connections.Cred{Provider: ProviderID, Via: ViaApp, InstallationID: in.ID, UserID: p.UserID, RepoID: repo.ID}, nil
			}
		}
	}
	return &connections.Cred{Provider: ProviderID, Via: ViaUser, UserID: p.UserID, RepoID: repo.ID}, nil
}

// ResolveExisting decides how a space the caller makes syncs with a
// repository that exists (ResolveExistingFor, in the caller's own context).
func (s *Service) ResolveExisting(ctx context.Context, userID, owner, name, via string) (*connections.Cred, *connections.Repo, error) {
	sc, err := s.userCtx(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return s.resolveExisting(ctx, sc, owner, name, via)
}

// ResolveExistingIn is ResolveExistingFor a space of the tenant that is not
// someone's personal space.
func (s *Service) ResolveExistingIn(ctx context.Context, tenantID, userID, owner, name, via string) (*connections.Cred, *connections.Repo, error) {
	return s.resolveExisting(ctx, ctxOf(connections.ScopeOfSpace(tenantID, false), userID), owner, name, via)
}

// ResolveExistingFor decides how a space of the scope syncs with a repository
// that exists. via is "app", "user" or "" (the App's installation when the
// space's owner has it here, else the caller's own connection). A person's
// space takes their own installation (the person is the one in the
// credential) or their own connection, which must be able to push; an
// organisation's takes only the organisation's own installation.
func (s *Service) ResolveExistingFor(ctx context.Context, sc connections.SpaceScope, userID, owner, name, via string) (*connections.Cred, *connections.Repo, error) {
	return s.resolveExisting(ctx, ctxOf(sc, userID), owner, name, via)
}

func (s *Service) resolveExisting(ctx context.Context, sc spaceCtx, owner, name, via string) (*connections.Cred, *connections.Repo, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !sc.personal {
		if via == ViaUser {
			return nil, nil, orgOnly()
		}
		return s.existingApp(ctx, st, sc, owner, name)
	}
	switch via {
	case ViaApp:
		return s.existingApp(ctx, st, sc, owner, name)
	case ViaUser:
		return s.existingUser(ctx, sc.userID, owner, name)
	}
	cred, repo, aerr := s.existingApp(ctx, st, sc, owner, name)
	if aerr == nil {
		return cred, repo, nil
	}
	var he *connections.Error
	if !errors.As(aerr, &he) || (he.Code != CodeRepoNotInInstall && he.Code != CodeOwnerNotAllowed && he.Code != CodeNotConnected && he.Code != CodeMissingPermission) {
		return nil, nil, aerr
	}
	cred, repo, uerr := s.existingUser(ctx, sc.userID, owner, name)
	if uerr == nil {
		return cred, repo, nil
	}
	if errors.As(uerr, &he) && he.Code == CodeNotConnected {
		return nil, nil, aerr // the first refusal says more
	}
	return nil, nil, uerr
}

func (s *Service) existingUser(ctx context.Context, userID, owner, name string) (*connections.Cred, *connections.Repo, error) {
	_, tok, err := s.connected(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if tok == "" {
		return nil, nil, notConnected()
	}
	r, err := s.c.GetRepo(ctx, tok, owner, name)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
			return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "Your GitHub connection cannot push to %s/%s.", owner, name)
		}
		return nil, nil, s.userFailed(ctx, userID, err)
	}
	if !r.Permissions.Push {
		return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "Your GitHub connection cannot push to %s.", r.FullName)
	}
	return &connections.Cred{Provider: ProviderID, Via: ViaUser, UserID: userID, RepoID: r.ID}, repoOf(r), nil
}

// existingApp is the repository through the installation that reaches it,
// when that installation is the space owner's own on this server.
func (s *Service) existingApp(ctx context.Context, st *appState, sc spaceCtx, owner, name string) (*connections.Cred, *connections.Repo, error) {
	inst, err := st.app.RepoInstallation(ctx, owner, name)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound) {
			return nil, nil, errf(http.StatusConflict, CodeRepoNotInInstall, "The App is not installed on %s/%s, or its installation does not reach that repository.", owner, name)
		}
		return nil, nil, s.listError(err)
	}
	claim, err := s.claimOf(ctx, sc, inst)
	if err != nil {
		return nil, nil, err
	}
	if claim == nil {
		if sc.personal {
			return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "The App's installation that reaches %s/%s is not yours on this server: install the App on your own account from your settings.", owner, name)
		}
		return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "The App's installation that reaches %s/%s is not your organisation's on this server: an administrator of the organisation connects it in its settings.", owner, name)
	}
	tok, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"metadata": "read"}})
	if err != nil {
		return nil, nil, s.listError(err)
	}
	repo, err := s.c.GetRepo(ctx, tok, owner, name)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
			return nil, nil, errf(http.StatusConflict, CodeRepoNotInInstall, "The App's installation does not reach %s/%s.", owner, name)
		}
		return nil, nil, s.wrap(err)
	}
	ok, err := s.reaches(ctx, st, inst, repo.ID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		st.app.ForgetLookup("repo", owner+"/"+name) // it may have moved: look again next time
		return nil, nil, errf(http.StatusConflict, CodeRepoNotInInstall, "The App's installation does not reach %s.", repo.FullName)
	}
	cred := &connections.Cred{Provider: ProviderID, Via: ViaApp, InstallationID: inst, RepoID: repo.ID}
	if sc.personal {
		cred.UserID = sc.userID // a person's installation syncs while they may manage the space
	}
	return cred, repoOf(repo), nil
}
