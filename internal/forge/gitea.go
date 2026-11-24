package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// gitea is Gitea and Forgejo, Codeberg included (codeberg.org runs Forgejo):
// REST API v1, OAuth 2 with PKCE.
//
// Scopes: none are asked for. A Gitea OAuth2 application's token is as wide as
// the person's own rights and an authorisation request carries no scope that
// narrows it (a Gitea that honours scopes grants the default when none is
// asked); the administration page says so.
type gitea struct{}

func (gitea) typ() string        { return "gitea" }
func (gitea) scopes() []string   { return nil }
func (gitea) tokenPath() string  { return "/login/oauth/access_token" }
func (gitea) revokePath() string { return "" } // Gitea has no token revocation endpoint; people revoke the application in their settings
func (gitea) gitUser() string    { return "oauth2" }
func (gitea) maxOwnerDepth() int { return 1 }

func (gitea) authorizeURL(base, clientID, redirect, state, challenge string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return base + "/login/oauth/authorize?" + q.Encode()
}

func (gitea) probe(ctx context.Context, c *client) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/version", kind: "version"}, &out); err != nil {
		return "", err
	}
	if out.Version == "" {
		return "", errNotThisPlatform
	}
	return out.Version, nil
}

// minVersion: Gitea 1.19 has PKCE. A Forgejo's version is its own number
// followed by "+gitea-X.Y.Z" (the Gitea it follows), which is what counts.
func (gitea) minVersion(v string) bool {
	if i := strings.Index(v, "gitea-"); i >= 0 {
		v = v[i+len("gitea-"):]
	}
	major, minor, ok := minorOf(v)
	return ok && (major > 1 || (major == 1 && minor >= 19))
}

// refreshExpires reads the end of a refresh token: Gitea's are signed tokens
// (JWT) that say when they end in "exp". The signature is not checked: the
// host made the token and this is only a date for the daily renewal.
func (gitea) refreshExpires(refresh string, now time.Time) time.Time {
	parts := strings.Split(refresh, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil || c.Exp <= 0 {
		return time.Time{}
	}
	t := time.Unix(c.Exp, 0)
	if !t.After(now) {
		return time.Time{}
	}
	return t
}

func (gitea) version(context.Context, *client, string) (string, error) { return "", nil }

func (gitea) user(ctx context.Context, c *client, tok string) (*account, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/user", bearer: tok, kind: "user"}, &u); err != nil {
		return nil, err
	}
	if u.ID == 0 || u.Login == "" {
		return nil, errNotThisPlatform
	}
	return &account{ID: idString(u.ID), Login: u.Login}, nil
}

// owners lists the organisations the person has a team in that may create
// repositories: one request a page of teams (an organisation with several of
// the person's teams is listed once per page).
func (gitea) owners(ctx context.Context, c *client, tok string, _ *account, page int, q string) ([]ownerInfo, int, error) {
	var teams []struct {
		CanCreate    bool   `json:"can_create_org_repo"`
		Permission   string `json:"permission"`
		Organization struct {
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"organization"`
	}
	h, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/user/teams", query: pageQuery(page), bearer: tok, kind: "list_teams"}, &teams)
	if err != nil {
		return nil, 0, err
	}
	var out []ownerInfo
	seen := map[string]bool{}
	for _, t := range teams {
		name := t.Organization.Username
		if name == "" {
			name = t.Organization.Name
		}
		if name == "" || seen[name] || !(t.CanCreate || t.Permission == "owner") {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(q)) {
			continue
		}
		seen[name] = true
		out = append(out, ownerInfo{Login: name, Group: true})
	}
	return out, nextPage(h), nil
}

func (gitea) findOwner(ctx context.Context, c *client, tok string, me *account, owner string) (*ownerInfo, error) {
	var p struct {
		CanCreate bool `json:"can_create_repository"`
	}
	_, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/users/" + url.PathEscape(me.Login) + "/orgs/" + url.PathEscape(owner) + "/permissions",
		bearer: tok, kind: "org_permissions"}, &p)
	if isStatus(err, http.StatusNotFound, http.StatusForbidden) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !p.CanCreate {
		return nil, nil
	}
	return &ownerInfo{Login: owner, Group: true}, nil
}

type gtRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

func (r gtRepo) info() repoInfo {
	owner, name := splitFull(r.FullName)
	return repoInfo{ID: r.ID, Owner: owner, Name: name, FullName: r.FullName, HTMLURL: r.HTMLURL, Private: r.Private,
		DefaultBranch: r.DefaultBranch, CanPush: r.Permissions.Push}
}

func (gitea) createRepo(ctx context.Context, c *client, tok string, o *ownerInfo, name, description string, private bool) (*repoInfo, error) {
	path, kind := "/api/v1/user/repos", "create_repo"
	if o != nil && o.Group {
		path = "/api/v1/orgs/" + url.PathEscape(o.Login) + "/repos"
	}
	body := map[string]any{"name": name, "private": private, "description": description, "auto_init": false, "default_branch": "main"}
	var r gtRepo
	if _, err := c.do(ctx, request{method: http.MethodPost, path: path, bearer: tok, body: body, kind: kind}, &r); err != nil {
		return nil, err
	}
	i := r.info()
	return &i, nil
}

func (gitea) repos(ctx context.Context, c *client, tok string, page int, q string) ([]repoInfo, int, error) {
	var list []gtRepo
	var h http.Header
	var err error
	if q == "" {
		h, err = c.do(ctx, request{method: http.MethodGet, path: "/api/v1/user/repos", query: pageQuery(page), bearer: tok, kind: "list_repos"}, &list)
	} else {
		v := pageQuery(page)
		v.Set("q", q)
		v.Set("sort", "updated")
		v.Set("order", "desc")
		var out struct {
			Data []gtRepo `json:"data"`
		}
		h, err = c.do(ctx, request{method: http.MethodGet, path: "/api/v1/repos/search", query: v, bearer: tok, kind: "search_repos"}, &out)
		list = out.Data
	}
	if err != nil {
		return nil, 0, err
	}
	out := make([]repoInfo, 0, len(list))
	for _, r := range list {
		if r.Permissions.Push {
			out = append(out, r.info())
		}
	}
	return out, nextPage(h), nil
}

func (gitea) getRepo(ctx context.Context, c *client, tok, owner, name string) (*repoInfo, error) {
	var r gtRepo
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), bearer: tok, kind: "get_repo"}, &r); err != nil {
		return nil, err
	}
	i := r.info()
	return &i, nil
}

var gtOwnerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,38}$`)

func (gitea) validOwner(s string) bool { return gtOwnerRe.MatchString(s) }

func (gitea) validName(s string) bool {
	return commonName(s) && !strings.HasSuffix(strings.ToLower(s), ".wiki")
}
