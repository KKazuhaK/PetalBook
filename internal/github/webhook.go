package github

// GitHub's webhook: the App's signed events. The server reads one kind, the
// installation events, for who installed each installation (created) and for
// the installations that are gone (deleted). An organisation's installation is
// claimed here only by the GitHub user who installed it, as that event's
// sender says: only an organisation's owners install an App on it (a member's
// request needs an owner's approval, and the approver is then the sender), so
// it proves an owner's act without asking GitHub for the organisation's
// members.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// WebhookPath is where GitHub sends the App's events (the manifest names it,
// under the public URL).
const WebhookPath = "/api/github/webhook"

// MaxWebhookBody bounds a delivery's body. An installation event lists the
// repositories it reaches; 2 MiB is some 15,000 of them.
const MaxWebhookBody = 2 << 20

// WebhookURL is where GitHub sends the App's events; "" without a public URL.
func (s *Service) WebhookURL() string {
	if s.opt.PublicURL == "" {
		return ""
	}
	return s.opt.PublicURL + WebhookPath
}

// Claimed is an installation a webhook connected to its owner: the claim made
// on the callback before GitHub said who installed it.
type Claimed struct {
	Owner     store.InstallOwner
	ID        int64
	Account   string
	ClaimedBy string
}

// Uninstalled is an installation GitHub says is gone, and what it was here.
type Uninstalled struct {
	Owner  store.InstallOwner // zero when no one here had it
	ID     int64
	Spaces []string
}

// WebhookResult is what a delivery did, for the caller's audit.
type WebhookResult struct {
	Claimed     []Claimed
	Uninstalled *Uninstalled
}

// Webhook takes one delivery of GitHub's webhook: its X-GitHub-Event, its
// X-Hub-Signature-256 and its body. The signature is checked, with the App's
// webhook secret and in constant time, before anything of the body is read.
// Events other than installation's created and deleted do nothing. A created
// event is taken only when the caller's NewInstalls hook says so (the site's
// switch); a deleted one always is. A pending claim it completes is asked of
// the caller's Recheck hook first. Either hook missing refuses.
func (s *Service) Webhook(ctx context.Context, event, signature string, body []byte) (*WebhookResult, error) {
	st, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	if st == nil || st.hook == "" {
		return nil, errf(http.StatusConflict, CodeNotConfigured, "This server keeps no webhook secret of a GitHub App: no delivery is believed.")
	}
	if !githubapp.VerifySignature(st.hook, body, signature) {
		s.log.Warn("a GitHub webhook delivery was refused: its signature does not match")
		return nil, errf(http.StatusUnauthorized, "unauthenticated", "The delivery's signature does not match the App's webhook secret.")
	}
	res := &WebhookResult{}
	if event != "installation" {
		return res, nil
	}
	var ev githubapp.InstallationEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "The installation event is not JSON GitHub sends.")
	}
	id := ev.Installation.ID
	switch ev.Action {
	case "created":
		a := ev.Installation.Account
		if id <= 0 || a.ID <= 0 || ev.Sender.ID <= 0 || (a.Type != "User" && a.Type != "Organization") {
			return nil, errf(http.StatusBadRequest, CodeInvalid, "The installation event names no installation, account or sender.")
		}
		h := connections.InstallHooksOf(ctx)
		if h.NewInstalls == nil {
			return nil, errf(http.StatusForbidden, CodeForbidden, "New installations are not taken here.")
		}
		if err := h.NewInstalls(ctx); err != nil {
			return nil, err
		}
		// An installation connected here already has had its one claim: a
		// redelivery does not record who installed it again (the record a
		// claim used up stays used).
		if _, err := store.GetGitHubInstallation(ctx, s.opt.Store.Q(), id); err == nil {
			return res, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		now := s.opt.Now()
		inst := store.GitHubInstaller{InstallationID: id, AccountID: a.ID, Account: a.Login, AccountType: a.Type,
			SenderID: ev.Sender.ID, Sender: ev.Sender.Login, Created: now}
		if err := store.PutGitHubInstaller(ctx, s.opt.Store.Q(), inst); err != nil {
			return nil, err
		}
		waiting, err := store.WaitingGitHubClaims(ctx, s.opt.Store.Q(), id, now)
		if err != nil {
			return nil, err
		}
		for _, w := range waiting {
			p, err := store.TakeGitHubPendingClaim(ctx, s.opt.Store.Q(), id, w.UserID, now)
			if errors.Is(err, store.ErrNotFound) {
				continue // the callback that saw this event completes it
			}
			if err != nil {
				return nil, err
			}
			if c := s.settle(ctx, p, &inst, true); c == "" {
				res.Claimed = append(res.Claimed, Claimed{Owner: p.Owner, ID: id, Account: p.Account, ClaimedBy: p.UserID})
			}
		}
	case "deleted":
		if id <= 0 {
			return nil, errf(http.StatusBadRequest, CodeInvalid, "The installation event names no installation.")
		}
		u := &Uninstalled{ID: id}
		if err := store.DeleteGitHubInstaller(ctx, s.opt.Store.Q(), id); err != nil {
			return nil, err
		}
		in, err := store.GetGitHubInstallation(ctx, s.opt.Store.Q(), id)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return nil, err
		default:
			ids, err := s.disconnect(ctx, in.Owner(), id)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if err == nil {
				u.Owner, u.Spaces = in.Owner(), ids
			}
		}
		res.Uninstalled = u
	}
	return res, nil
}

