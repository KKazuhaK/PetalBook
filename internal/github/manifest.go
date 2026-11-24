package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// ManifestCallbackPath is where GitHub sends the browser back after the App is
// made from a manifest.
const ManifestCallbackPath = "/api/connections/github/manifest/callback"

const (
	manifestKind = "manifest"
	// maxAppName is GitHub's limit for an App's name.
	maxAppName = 34
	// Where the browser goes when it is over: the administration's GitHub section.
	manifestDone = "/admin/storage?connection_app=created"
	manifestFail = "/admin/storage?connection_app_error="
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
		return nil, errf(http.StatusConflict, CodePublicURLNeeded, "making the App needs the server's public address (--public-url): GitHub sends the browser back to it")
	}
	org, name = strings.TrimSpace(org), strings.TrimSpace(name)
	if org != "" && !validOwner(org) {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "organization is not a GitHub account name")
	}
	if name == "" {
		name = defaultAppName(s.opt.PublicURL)
	}
	if utf8.RuneCountInString(name) > maxAppName || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "name is at most %d characters on one line", maxAppName)
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
		auths, err := store.CountGitHubAuthorizations(ctx, q)
		if err != nil {
			return nil, err
		}
		if spaces > 0 || auths > 0 {
			return nil, appInUse(spaces, auths)
		}
	}
	state := githubapp.RandomState()
	binding := s.attempts.put(attempt{kind: manifestKind, state: state, userID: userID})
	ret := s.manifestReturn()
	m := githubapp.Manifest{
		Name:               name,
		URL:                s.opt.PublicURL,
		HookAttributes:     githubapp.ManifestHook{URL: s.opt.PublicURL, Active: false},
		RedirectURL:        ret,
		CallbackURLs:       []string{s.CallbackURL()},
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
	_, res := s.attempts.take(manifestKind, binding, state, userID)
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
	secretID := creds.ClientID
	_, err = s.Save(ctx, SaveRequest{AppID: strconv.FormatInt(creds.ID, 10), PrivateKey: creds.PEM, ClientID: &secretID, ClientSecret: creds.ClientSecret, WebhookSecret: creds.WebhookSecret})
	if err != nil {
		var he *connections.Error
		switch {
		case errors.As(err, &he) && he.Code == CodeInUse:
			return fail("app_in_use")
		case errors.As(err, &he) && he.Code == CodeUnavailable:
			return fail("unavailable")
		}
		s.log.Warn("the App GitHub made could not be saved", "reason", reason(err))
		return fail("provider_error")
	}
	return manifestDone, true
}

// RetryManifest saves an App GitHub made but the server could not save at the time.
func (s *Service) RetryManifest(ctx context.Context, userID string) (any, error) {
	return nil, errf(http.StatusNotFound, CodeNotFound, "no App is waiting to be saved")
}
