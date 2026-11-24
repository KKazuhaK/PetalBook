package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Owner is an account a repository can be created in.
type Owner struct {
	Login          string
	Type           string // "User" or "Organization"
	Via            string // "user": the caller's own token creates the repository; "app": the installation's
	InstallationID int64  // 0 when not known (the caller's own account)
	// NeedsInstall: the caller's own account, where the App is not installed;
	// GitHub lets no repository be made there until it is.
	NeedsInstall bool
}

// RepoItem is a repository a space can be connected to.
type RepoItem struct {
	ID            int64
	FullName      string
	Private       bool
	DefaultBranch string
	HTMLURL       string
	CanPush       bool
}

// Via values.
const (
	ViaApp  = "app"
	ViaUser = "user"
)

// connected is the user's connection when it is usable, with its token; nil
// when there is none or it has expired.
func (s *Service) connected(ctx context.Context, userID string) (*store.GitHubAuthorization, string, error) {
	row, err := store.GetGitHubAuthorization(ctx, s.opt.Store.Q(), userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if row.State != store.GitHubOK {
		return row, "", nil
	}
	tok, err := s.UserToken(ctx, userID)
	switch {
	case errors.Is(err, ErrNoAuthorization), errors.Is(err, ErrAuthorizationExpired):
		return nil, "", nil
	case err != nil:
		return nil, "", err
	}
	return row, tok, nil
}

// userFailed turns an error of a call made with a user's token into an answer;
// a token GitHub no longer accepts ends the connection.
func (s *Service) userFailed(ctx context.Context, userID string, err error) error {
	if githubapp.IsStatus(err, http.StatusUnauthorized) && !limited(err) {
		if row, gerr := store.GetGitHubAuthorization(ctx, s.opt.Store.Q(), userID); gerr == nil {
			s.expire(ctx, row)
		}
		return notAuthorized()
	}
	return s.wrap(err)
}

// Owners lists the accounts the caller can create a repository in, one page of
// GitHub's list (one request) at a time: the caller's own account, the
// organisations among the installations GitHub lists for them, then the
// organisations an administrator shared with the caller's tenant that are not
// listed yet.
func (s *Service) Owners(ctx context.Context, userID, cur string) ([]Owner, string, error) {
	if _, err := s.need(ctx); err != nil {
		return nil, "", err
	}
	sc, err := s.scopeOf(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	var out []Owner
	seen := append([]int64(nil), c.Seen...)
	phase := c.Kind
	if phase == "" {
		phase = "p"
		c.Page = 0
	}
	if phase == "p" {
		row, tok, err := s.connected(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		if tok == "" {
			phase = "s"
		} else {
			if cur == "" {
				own := Owner{Login: row.Login, Type: "User", Via: ViaUser}
				// One JWT request, remembered while the App is there; a person with the App is not asked twice.
				if st, serr := s.need(ctx); serr == nil {
					_, ierr := st.app.UserInstallation(ctx, row.Login)
					own.NeedsInstall = githubapp.IsStatus(ierr, http.StatusNotFound)
				}
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
				seen = append(seen, in.ID)
				out = append(out, Owner{Login: in.Account.Login, Type: in.Account.Type, Via: ViaUser, InstallationID: in.ID})
			}
			if next > 0 {
				return out, encodeCursor(cursor{Kind: "p", Page: next, Seen: capSeen(seen)}), nil
			}
			phase, c.After = "s", 0
		}
	}
	// Shared installations: only the database is asked.
	skip := map[int64]bool{}
	for _, id := range seen {
		skip[id] = true
	}
	const limit = 30
	rows, err := store.ListGitHubInstallationsOf(ctx, s.opt.Store.Q(), sc, c.After, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(cursor{Kind: "s", After: rows[limit-1].ID, Seen: capSeen(seen)})
	}
	for _, r := range rows {
		if skip[r.ID] {
			continue
		}
		out = append(out, Owner{Login: r.Account, Type: r.AccountType, Via: ViaApp, InstallationID: r.ID})
	}
	return out, next, nil
}

func capSeen(seen []int64) []int64 {
	if len(seen) > 400 {
		return seen[len(seen)-400:]
	}
	return seen
}

// Repos lists repositories the caller can connect a space to: those of an
// installation (shared with the caller's tenant: all of them; otherwise the ones the caller's own
// connection reaches), or, with mine, the caller's own. One request per page.
func (s *Service) Repos(ctx context.Context, userID string, installation int64, mine bool, cur string) ([]RepoItem, string, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, "", err
	}
	c, err := decodeCursor(cur)
	if err != nil {
		return nil, "", err
	}
	if c.Kind != "" && c.Kind != "p" {
		return nil, "", errf(http.StatusBadRequest, CodeInvalid, "the cursor is not one this server made")
	}
	var list []githubapp.Repo
	var next int
	pushOK := false
	switch {
	case mine:
		_, tok, err := s.connected(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		if tok == "" {
			return nil, "", notAuthorized()
		}
		if list, next, err = s.c.UserRepos(ctx, tok, c.Page); err != nil {
			return nil, "", s.userFailed(ctx, userID, err)
		}
	case installation > 0:
		sc, err := s.scopeOf(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		shared, err := store.GitHubInstallationShared(ctx, s.opt.Store.Q(), sc, installation)
		if err != nil {
			return nil, "", err
		}
		if shared {
			tok, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: installation, Perms: map[string]string{"metadata": "read"}})
			if err != nil {
				if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden, http.StatusUnprocessableEntity) && !limited(err) {
					return nil, "", errf(http.StatusNotFound, CodeNotFound, "the App is no longer installed there")
				}
				return nil, "", s.listError(err)
			}
			if list, next, err = s.c.InstallationRepos(ctx, tok, c.Page); err != nil {
				return nil, "", s.listError(err)
			}
			pushOK = true // the installation's own token pushes
			break
		}
		_, tok, err := s.connected(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		if tok == "" {
			return nil, "", errf(http.StatusNotFound, CodeNotFound, "no such installation")
		}
		if list, next, err = s.c.UserInstallationRepos(ctx, tok, installation, c.Page); err != nil {
			if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
				return nil, "", errf(http.StatusNotFound, CodeNotFound, "no such installation")
			}
			return nil, "", s.userFailed(ctx, userID, err)
		}
	default:
		return nil, "", errf(http.StatusBadRequest, CodeInvalid, "give installation or mine=true")
	}
	out := make([]RepoItem, len(list))
	for i, r := range list {
		out[i] = RepoItem{ID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL, CanPush: r.Permissions.Push || pushOK}
	}
	nc := ""
	if next > 0 {
		nc = encodeCursor(cursor{Kind: "p", Page: next})
	}
	return out, nc, nil
}

// --- creating a repository -----------------------------------------------------------

// Plan says where a repository will be created and with whose token.
type Plan struct {
	Owner          string // as GitHub spells it
	Org            string // "" for the caller's own account
	Via            string // the token that creates it: "user" or "app"
	UserID         string
	InstallationID int64 // where the App is installed on the account, if known
}

// NewRepoSpec is what a repository is created with.
type NewRepoSpec struct {
	Name        string
	Private     bool
	Description string
}

const maxOwnerPages = 10

// PlanCreate decides, without creating anything, whose token creates a
// repository for owner: the caller's own when the owner is their account or an
// organisation GitHub lists for them, else the installation's when an
// administrator shared it with the caller's tenant. It needs the App and, for the first, the caller's
// connection (github_not_authorized when it has expired).
func (s *Service) PlanCreate(ctx context.Context, userID, owner string) (*Plan, error) {
	if _, err := s.need(ctx); err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if !validOwner(owner) {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "owner is not a GitHub account name")
	}
	row, tok, err := s.connected(ctx, userID)
	if err != nil {
		return nil, err
	}
	if row != nil && strings.EqualFold(row.Login, owner) {
		if tok == "" {
			return nil, notAuthorized()
		}
		return &Plan{Owner: row.Login, Via: ViaUser, UserID: userID}, nil
	}
	if tok != "" {
		for page := 1; page > 0 && page <= maxOwnerPages; {
			list, next, err := s.c.UserInstallations(ctx, tok, page)
			if err != nil {
				return nil, s.userFailed(ctx, userID, err)
			}
			for _, in := range list {
				if in.Account.Type == "Organization" && !in.Suspended() && strings.EqualFold(in.Account.Login, owner) {
					return &Plan{Owner: in.Account.Login, Org: in.Account.Login, Via: ViaUser, UserID: userID, InstallationID: in.ID}, nil
				}
			}
			page = next
		}
	}
	sc, err := s.scopeOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	sh, err := store.GitHubInstallationOf(ctx, s.opt.Store.Q(), sc, owner)
	if err == nil && sh.AccountType == "Organization" {
		return &Plan{Owner: sh.Account, Org: sh.Account, Via: ViaApp, UserID: userID, InstallationID: sh.ID}, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "you cannot create a repository for %s", owner)
}

var ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func validOwner(s string) bool { return ownerRe.MatchString(s) }

// ValidRepoName checks a repository name: what GitHub keeps as it is.
func ValidRepoName(name string) bool {
	return repoNameRe.MatchString(name) && name != "." && name != ".." && !strings.HasSuffix(strings.ToLower(name), ".git")
}

// Repo is a repository on GitHub as this package hands it on.
type Repo struct {
	ID       int64
	Owner    string
	Name     string
	FullName string
	HTMLURL  string
	Private  bool
}

func repoOf(r *githubapp.Repo) *Repo {
	owner, name, _ := strings.Cut(r.FullName, "/")
	return &Repo{ID: r.ID, Owner: owner, Name: name, FullName: r.FullName, HTMLURL: r.HTMLURL, Private: r.Private}
}

// Ref is the repository as an error answer names it.
func (r *Repo) Ref() *RepoRef { return &RepoRef{ID: r.ID, FullName: r.FullName, HTMLURL: r.HTMLURL} }

// CreateRepo creates the empty repository the plan describes.
func (s *Service) CreateRepo(ctx context.Context, p *Plan, n NewRepoSpec) (*Repo, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, err
	}
	var token string
	switch p.Via {
	case ViaUser:
		if token, err = s.UserToken(ctx, p.UserID); err != nil {
			return nil, s.tokenError(err)
		}
	default:
		// One request's token: administration write on the installation, no repository named.
		if token, err = st.app.Token(ctx, githubapp.TokenSpec{Installation: p.InstallationID, Perms: map[string]string{"administration": "write"}, Once: true}); err != nil {
			switch {
			case githubapp.IsStatus(err, http.StatusUnauthorized) && !limited(err):
				return nil, errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App")
			case githubapp.IsPermissionRefusal(err):
				return nil, s.missingPermission(ctx, st, p.InstallationID, administrationWrite)
			case githubapp.IsStatus(err, http.StatusUnprocessableEntity, http.StatusForbidden, http.StatusNotFound) && !limited(err):
				return nil, errf(http.StatusForbidden, CodeNoPermission, "the App is not installed there, or GitHub refused it a token for the installation")
			}
			return nil, s.wrap(err)
		}
	}
	r, err := s.c.CreateRepo(ctx, token, p.Org, githubapp.NewRepo{Name: n.Name, Private: n.Private, Description: n.Description})
	if err != nil {
		return nil, s.createError(ctx, p, err)
	}
	return repoOf(r), nil
}