// settle completes a pending claim taken for completion, now that GitHub said
// who installed the installation: connected when the claimer is that user and,
// with recheck (the webhook, minutes after the callback asked), may still claim
// for the owner. It answers the code it failed with, "" when connected; the
// claim is removed, or kept with its code until it expires.
func (s *Service) settle(ctx context.Context, p *store.GitHubPendingClaim, inst *store.GitHubInstaller, recheck bool) string {
	code := ""
	switch {
	case inst.SenderID != p.ClaimerGitHub || inst.AccountID != p.AccountID:
		code = cbNotInstaller
	case p.Owner.TenantID != "" && !s.appMayClaimOrgs(ctx):
		code = CodeMissingPermission
	case recheck && !s.mayStill(ctx, p):
		code = cbForbidden
	default:
		code = s.claim(ctx, p.Owner, p.UserID, p.InstallationID, githubapp.Account{ID: p.AccountID, Login: p.Account, Type: p.AccountType})
	}
	if err := store.EndGitHubPendingClaim(ctx, s.opt.Store.Q(), p.InstallationID, p.UserID, code); err != nil {
		s.log.Error("could not end a pending GitHub claim", "error", err)
	}
	return code
}

// appMayClaimOrgs says whether the App asks for Repository administration:
// only then can only an organisation's owners install it there (GitHub lets a
// member install an App that does not), which is what an organisation's
// claim by its installer rests on.
func (s *Service) appMayClaimOrgs(ctx context.Context) bool {
	st, err := s.state(ctx)
	if err != nil || st == nil {
		return false
	}
	return !slices.Contains(missingPermissions(st.rec.Permissions), "administration")
}

// mayStill asks the caller's Recheck hook whether the claimer may still claim
// for the claim's owner; no hook is a refusal.
func (s *Service) mayStill(ctx context.Context, p *store.GitHubPendingClaim) bool {
	h := connections.InstallHooksOf(ctx)
	if h.Recheck == nil {
		return false
	}
	if err := h.Recheck(ctx, p.Owner, p.UserID); err != nil {
		s.log.Info("a pending GitHub claim was refused: its claimer may no longer claim", "installation", p.InstallationID, "reason", err.Error())
		return false
	}
	return true
}

// claim connects an installation to its owner; it answers a callback failure
// code, "" when connected.
func (s *Service) claim(ctx context.Context, o store.InstallOwner, claimer string, id int64, acct githubapp.Account) string {
	now := s.opt.Now()
	err := store.ClaimGitHubInstallation(ctx, s.opt.Store.Q(), store.GitHubInstallation{ID: id, Account: acct.Login, AccountID: acct.ID,
		AccountType: acct.Type, OwnerUser: o.UserID, OwnerTenant: o.TenantID, ClaimedBy: claimer, Created: now, Updated: now})
	switch {
	case errors.Is(err, store.ErrInstallationTaken):
		return cbTaken
	case err != nil:
		s.log.Error("could not connect a GitHub installation", "error", err)
		return cbError
	}
	// Who installed it proved this one claim: a later one (after a disconnect)
	// needs the App installed afresh on GitHub.
	if err := store.ForgetGitHubInstaller(ctx, s.opt.Store.Q(), id); err != nil {
		s.log.Error("could not forget who installed a GitHub installation", "error", err)
	}
	s.forgetNoApp(acct.Login)
	s.changed("") // spaces that lost access through it are fetched again
	return ""
}

