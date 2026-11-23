package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// RequiredPermissions are what the App needs, and the level of each.
var RequiredPermissions = map[string]string{"administration": "write", "contents": "write", "metadata": "read"}

// AppView is the App's settings as an administrator sees them. No secret is in it.
type AppView struct {
	Configured       bool
	AppID            string
	ClientID         string
	Slug, Name       string
	Owner            string
	HTMLURL          string
	InstallURL       string
	KeySet           bool
	Fingerprint      string
	SecretSet        bool
	CallbackURL      string
	Permissions      map[string]string
	Missing          []string
	UserAuthorizaton bool
	Checked          time.Time
	Spaces           int
	Authorizations   int
}

func missingPermissions(have map[string]string) []string {
	out := []string{}
	for _, k := range []string{"administration", "contents", "metadata"} {
		want, got := RequiredPermissions[k], have[k]
		if got == "" || (want == "write" && got != "write") {
			out = append(out, k)
		}
	}
	return out
}

// View is what the administrator sees of the App.
func (s *Service) View(ctx context.Context) (*AppView, error) {
	st, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	v := &AppView{CallbackURL: s.CallbackURL()}
	if st == nil {
		return v, nil
	}
	q := s.opt.Store.Q()
	spaces, err := store.CountGitHubSpaces(ctx, q)
	if err != nil {
		return nil, err
	}
	auths, err := store.CountGitHubAuthorizations(ctx, q)
	if err != nil {
		return nil, err
	}
	r := st.rec
	v.Configured, v.AppID, v.ClientID, v.Slug, v.Name, v.Owner, v.HTMLURL = true, r.AppID, r.ClientID, r.Slug, r.Name, r.Owner, r.HTMLURL
	v.InstallURL = installURL(r.HTMLURL)
	v.KeySet, v.Fingerprint, v.SecretSet = true, r.Fingerprint, r.SecretEnc != ""
	v.Permissions, v.Missing = r.Permissions, missingPermissions(r.Permissions)
	v.UserAuthorizaton = st.userAuth(s.opt.PublicURL)
	v.Checked, v.Spaces, v.Authorizations = r.Checked, spaces, auths
	return v, nil
}

func installURL(htmlURL string) string {
	if htmlURL == "" {
		return ""
	}
	return strings.TrimRight(htmlURL, "/") + "/installations/new"
}

// SaveRequest is a change to the App's settings; a nil or empty field leaves
// what is stored (ClientID "" removes the client id and secret).
type SaveRequest struct {
	AppID        string
	PrivateKey   string
	ClientID     *string
	ClientSecret string
}

func appInUse(spaces, auths int) *Error {
	var parts []string
	if spaces > 0 {
		parts = append(parts, strconv.Itoa(spaces)+" space(s) sync through the App")
	}
	if auths > 0 {
		parts = append(parts, strconv.Itoa(auths)+" user(s) are connected")
	}
	return errf(http.StatusConflict, CodeAppInUse, "another GitHub App cannot be set while %s: switch or disconnect those first", strings.Join(parts, " and "))
}