// tokenError maps what UserToken fails with.
func (s *Service) tokenError(err error) error {
	switch {
	case errors.Is(err, ErrNoAuthorization), errors.Is(err, ErrAuthorizationExpired):
		return notAuthorized()
	}
	return s.wrap(err)
}

func (s *Service) createError(ctx context.Context, p *Plan, err error) error {
	var se *githubapp.StatusError
	if !errors.As(err, &se) || se.RateLimited {
		return s.wrap(err)
	}
	said := se.Message
	if len(se.Errors) > 0 {
		said = strings.Join(se.Errors, "; ")
	}
	switch se.Status {
	case http.StatusUnprocessableEntity:
		for _, e := range append(se.Errors, se.Message) {
			if strings.Contains(strings.ToLower(e), "already exists") {
				return errf(http.StatusConflict, CodeNameTaken, "%s already has a repository with that name", p.Owner)
			}
		}
		return errf(http.StatusBadRequest, CodeInvalid, "GitHub refused the repository: %s", said)
	case http.StatusForbidden, http.StatusNotFound:
		if githubapp.IsPermissionRefusal(err) {
			if st, serr := s.need(ctx); serr == nil {
				switch {
				case p.Via == ViaApp: // the installation's token may not create it
					return s.missingPermission(ctx, st, p.InstallationID, administrationWrite)
				case p.Org != "": // the person's token, through an installation that may not
					return s.missingPermission(ctx, st, p.InstallationID, administrationWrite)
				default:
					// The person's own account: a user's token reaches only the accounts the App is installed on.
					_, ierr := st.app.UserInstallation(ctx, p.Owner)
					switch {
					case githubapp.IsStatus(ierr, http.StatusNotFound):
						e := errf(http.StatusConflict, CodeAppNotInstalled, "the GitHub App is not installed on your account %s, and GitHub lets no repository be made there until it is; install it there first", p.Owner)
						e.InstallURL = installURL(st.rec.HTMLURL)
						return e
					case ierr == nil:
						return s.missingPermission(ctx, st, 0, administrationWrite)
					}
				}
			}
		}
		return errf(http.StatusForbidden, CodeNoPermission, "GitHub refused to create the repository: %s", said)
	case http.StatusUnauthorized:
		if p.Via == ViaUser {
			return s.userFailed(ctx, p.UserID, err)
		}
		return errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App")
	}
	return s.wrap(err)
}

