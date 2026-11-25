package github

import (
	"context"
	"errors"
	"net/http"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/store"
)

func credErr(code, msg string, err error) *gitstore.CredentialError {
	return &gitstore.CredentialError{Code: code, Msg: msg, Err: err}
}

func offlineErr(err error) *gitstore.CredentialError {
	msg := "could not reach GitHub"
	var he *connections.Error
	if errors.As(err, &he) && he.Code == CodeRateLimited {
		msg = "GitHub is limiting this server for a moment"
	}
	return &gitstore.CredentialError{Msg: msg, Offline: true, Err: err}
}

// GitToken is the token Git is given for a space's remote: an installation
// token narrowed to its repository (via "app"), or the access token of the
// user the remote names (via "user"). Every error is a *gitstore.CredentialError:
// with a Code when the credential cannot be had until someone fixes something,
// Offline when GitHub could not be asked. staging is a space that is being
// made: it has no member yet (it is made for the user the remote names), so
// that is not asked.
func (s *Service) GitToken(ctx context.Context, r *store.Remote, staging bool) (string, error) {
	return s.gitToken(ctx, r, !staging)
}

func (s *Service) gitToken(ctx context.Context, r *store.Remote, checkMember bool) (string, error) {
	st, err := s.state(ctx)
	if err != nil {
		return "", offlineErr(err)
	}
	if st == nil {
		return "", credErr(CodeNotConfigured, "the server's GitHub App is not set up any more; an administrator must set it up again", nil)
	}
	switch r.GHVia {
	case ViaApp:
		tok, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: r.GHInstallationID, RepoIDs: []int64{r.GHRepoID}, Perms: contentsWrite})
		if err == nil {
			return tok, nil
		}
		switch {
		case limited(err):
			return "", offlineErr(s.wrap(err))
		case githubapp.IsStatus(err, http.StatusUnauthorized):
			return "", credErr(CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App", err)
		case githubapp.IsPermissionRefusal(err):
			s.autoRecheck(ctx, st) // the administration should see what the App has now
			return "", credErr(CodeMissingPermission, "the App or its installation lacks the Contents permission; an administrator must add it to the App on GitHub and accept the change in the installation's settings", err)
		case githubapp.IsStatus(err, http.StatusNotFound, http.StatusForbidden, http.StatusUnprocessableEntity):
			return "", credErr(CodeInstallationGone, "the App is no longer installed there, or its installation no longer reaches the repository; install it again or add the repository", err)
		}
		return "", offlineErr(s.wrap(err))
	case ViaUser:
		if checkMember {
			// Manage may come directly or through a group: the union counts.
			roles, disabled, err := store.MemberStanding(ctx, s.opt.Store.Q(), r.SpaceID, r.GHUserID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return "", offlineErr(err)
			}
			var ps perm.Set
			for _, role := range roles {
				rp, _ := perm.ForRole(role)
				ps |= rp
			}
			if errors.Is(err, store.ErrNotFound) || disabled || !ps.Has(perm.Manage) {
				return "", credErr(CodeConnectionMissing, "the GitHub connection this space syncs with belongs to someone who can no longer manage it; connect another", nil)
			}
		}
		tok, err := s.UserToken(ctx, r.GHUserID)
		switch {
		case err == nil:
			return tok, nil
		case errors.Is(err, ErrNoAuthorization):
			return "", credErr(CodeConnectionMissing, "the person whose GitHub connection this space syncs with has disconnected it; they must connect again", err)
		case errors.Is(err, ErrAuthorizationExpired):
			return "", credErr(CodeConnectionExpired, "the GitHub authorisation this space syncs with has expired or was revoked; its owner must connect again", err)
		}
		var he *connections.Error
		if errors.As(err, &he) && he.Code == CodeNotConfigured {
			return "", credErr(CodeNotConfigured, "the server's GitHub App is not set up any more; an administrator must set it up again", err)
		}
		return "", offlineErr(err)
	}
	return "", credErr(CodeNotConfigured, "this space's GitHub credential is not understood by this server", nil)
}
