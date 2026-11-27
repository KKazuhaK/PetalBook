package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// connected is the user's connection when it is usable, with its token; nil
// when there is none or it has expired.
func (s *Service) connected(ctx context.Context, userID string) (*store.RepoLink, string, error) {
	row, err := store.GetRepoLink(ctx, s.opt.Store.Q(), ProviderID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if row.State != store.LinkOK {
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
		if row, gerr := store.GetRepoLink(ctx, s.opt.Store.Q(), ProviderID, userID); gerr == nil {
			s.expire(ctx, row)
		}
		return notConnected()
	}
	return s.wrap(err)
}

// noAppFor is how long "the App is not installed for this person" is
// remembered, so reading the owners again and again costs GitHub no call.
const noAppFor = time.Minute

// maxNoApp bounds the logins remembered as having no App.
const maxNoApp = 10_000

// notInstalledFor reports whether the App is not installed on the account of
// login. A "no" from GitHub is remembered for a minute; a "yes" is remembered
// by the App's own lookup while the App is there.
func (s *Service) notInstalledFor(ctx context.Context, st *appState, login string) bool {
	key := strings.ToLower(login)
	now := s.opt.Now()
	s.mu.Lock()
	until, ok := s.noApp[key]
	s.mu.Unlock()
	if ok && now.Before(until) {
		return true
	}
	_, err := st.app.UserInstallation(ctx, login)
	missing := githubapp.IsStatus(err, http.StatusNotFound)
	s.mu.Lock()
	if missing {
		if len(s.noApp) >= maxNoApp {
			for k, t := range s.noApp {
				if !now.Before(t) {
					delete(s.noApp, k)
				}
			}
			if len(s.noApp) >= maxNoApp {
				s.noApp = map[string]time.Time{}
			}
		}
		s.noApp[key] = now.Add(noAppFor)
	} else {
		delete(s.noApp, key)
	}
	s.mu.Unlock()
	return missing
}

// forgetNoApp drops the memory of the App not being installed for a login: it
// is known to be, or the person has just connected.
func (s *Service) forgetNoApp(login string) {
	s.mu.Lock()
	delete(s.noApp, strings.ToLower(login))
	s.mu.Unlock()
}

// recheckEvery is how often one person may make the next Owners ask GitHub
// again: each ask costs a call of the App's rate limit, which everyone shares.
const recheckEvery = time.Minute

// maxRechecked bounds the people remembered as having just rechecked.
const maxRechecked = 10_000

// RecheckPerson forgets that the App was not installed on the person's own account,
// for a person who has installed it since (the next Owners asks GitHub). It
// is allowed once a minute per person; within the minute it changes nothing
// and the answer stays the remembered one.
func (s *Service) RecheckPerson(ctx context.Context, userID string) {
	now := s.opt.Now()
	s.mu.Lock()
	if until, ok := s.rechecked[userID]; ok && now.Before(until) {
		s.mu.Unlock()
		return
	}
	if len(s.rechecked) >= maxRechecked {
		for k, t := range s.rechecked {
			if !now.Before(t) {
				delete(s.rechecked, k)
			}
		}
		if len(s.rechecked) >= maxRechecked {
			// All are recent: refuse this one rather than forget the others.
			s.mu.Unlock()
			return
		}
	}
	s.rechecked[userID] = now.Add(recheckEvery)
	s.mu.Unlock()
	if row, err := store.GetRepoLink(ctx, s.opt.Store.Q(), ProviderID, userID); err == nil && row != nil {
		s.forgetNoApp(row.Login)
	}
}

// --- creating a repository -----------------------------------------------------------

const maxOwnerPages = 10

var ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func validOwner(s string) bool { return ownerRe.MatchString(s) }

// ValidRepoName checks a repository name: what GitHub keeps as it is.
func ValidRepoName(name string) bool {
	return repoNameRe.MatchString(name) && name != "." && name != ".." && !strings.HasSuffix(strings.ToLower(name), ".git")
}

func repoOf(r *githubapp.Repo) *connections.Repo {
	owner, name, _ := strings.Cut(r.FullName, "/")
	return &connections.Repo{ID: r.ID, Owner: owner, Name: name, FullName: r.FullName, HTMLURL: r.HTMLURL, Private: r.Private}
}

// CreateRepo creates the empty repository the plan describes.
func (s *Service) CreateRepo(ctx context.Context, p *connections.Plan, n connections.NewRepoSpec) (*connections.Repo, error) {
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
				return nil, errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App.")
			case githubapp.IsPermissionRefusal(err):
				return nil, s.missingPermission(ctx, st, p.InstallationID, administrationWrite)
			case githubapp.IsStatus(err, http.StatusUnprocessableEntity, http.StatusForbidden, http.StatusNotFound) && !limited(err):
				return nil, errf(http.StatusForbidden, CodeRefused, "The App is not installed there, or GitHub refused it a token for the installation.")
			}
			return nil, s.wrap(err)
		}
	}
	r, err := s.c.CreateRepo(ctx, token, p.Org, githubapp.NewRepo{Name: n.Name, Private: n.Private, Description: n.Description})
	if err != nil {
		return nil, s.createError(ctx, p, err)
	}
	s.forgetNoApp(p.Owner) // it worked: the App is there
	return repoOf(r), nil
}