// --- the credential of a space --------------------------------------------------------

// Cred is how a space syncs with its repository.
type Cred struct {
	Via            string
	InstallationID int64 // via "app"
	UserID         string
	RepoID         int64
}

var (
	contentsWrite       = map[string]string{"contents": "write"}
	administrationWrite = map[string]string{"administration": "write"}
)

// hasPermission reports whether a set of granted permissions holds one at
// least at the level wanted.
func hasPermission(have map[string]string, name, want string) bool {
	got := have[name]
	return got != "" && (want != "write" || got == "write")
}

// missingPermission is the answer for a token GitHub refused because the App
// or its installation lacks permissions: it names which of those asked for
// are missing, from what GitHub says the installation has and what was stored
// of the App. When that finds nothing (the stored view is old) it names all
// that were asked for.
func (s *Service) missingPermission(ctx context.Context, st *appState, installation int64, asked map[string]string) *Error {
	// What the administrator sees should not stay as old as the day the App was saved.
	s.autoRecheck(ctx, st)
	if fresh, err := s.state(ctx); err == nil && fresh != nil {
		st = fresh
	}
	var instPerms map[string]string
	instKnown := false
	if installation > 0 {
		if in, err := st.app.Installation(ctx, installation); err == nil {
			instPerms, instKnown = in.Permissions, true
		}
	}
	var names []string
	for name, want := range asked {
		if !hasPermission(st.rec.Permissions, name, want) || (instKnown && !hasPermission(instPerms, name, want)) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		for name := range asked {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	e := errf(http.StatusForbidden, CodeMissingPermission, "the GitHub App or its installation lacks the %s permission; an administrator must add it to the App on GitHub and accept the change in the installation's settings", strings.Join(names, ", "))
	e.Missing = names
	return e
}

// reaches asks for the narrow token a space syncs with: false when the
// installation does not reach the repository (a 404 or 422 of GitHub); an
// App or installation that lacks the permission is an error of its own.
func (s *Service) reaches(ctx context.Context, st *appState, installation, repoID int64) (bool, error) {
	_, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: installation, RepoIDs: []int64{repoID}, Perms: contentsWrite})
	switch {
	case err == nil:
		return true, nil
	case githubapp.IsPermissionRefusal(err):
		return false, s.missingPermission(ctx, st, installation, contentsWrite)
	case githubapp.IsStatus(err, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusForbidden) && !limited(err):
		return false, nil
	case githubapp.IsStatus(err, http.StatusUnauthorized) && !limited(err):
		return false, errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App")
	}
	return false, s.wrap(err)
}