// Save checks the App with GitHub and stores it. It saves nothing when a check
// fails.
func (s *Service) Save(ctx context.Context, req SaveRequest) (*AppView, error) {
	old, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	appID := strings.TrimSpace(req.AppID)
	keyPEM := strings.TrimSpace(req.PrivateKey)
	clientID, secretNew := "", strings.TrimSpace(req.ClientSecret)
	var secret string
	if old != nil {
		if appID == "" {
			appID = old.rec.AppID
		}
		clientID, secret = old.rec.ClientID, old.secret
	}
	if req.ClientID != nil {
		clientID = strings.TrimSpace(*req.ClientID)
	}
	if secretNew != "" {
		secret = secretNew
	}
	if old == nil && (appID == "" || keyPEM == "") {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "the first save needs appId and privateKey")
	}
	if appID == "" || strings.ContainsAny(appID, " \t\r\n/") {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "appId is missing or malformed")
	}
	if clientID == "" {
		secret = ""
	} else if secret == "" || (old != nil && clientID != old.rec.ClientID && secretNew == "") {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "clientId and clientSecret go together")
	}
	var pemBytes []byte
	if keyPEM != "" {
		pemBytes = []byte(keyPEM)
	} else if old != nil {
		if pemBytes, err = s.opt.Secrets.Open(siteBinding, purposeKey, old.rec.KeyEnc); err != nil {
			return nil, err
		}
	}
	key, err := githubapp.ParseKey(pemBytes)
	if err != nil {
		return nil, errf(http.StatusBadRequest, CodeKeyInvalid, "the private key must be an RSA key in PEM form (PKCS#1 or PKCS#8): %s", err.Error())
	}
	if len(keyPEM) > 16<<10 || len(secret) > 1024 || len(clientID) > 256 {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "a value is too long")
	}
	// Another App strands everything made with this one. Another (or no)
	// client id only strands the users' connections: spaces syncing through an
	// installation need the key alone, and a space via a user needs a connection.
	appChanged := old != nil && appID != old.rec.AppID
	clientChanged := old != nil && old.rec.ClientID != "" && clientID != old.rec.ClientID
	if appChanged || clientChanged {
		q := s.opt.Store.Q()
		spaces := 0
		if appChanged {
			if spaces, err = store.CountGitHubSpaces(ctx, q); err != nil {
				return nil, err
			}
		}
		auths, err := store.CountGitHubAuthorizations(ctx, q)
		if err != nil {
			return nil, err
		}
		if spaces > 0 || auths > 0 {
			if !appChanged {
				return nil, errf(http.StatusConflict, CodeAppInUse, "the client id cannot be changed or removed while %d user(s) are connected: they disconnect first", auths)
			}
			return nil, appInUse(spaces, auths)
		}
	}
	app, err := s.c.NewApp(appID, key)
	if err != nil {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "%s", err.Error())
	}
	info, err := app.AppInfo(ctx)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusUnauthorized, http.StatusNotFound, http.StatusForbidden) && !limited(err) {
			return nil, errf(http.StatusBadRequest, CodeAppRejected, "GitHub does not accept this private key for App %s", appID)
		}
		return nil, s.unreachable(err)
	}
	if strconv.FormatInt(info.ID, 10) != appID && info.ClientID != appID {
		return nil, errf(http.StatusBadRequest, CodeAppRejected, "the key belongs to App %d, not %s", info.ID, appID)
	}
	if clientID != "" {
		if info.ClientID != clientID {
			return nil, errf(http.StatusBadRequest, CodeClientRejected, "%s is not the client id of this App", clientID)
		}
		ok, err := s.c.CheckClient(ctx, clientID, secret)
		if err != nil {
			return nil, s.unreachable(err)
		}
		if !ok {
			return nil, errf(http.StatusBadRequest, CodeClientRejected, "GitHub does not accept this client id and secret")
		}
	}
	rec := appRecord{AppID: appID, ClientID: clientID, Fingerprint: githubapp.Fingerprint(key), Slug: info.Slug, Name: info.Name,
		Owner: info.Owner.Login, HTMLURL: info.HTMLURL, Permissions: info.Permissions, Checked: s.opt.Now().UTC()}
	if rec.KeyEnc, err = s.opt.Secrets.Seal(siteBinding, purposeKey, pemBytes); err != nil {
		return nil, err
	}
	if secret != "" {
		if rec.SecretEnc, err = s.opt.Secrets.Seal(siteBinding, purposeSecret, []byte(secret)); err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(rec)
	if err := store.SetSiteSetting(ctx, s.opt.Store.Q(), settingKey, string(raw), s.opt.Now()); err != nil {
		return nil, err
	}
	s.forget()
	s.dropAllTokens()
	s.changed("")
	return s.View(ctx)
}

func limited(err error) bool {
	var se *githubapp.StatusError
	return errors.As(err, &se) && se.RateLimited
}

// unreachable answers for a check that could not be made.
func (s *Service) unreachable(err error) error {
	if e := s.wrap(err); e != nil {
		var he *Error
		if errors.As(e, &he) {
			return he
		}
	}
	return errf(http.StatusServiceUnavailable, CodeUnavailable, "GitHub could not be asked right now; nothing was saved")
}

