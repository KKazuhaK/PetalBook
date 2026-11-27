package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// ManifestCallbackPath is where GitHub sends the browser back after the App is
// made from a manifest.
const ManifestCallbackPath = "/api/providers/github/manifest/callback"

const (
	manifestKind = "manifest"
	// maxAppName is GitHub's limit for an App's name.
	maxAppName = 34
	// Where the browser goes when it is over: the administration's GitHub section.
	manifestDone = "/admin/storage/connections?connection_app=created"
	manifestFail = "/admin/storage/connections?connection_app_error="
)

// manifestReturn is where GitHub sends the browser back to; "" without a public URL.
func (s *Service) manifestReturn() string {
	if s.opt.PublicURL == "" {
		return ""
	}
	return s.opt.PublicURL + ManifestCallbackPath
}

func defaultAppName(publicURL string) string {
	host := publicURL
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		host = u.Host
	}
	name := "PetalBook " + host
	if utf8.RuneCountInString(name) > maxAppName {
		name = string([]rune(name)[:maxAppName])
	}
	return strings.TrimSpace(name)
}

// StartManifest begins making the App on GitHub in one click (the App Manifest
// flow). org, when given, makes it in that organisation; name is the App's name
// (a default from the server's address when empty). The attempt is bound to
// the browser and to the administrator, single use, and lasts ten minutes.
func (s *Service) StartManifest(ctx context.Context, userID string, req connections.ManifestRequest) (*connections.ManifestStart, error) {
	org, name := req.Organization, req.Name
	if s.opt.PublicURL == "" {
		return nil, errf(http.StatusConflict, CodePublicURLNeeded, "Making the App needs the server's public address (--public-url): GitHub sends the browser back to it.")
	}
	org, name = strings.TrimSpace(org), strings.TrimSpace(name)
	if org != "" && !validOwner(org) {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "Organization is not a GitHub account name.")
	}
	if name == "" {
		name = defaultAppName(s.opt.PublicURL)
	}
	if utf8.RuneCountInString(name) > maxAppName || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "Name is at most %d characters on one line.", maxAppName)
	}
	// An App in use is not replaced: stop before anything is made on GitHub.
	if st, err := s.state(ctx); err != nil {
		return nil, err
	} else if st != nil {
		q := s.opt.Store.Q()
		spaces, err := store.CountGitHubSpaces(ctx, q)
		if err != nil {
			return nil, err
		}
		auths, err := store.CountProviderLinks(ctx, q, ProviderID)
		if err != nil {
			return nil, err
		}
		if spaces > 0 || auths > 0 {
			return nil, appInUse(spaces, auths)
		}
	}
	state := githubapp.RandomState()
	binding := s.manifests.put(attempt{kind: manifestKind, state: state, userID: userID})
	ret := s.manifestReturn()
	m := githubapp.Manifest{
		Name:               name,
		URL:                s.opt.PublicURL,
		HookAttributes:     githubapp.ManifestHook{URL: s.opt.PublicURL + WebhookPath, Active: true},
		RedirectURL:        ret,
		CallbackURLs:       []string{s.CallbackURL(), s.SigninCallbackURL()},
		SetupURL:           ret,
		Public:             true,
		RequestOAuthOnInst: true,
		DefaultPermissions: map[string]string{"administration": "write", "contents": "write", "metadata": "read"},
	}
	return &connections.ManifestStart{URL: s.c.ManifestURL(org, state), Manifest: m, Binding: binding}, nil
}