// ChooseCredential picks and checks how a space syncs with a repository the
// plan has just created. An organisation's repository syncs with the
// installation's token; the caller's own account's does too when the App is
// installed there and reaches the repository, else with the caller's token.
// An installation of "selected repositories" that does not reach the new
// repository gets it added with the caller's token.
func (s *Service) ChooseCredential(ctx context.Context, p *Plan, repo *Repo) (*Cred, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, err
	}
	inst := p.InstallationID
	if inst == 0 {
		if p.Org != "" {
			inst, err = st.app.OrgInstallation(ctx, p.Org)
		} else {
			inst, err = st.app.UserInstallation(ctx, p.Owner)
		}
		if err != nil && !githubapp.IsStatus(err, http.StatusNotFound) {
			return nil, s.wrap(err)
		}
		if err != nil {
			inst = 0
		}
	}
	if inst > 0 {
		ok, err := s.reaches(ctx, st, inst, repo.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			if _, tok, cerr := s.connected(ctx, p.UserID); cerr == nil && tok != "" {
				if aerr := s.c.AddRepoToInstallation(ctx, tok, inst, repo.ID); aerr != nil && !githubapp.IsStatus(aerr, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity) {
					return nil, s.userFailed(ctx, p.UserID, aerr)
				} else if aerr == nil {
					if ok, err = s.reaches(ctx, st, inst, repo.ID); err != nil {
						return nil, err
					}
				}
			}
		}
		if ok {
			return &Cred{Via: ViaApp, InstallationID: inst, RepoID: repo.ID}, nil
		}
		if p.InstallationID == 0 { // found by a lookup: forget it, it may have moved
			if p.Org != "" {
				st.app.ForgetLookup("org", p.Org)
			} else {
				st.app.ForgetLookup("user", p.Owner)
			}
		}
	}
	if p.Org != "" {
		e := errf(http.StatusConflict, CodeRepoNotInInstall, "the App's installation on %s does not reach the new repository %s and it could not be added; add it in the installation's settings on GitHub", p.Owner, repo.FullName)
		e.Repo = repo.Ref()
		return nil, e
	}
	return &Cred{Via: ViaUser, UserID: p.UserID, RepoID: repo.ID}, nil
}

// ResolveExisting decides how a space syncs with a repository that exists. via
// is "app", "user" or "" (the server picks: the App when the caller may use
// it, else the caller's own connection). The caller must be allowed to use the
// repository: with via "app" their connection can push to it or the
// installation is shared with their tenant; with via "user" their connection can push.
func (s *Service) ResolveExisting(ctx context.Context, userID, owner, name, via string) (*Cred, *Repo, error) {
	sc, err := s.scopeOf(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return s.resolveExisting(ctx, sc, userID, owner, name, via)
}

// ResolveExistingIn is ResolveExisting for a space of the given tenant: an
// installation counts as shared when it is shared with that tenant, whichever
// tenant the caller belongs to (a site admin may connect another tenant's space).
func (s *Service) ResolveExistingIn(ctx context.Context, tenantID, userID, owner, name, via string) (*Cred, *Repo, error) {
	return s.resolveExisting(ctx, store.InTenant(tenantID), userID, owner, name, via)
}

func (s *Service) resolveExisting(ctx context.Context, sc store.Scope, userID, owner, name, via string) (*Cred, *Repo, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, nil, err
	}
	switch via {
	case ViaApp:
		return s.existingApp(ctx, st, sc, userID, owner, name)
	case ViaUser:
		return s.existingUser(ctx, userID, owner, name)
	}
	cred, repo, aerr := s.existingApp(ctx, st, sc, userID, owner, name)
	if aerr == nil {
		return cred, repo, nil
	}
	var he *Error
	if !errors.As(aerr, &he) || (he.Code != CodeRepoNotInInstall && he.Code != CodeOwnerNotAllowed && he.Code != CodeNotAuthorized && he.Code != CodeMissingPermission) {
		return nil, nil, aerr
	}
	cred, repo, uerr := s.existingUser(ctx, userID, owner, name)
	if uerr == nil {
		return cred, repo, nil
	}
	if errors.As(uerr, &he) && he.Code == CodeNotAuthorized {
		return nil, nil, aerr // the first refusal says more
	}
	return nil, nil, uerr
}