// Delete removes the App's settings, the users' connections (GitHub is asked
// to revoke each grant first, best effort) and the shared marks.
func (s *Service) Delete(ctx context.Context) error {
	st, err := s.state(ctx)
	if err != nil {
		return err
	}
	if st == nil {
		return nil
	}
	q := s.opt.Store.Q()
	n, err := store.CountGitHubSpaces(ctx, q)
	if err != nil {
		return err
	}
	if n > 0 {
		return errf(http.StatusConflict, CodeAppInUse, "%d space(s) sync through the App: switch or disconnect those first", n)
	}
	auths, err := store.ListGitHubAuthorizations(ctx, q)
	if err != nil {
		return err
	}
	for _, a := range auths {
		s.revoke(ctx, st, a)
	}
	err = s.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.DeleteGitHubAuthorizations(ctx, q); err != nil {
			return err
		}
		if err := store.DeleteGitHubInstallations(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `DELETE FROM site_settings WHERE key = ?`, settingKey)
		return err
	})
	if err != nil {
		return err
	}
	s.forget()
	s.dropAllTokens()
	return nil
}

// --- installations ----------------------------------------------------------------

// Installation is an installation as the administrator sees it.
type Installation struct {
	ID                  int64
	Account             githubapp.Account
	RepositorySelection string
	Suspended           bool
	Permissions         map[string]string
	HTMLURL             string
	Shared              bool
	Spaces              int
}

// cursor is opaque to clients: a page number, or a position among shared installations.
type cursor struct {
	Kind  string  `json:"k"`           // "p": a page of GitHub's list; "s": shared installations after ID
	Page  int     `json:"p,omitempty"` // "p"
	After int64   `json:"a,omitempty"` // "s"
	Seen  []int64 `json:"x,omitempty"` // "s": installations already listed
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil || c.Page < 0 || c.After < 0 || len(c.Seen) > 400 {
		return cursor{}, errf(http.StatusBadRequest, CodeInvalid, "the cursor is not one this server made")
	}
	return c, nil
}

// Installations lists the App's installations, one page of GitHub's list (one
// request) and one query of the database.
func (s *Service) Installations(ctx context.Context, cur string) ([]Installation, string, error) {
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
	list, next, err := st.app.Installations(ctx, c.Page)
	if err != nil {
		return nil, "", s.listError(err)
	}
	ids := make([]int64, len(list))
	for i, in := range list {
		ids[i] = in.ID
	}
	q := s.opt.Store.Q()
	shared, err := store.SharedGitHubInstallationIDs(ctx, q, ids)
	if err != nil {
		return nil, "", err
	}
	counts, err := store.GitHubSpaceCounts(ctx, q, ids)
	if err != nil {
		return nil, "", err
	}
	out := make([]Installation, len(list))
	for i, in := range list {
		out[i] = Installation{ID: in.ID, Account: in.Account, RepositorySelection: in.RepositorySelection, Suspended: in.Suspended(),
			Permissions: in.Permissions, HTMLURL: in.HTMLURL, Shared: shared[in.ID], Spaces: counts[in.ID]}
	}
	nc := ""
	if next > 0 {
		nc = encodeCursor(cursor{Kind: "p", Page: next})
	}
	return out, nc, nil
}

func (s *Service) listError(err error) error {
	if githubapp.IsStatus(err, http.StatusUnauthorized) && !limited(err) {
		return errf(http.StatusConflict, CodeNotConfigured, "GitHub no longer accepts the App's key; an administrator must fix the App")
	}
	return s.unreachable(err)
}

// SetShared marks an installation of an organisation as shared (or not).
func (s *Service) SetShared(ctx context.Context, id int64, shared bool) (*Installation, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, err
	}
	in, err := st.app.Installation(ctx, id)
	if err != nil {
		if githubapp.IsStatus(err, http.StatusNotFound) {
			return nil, errf(http.StatusNotFound, CodeNotFound, "GitHub does not know that installation")
		}
		return nil, s.listError(err)
	}
	if shared && in.Account.Type != "Organization" {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "only the installation of an organisation can be shared")
	}
	if err := store.PutGitHubInstallation(ctx, s.opt.Store.Q(), store.GitHubInstallation{ID: id, Account: in.Account.Login,
		AccountType: in.Account.Type, Shared: shared, Updated: s.opt.Now()}); err != nil {
		return nil, err
	}
	counts, err := store.GitHubSpaceCounts(ctx, s.opt.Store.Q(), []int64{id})
	if err != nil {
		return nil, err
	}
	return &Installation{ID: in.ID, Account: in.Account, RepositorySelection: in.RepositorySelection, Suspended: in.Suspended(),
		Permissions: in.Permissions, HTMLURL: in.HTMLURL, Shared: shared, Spaces: counts[id]}, nil
}