// CompleteManifest finishes the flow from GitHub's redirect: it trades the code
// for the App's credentials and saves the App, checked and sealed exactly as
// one entered by hand. It never fails: every outcome is a path to redirect to.
// userID is the administrator of the browser's session ("" without one, or when
// that user is no site administrator); clearBinding says the attempt is over.
func (s *Service) CompleteManifest(ctx context.Context, binding, userID string, q url.Values) (redirect string, clearBinding bool) {
	one := func(k string) string {
		if v := q[k]; len(v) == 1 {
			return v[0]
		}
		return ""
	}
	code, state := one("code"), one("state")
	if code == "" && (one("setup_action") != "" || one("installation_id") != "") {
		// The page GitHub sends people to after installing (the manifest names
		// this address for it too): nothing to do here, show their account.
		return "/settings/connections", false
	}
	fail := func(c string) (string, bool) { return manifestFail + c, true }
	if binding == "" || state == "" || len(q["code"]) > 1 || len(q["state"]) > 1 {
		return manifestFail + "expired", false
	}
	_, res := s.manifests.take(binding, state, userID, manifestKind)
	if res != takeOK {
		return manifestFail + "expired", res == takeUnknown
	}
	if code == "" {
		return fail("provider_error")
	}
	creds, err := s.c.ConvertManifest(ctx, code)
	if err != nil {
		s.log.Warn("GitHub did not turn the manifest into an App", "reason", reason(err))
		return fail("provider_error")
	}
	_, err = s.saveMade(ctx, creds)
	if err != nil {
		var he *connections.Error
		switch {
		case errors.As(err, &he) && he.Code == CodeInUse:
			return fail("app_in_use")
		case errors.As(err, &he) && he.Code == CodeUnavailable:
			// GitHub made the App but could not be asked about it: it exists there
			// and what it gave is kept, sealed, for a retry.
			to := manifestFail + "unavailable"
			if s.keepPending(userID, creds) {
				to += "&pending=1"
			}
			return to + "&app=" + url.QueryEscape(creds.Slug), true
		}
		s.log.Warn("the App GitHub made could not be saved", "reason", reason(err))
		return fail("provider_error")
	}
	return manifestDone, true
}

// saveMade saves the App GitHub made, as one entered by hand.
func (s *Service) saveMade(ctx context.Context, creds *githubapp.AppCredentials) (*AppView, error) {
	secretID := creds.ClientID
	return s.Save(ctx, SaveRequest{AppID: strconv.FormatInt(creds.ID, 10), PrivateKey: creds.PEM, ClientID: &secretID, ClientSecret: creds.ClientSecret, WebhookSecret: creds.WebhookSecret})
}

// pendingTTL is how long an App GitHub made but the server could not save is
// kept for a retry; maxPending bounds how many administrators have one.
const (
	pendingTTL     = 10 * time.Minute
	maxPending     = 16
	pendingPurpose = "github-manifest-pending"
)

// pendingApp is what GitHub gave for an App that could not be saved yet. The
// credentials are sealed even in memory.
type pendingApp struct {
	sealed  string // the AppCredentials as JSON, sealed for the administrator
	expires time.Time
}

// keepPending remembers the App for the administrator; false when it could not be kept.
func (s *Service) keepPending(userID string, creds *githubapp.AppCredentials) bool {
	if userID == "" {
		return false
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return false
	}
	sealed, err := s.opt.Secrets.Seal(userID, pendingPurpose, raw)
	if err != nil {
		return false
	}
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, p := range s.pending {
		if !now.Before(p.expires) {
			delete(s.pending, k)
		}
	}
	if _, replaced := s.pending[userID]; !replaced && len(s.pending) >= maxPending {
		return false
	}
	s.pending[userID] = &pendingApp{sealed: sealed, expires: now.Add(pendingTTL)}
	return true
}

// RetryManifest saves the App GitHub made for the administrator when the
// server could not save it then. It is not_found when none is waiting (it was
// used, or ten minutes passed); while GitHub still cannot be asked it stays.
func (s *Service) RetryManifest(ctx context.Context, userID string) (any, error) {
	now := s.opt.Now()
	// The entry is taken, not just read: of two retries at once only one gets
	// it, so the App is saved once.
	s.mu.Lock()
	p := s.pending[userID]
	delete(s.pending, userID)
	s.mu.Unlock()
	if p == nil || !now.Before(p.expires) {
		return nil, errf(http.StatusNotFound, CodeNotFound, "No App is waiting to be saved.")
	}
	raw, err := s.opt.Secrets.Open(userID, pendingPurpose, p.sealed)
	var creds githubapp.AppCredentials
	if err == nil {
		err = json.Unmarshal(raw, &creds)
	}
	if err != nil {
		return nil, errf(http.StatusNotFound, CodeNotFound, "No App is waiting to be saved.")
	}
	v, err := s.saveMade(ctx, &creds)
	var he *connections.Error
	if err != nil && errors.As(err, &he) && he.Code == CodeUnavailable {
		// Still pending: put it back, unless the administrator has made another meanwhile.
		s.mu.Lock()
		if s.pending[userID] == nil {
			s.pending[userID] = p
		}
		s.mu.Unlock()
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return toAdminView(v), nil
}
