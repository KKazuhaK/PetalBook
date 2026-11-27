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
	switch r.Via {
	case ViaApp:
		if lost, err := s.lostAccess(ctx, r, checkMember); err != nil {
			return "", offlineErr(err)
		} else if lost != nil {
			return "", lost
		}
		tok, err := st.app.Token(ctx, githubapp.TokenSpec{Installation: r.InstallationID, RepoIDs: []int64{r.RepoID}, Perms: contentsWrite})
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
			// A person's own token syncs only a person's space.
			tenant, personal, err := store.SpaceOwnership(ctx, s.opt.Store.Q(), r.SpaceID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return "", offlineErr(err)
			}
			if err == nil && !connections.ScopeOfSpace(tenant, personal).Personal {
				return "", credErr(CodeAccessLost, "an organisation's space syncs only through the organisation's own GitHub installation, never through a person's own connection; an administrator of the organisation connects GitHub, then the space is connected again", nil)
			}
			ok, err := s.canManage(ctx, r.SpaceID, r.CredUserID)
			if err != nil {
				return "", offlineErr(err)
			}
			if !ok {
				return "", credErr(CodeConnectionMissing, "the GitHub connection this space syncs with belongs to someone who can no longer manage it; connect another", nil)
			}
		}
		tok, err := s.UserToken(ctx, r.CredUserID)
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

// canManage says whether a person may still manage a space: directly or
// through a group (the union of the roles counts), and not disabled.
func (s *Service) canManage(ctx context.Context, spaceID, userID string) (bool, error) {
	roles, disabled, err := store.MemberStanding(ctx, s.opt.Store.Q(), spaceID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var ps perm.Set
	for _, role := range roles {
		rp, _ := perm.ForRole(role)
		ps |= rp
	}
	return !disabled && ps.Has(perm.Manage), nil
}

// lostAccess is the refusal of a remote through an installation that is not
// the space owner's own on this server any more (nil when it is): an
// organisation's space needs the organisation's installation, a person's the
// installation of the person the remote names, who must still be able to
// manage the space. A space being made (staged) has no row yet: the
// installation must still be connected to the owner the credential was
// chosen for.
func (s *Service) lostAccess(ctx context.Context, r *store.Remote, checkMember bool) (*gitstore.CredentialError, error) {
	lost := credErr(CodeAccessLost, lostMessage, nil)
	q := s.opt.Store.Q()
	claim, err := store.GetGitHubInstallation(ctx, q, r.InstallationID)
	if errors.Is(err, store.ErrNotFound) {
		return lost, nil
	}
	if err != nil {
		return nil, err
	}
	if !checkMember {
		if (r.CredUserID != "" && claim.OwnerUser != r.CredUserID) || (r.CredUserID == "" && claim.OwnerTenant == "") {
			return lost, nil
		}
		return nil, nil
	}
	tenant, personal, err := store.SpaceOwnership(ctx, q, r.SpaceID)
	if errors.Is(err, store.ErrNotFound) {
		return lost, nil
	}
	if err != nil {
		return nil, err
	}
	if !connections.ScopeOfSpace(tenant, personal).Personal {
		if claim.OwnerTenant != tenant {
			return lost, nil
		}
		return nil, nil
	}
	if r.CredUserID == "" || claim.OwnerUser != r.CredUserID {
		return lost, nil
	}
	ok, err := s.canManage(ctx, r.SpaceID, r.CredUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return credErr(CodeConnectionMissing, "the GitHub installation this space syncs with belongs to someone who can no longer manage it; connect another", nil), nil
	}
	return nil, nil
}