func (s *Service) existingUser(ctx context.Context, userID, owner, name string) (*Cred, *Repo, error) {
	_, tok, err := s.connected(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if tok == "" {
		return nil, nil, notAuthorized()
	}
	r, err := s.c.GetRepo(ctx, tok, owner, name)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
			return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "your GitHub connection cannot push to %s/%s", owner, name)
		}
		return nil, nil, s.userFailed(ctx, userID, err)
	}
	if !r.Permissions.Push {
		return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "your GitHub connection cannot push to %s", r.FullName)
	}
	return &Cred{Via: ViaUser, UserID: userID, RepoID: r.ID}, repoOf(r), nil
}

func (s *Service) existingApp(ctx context.Context, st *appState, sc store.Scope, userID, owner, name string) (*Cred, *Repo, error) {
	inst, err := st.app.RepoInstallation(ctx, owner, name)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound) {
			return nil, nil, errf(http.StatusConflict, CodeRepoNotInInstall, "the App is not installed on %s/%s, or its installation does not reach that repository", owner, name)
		}
		return nil, nil, s.listError(err)
	}
	var repo *githubapp.Repo
	shared, err := store.GitHubInstallationShared(ctx, s.opt.Store.Q(), sc, inst)
	if err != nil {
		return nil, nil, err
	}
	if shared {
		tok, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"metadata": "read"}})
		if err != nil {
			return nil, nil, s.listError(err)
		}
		if repo, err = s.c.GetRepo(ctx, tok, owner, name); err != nil {
			if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
				return nil, nil, errf(http.StatusConflict, CodeRepoNotInInstall, "the App's installation does not reach %s/%s", owner, name)
			}
			return nil, nil, s.wrap(err)
		}
	} else {
		_, tok, err := s.connected(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		if tok == "" {
			return nil, nil, notAuthorized()
		}
		if repo, err = s.c.GetRepo(ctx, tok, owner, name); err != nil {
			if githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
				return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "the installation is not shared and your GitHub connection cannot push to %s/%s", owner, name)
			}
			return nil, nil, s.userFailed(ctx, userID, err)
		}
		if !repo.Permissions.Push {
			return nil, nil, errf(http.StatusForbidden, CodeOwnerNotAllowed, "the installation is not shared and your GitHub connection cannot push to %s", repo.FullName)
		}
	}
	ok, err := s.reaches(ctx, st, inst, repo.ID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		st.app.ForgetLookup("repo", owner+"/"+name) // it may have moved: look again next time
		return nil, nil, errf(http.StatusConflict, CodeRepoNotInInstall, "the App's installation does not reach %s", repo.FullName)
	}
	return &Cred{Via: ViaApp, InstallationID: inst, RepoID: repo.ID}, repoOf(repo), nil
}

// ParseRepoURL reads the address of a repository on this GitHub: https, ssh
// and the scp form (git@host:owner/name.git), with or without ".git".
func (s *Service) ParseRepoURL(raw string) (owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	host := strings.ToLower(s.c.Host())
	var path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || strings.ToLower(u.Host) != host {
			return "", "", false
		}
		switch u.Scheme {
		case "https", "ssh":
		case "http":
			if !strings.HasPrefix(s.c.Web(), "http://") {
				return "", "", false
			}
		default:
			return "", "", false
		}
		if u.Scheme == "ssh" && u.User != nil && u.User.Username() != "git" {
			return "", "", false
		}
		if u.Scheme != "ssh" && u.User != nil {
			return "", "", false
		}
		path = strings.TrimPrefix(u.Path, "/")
	case strings.HasPrefix(raw, "git@") || strings.Contains(raw, ":"):
		h, p, found := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !found || strings.ToLower(h) != strings.ToLower(strings.Split(host, ":")[0]) {
			return "", "", false
		}
		path = p
	default:
		return "", "", false
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	o, n, found := strings.Cut(path, "/")
	if !found || strings.Contains(n, "/") || !validOwner(o) || !repoNameRe.MatchString(n) || n == "." || n == ".." {
		return "", "", false
	}
	return o, n, true
}

// scopeOf is the scope of the tenant a user belongs to: what an installation
// shared with that tenant may be used for. A user that is gone sees nothing.
func (s *Service) scopeOf(ctx context.Context, userID string) (store.Scope, error) {
	t, err := store.UserTenant(ctx, s.opt.Store.Q(), userID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Scope{}, nil
	}
	if err != nil {
		return store.Scope{}, err
	}
	return store.InTenant(t), nil
}