// PendingClaim is an organisation's claim that waits for GitHub's webhook, or
// that failed when it came (Error), as its owner sees it until it expires.
type PendingClaim struct {
	ID        int64
	Account   string
	ClaimedBy string
	Started   string // RFC 3339
	Expires   string // RFC 3339
	Error     string // "" while it waits
}

// PendingClaims lists the owner's claims that have not expired.
func (s *Service) PendingClaims(ctx context.Context, o store.InstallOwner) ([]PendingClaim, error) {
	rows, err := store.ListGitHubPendingClaimsOwned(ctx, s.opt.Store.Q(), o, s.opt.Now())
	if err != nil {
		return nil, err
	}
	out := make([]PendingClaim, 0, len(rows))
	for _, r := range rows {
		e := r.State
		if e == store.PendingClaiming {
			e = ""
		}
		out = append(out, PendingClaim{ID: r.InstallationID, Account: r.Account, ClaimedBy: r.UserID,
			Started: r.Created.UTC().Format("2006-01-02T15:04:05Z"), Expires: r.Expires.UTC().Format("2006-01-02T15:04:05Z"), Error: e})
	}
	return out, nil
}

// claimOrg is the organisation's branch of the setup callback, after GitHub
// listed the installation for the claimer's own token on the organisation
// asked for: who is the claimer on GitHub (who), and whether GitHub says the
// installation was just made (action "install").
func (s *Service) claimOrg(ctx context.Context, a *attempt, in *githubapp.Installation, who githubapp.User, action string) (to, code string) {
	if !s.appMayClaimOrgs(ctx) {
		return "", CodeMissingPermission
	}
	q := s.opt.Store.Q()
	inst, err := store.GetGitHubInstaller(ctx, q, in.ID)
	switch {
	case err == nil:
		if inst.SenderID != who.ID {
			return "", cbNotInstaller
		}
		return "", s.claim(ctx, a.owner, a.userID, in.ID, in.Account)
	case !errors.Is(err, store.ErrNotFound):
		s.log.Error("could not read who installed a GitHub installation", "error", err)
		return "", cbError
	}
	// No record: connected here already (its claim used the record up), it is
	// someone else's, or already the owner's (nothing new to grant).
	if held, err := store.GetGitHubInstallation(ctx, q, in.ID); err == nil {
		if held.Owner() != a.owner {
			return "", cbTaken
		}
		return "", ""
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Error("could not read a GitHub installation", "error", err)
		return "", cbError
	}
	// No webhook said who installed it. When GitHub says it was just
	// installed, the event may still be on its way: the claim waits for it.
	// Otherwise none is coming.
	if action != "install" {
		return "", cbNotInstaller
	}
	now := s.opt.Now()
	p := store.GitHubPendingClaim{InstallationID: in.ID, UserID: a.userID, Owner: a.owner, ClaimerGitHub: who.ID,
		AccountID: in.Account.ID, Account: in.Account.Login, AccountType: in.Account.Type, Created: now, Expires: now.Add(attemptTTL)}
	if err := store.PutGitHubPendingClaim(ctx, q, p); err != nil {
		s.log.Error("could not keep a pending GitHub claim", "error", err)
		return "", cbError
	}
	// The event may have come between the read above and the write: the
	// webhook reads the claims after it writes who installed it, the callback
	// reads who installed it after it writes the claim, so one of the two sees
	// the other.
	inst, err = store.GetGitHubInstaller(ctx, q, in.ID)
	if err != nil {
		return withParam(a.ret, "connection_pending", in.Account.Login), ""
	}
	taken, err := store.TakeGitHubPendingClaim(ctx, q, in.ID, a.userID, now)
	if err != nil {
		// The webhook has it: the list shows how it ends.
		return withParam(a.ret, "connection_pending", in.Account.Login), ""
	}
	if c := s.settle(ctx, taken, inst, false); c != "" {
		return "", c
	}
	return "", ""
}

// withParam is ret with k=v added.
func withParam(ret, k, v string) string {
	sep := "?"
	if strings.Contains(ret, "?") {
		sep = "&"
	}
	return ret + sep + k + "=" + url.QueryEscape(v)
}