// tokenError maps what UserToken fails with.
func (s *Service) tokenError(err error) error {
	switch {
	case errors.Is(err, ErrNoAuthorization), errors.Is(err, ErrAuthorizationExpired):
		return notConnected()
	}
	return s.wrap(err)
}

func (s *Service) createError(ctx context.Context, p *connections.Plan, err error) error {
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
				return errf(http.StatusConflict, CodeNameTaken, "%s already has a repository with that name.", p.Owner)
			}
		}
		return errf(http.StatusBadRequest, CodeInvalid, "GitHub refused the repository: %s.", said)
	case http.StatusForbidden, http.StatusNotFound:
		if githubapp.IsPermissionRefusal(err) {
			if st, serr := s.need(ctx); serr == nil {
				switch {
				case p.Via == ViaApp: // the installation's token may not create it
					return s.missingPermission(ctx, st, p.InstallationID, administrationWrite)
				case p.Org != "":
					// The person's own token, refused in an organisation: the App may not
					// be installed there (then it is not a permission the App lacks), or
					// it is, and the installation lacks the permission.
					st.app.ForgetLookup("org", p.Org)
					_, ierr := st.app.OrgInstallation(ctx, p.Org)
					switch {
					case githubapp.IsStatus(ierr, http.StatusNotFound):
						e := errf(http.StatusConflict, CodeAppNotInstalled, "The GitHub App is not installed on %s, and GitHub lets no repository be made there until it is; an owner of %s installs it first.", p.Owner, p.Owner)
						e.InstallURL = installURL(st.rec.HTMLURL)
						return e
					case ierr == nil:
						return s.missingPermission(ctx, st, p.InstallationID, administrationWrite)
					}
				default:
					// The person's own account: a user's token reaches only the accounts the App is installed on.
					_, ierr := st.app.UserInstallation(ctx, p.Owner)
					switch {
					case githubapp.IsStatus(ierr, http.StatusNotFound):
						e := errf(http.StatusConflict, CodeAppNotInstalled, "The GitHub App is not installed on your account %s, and GitHub lets no repository be made there until it is; install it there first.", p.Owner)
						e.InstallURL = installURL(st.rec.HTMLURL)
						return e
					case ierr == nil:
						return s.missingPermission(ctx, st, 0, administrationWrite)
					}
				}
			}
		}
		return errf(http.StatusForbidden, CodeRefused, "GitHub refused to create the repository: %s.", said)
	case http.StatusUnauthorized:
		if p.Via == ViaUser {
			return s.userFailed(ctx, p.UserID, err)
		}
		return errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App.")
	}
	return s.wrap(err)
}

// --- the credential of a space --------------------------------------------------------

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
func (s *Service) missingPermission(ctx context.Context, st *appState, installation int64, asked map[string]string) *connections.Error {
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
	e := errf(http.StatusForbidden, CodeMissingPermission, "The GitHub App or its installation lacks the %s permission; an administrator must add it to the App on GitHub and accept the change in the installation's settings.", strings.Join(names, ", "))
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
		return false, errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App.")
	}
	return false, s.wrap(err)
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
