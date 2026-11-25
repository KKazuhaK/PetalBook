package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/connections"
)

// gitlab is gitlab.com and self-hosted GitLab (REST API v4, OAuth 2 with PKCE).
//
// Scopes: `api`. Creating a project needs it (read_repository and
// write_repository only read and write the Git data), and it also covers the
// user and group reads; it is the least that does what 3.8 does.
type gitlab struct{}

func (gitlab) typ() string                                { return "gitlab" }
func (gitlab) scopes() []string                           { return []string{"api"} }
func (gitlab) tokenPath() string                          { return "/oauth/token" }
func (gitlab) revokePath() string                         { return "/oauth/revoke" }
func (gitlab) gitUser() string                            { return connections.GitUsernameForType("gitlab") }
func (gitlab) maxOwnerDepth() int                         { return 20 }
func (gitlab) refreshExpires(string, time.Time) time.Time { return time.Time{} } // GitLab's refresh tokens do not expire

func (g gitlab) authorizeURL(base, clientID, redirect, state, challenge string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "state": {state},
		"scope": {strings.Join(g.scopes(), " ")}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return base + "/oauth/authorize?" + q.Encode()
}

// probe asks GET /api/v4/version, which answers 200 with the version to a
// signed-in person and 401 {"message": "401 Unauthorized"} to anyone else: the
// second is how a GitLab looks from here, and the version is read later, with
// the first person's token.
func (gitlab) probe(ctx context.Context, c *client) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	_, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v4/version", kind: "version"}, &out)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusUnauthorized && strings.Contains(strings.ToLower(se.Message), "unauthorized") {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if out.Version == "" {
		return "", errNotThisPlatform
	}
	return out.Version, nil
}

func (gitlab) minVersion(v string) bool {
	if v == "" {
		return true
	}
	major, _, ok := minorOf(v)
	return ok && major >= 15 // PKCE and expiring tokens by default
}

func (gitlab) version(ctx context.Context, c *client, tok string) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v4/version", bearer: tok, kind: "version"}, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

type glUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (gitlab) user(ctx context.Context, c *client, tok string) (*account, error) {
	var u glUser
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v4/user", bearer: tok, kind: "user"}, &u); err != nil {
		return nil, err
	}
	if u.ID == 0 || u.Username == "" {
		return nil, errNotThisPlatform
	}
	return &account{ID: idString(u.ID), Login: u.Username}, nil
}

type glGroup struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
}

func (gitlab) groups(ctx context.Context, c *client, tok string, page int, search string, size int) ([]glGroup, int, error) {
	q := pageQuery(page)
	q.Set("min_access_level", "30") // Developer: the lowest level that may create a project
	q.Set("order_by", "path")
	q.Set("sort", "asc")
	if size > 0 {
		q.Set("per_page", strconv.Itoa(size))
	}
	if search != "" {
		q.Set("search", search)
	}
	var list []glGroup
	h, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v4/groups", query: q, bearer: tok, kind: "list_groups"}, &list)
	if err != nil {
		return nil, 0, err
	}
	return list, nextPage(h), nil
}

func (g gitlab) owners(ctx context.Context, c *client, tok string, _ *account, page int, q string) ([]ownerInfo, int, error) {
	list, next, err := g.groups(ctx, c, tok, page, q, 0)
	if err != nil {
		return nil, 0, err
	}
	out := make([]ownerInfo, len(list))
	for i, x := range list {
		out[i] = ownerInfo{Login: x.FullPath, Group: true, NamespaceID: idString(x.ID)}
	}
	return out, next, nil
}

func (g gitlab) findOwner(ctx context.Context, c *client, tok string, _ *account, owner string) (*ownerInfo, error) {
	last := owner[strings.LastIndex(owner, "/")+1:]
	for page := 1; page > 0 && page <= 5; {
		list, next, err := g.groups(ctx, c, tok, page, last, 100)
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			if strings.EqualFold(x.FullPath, owner) {
				return &ownerInfo{Login: x.FullPath, Group: true, NamespaceID: idString(x.ID)}, nil
			}
		}
		page = next
	}
	return nil, nil
}

type glProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	Visibility        string `json:"visibility"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
	Permissions       struct {
		Project *struct {
			Level int `json:"access_level"`
		} `json:"project_access"`
		Group *struct {
			Level int `json:"access_level"`
		} `json:"group_access"`
	} `json:"permissions"`
}

func (p glProject) info() repoInfo {
	owner, name := splitFull(p.PathWithNamespace)
	level := 0
	if p.Permissions.Project != nil {
		level = max(level, p.Permissions.Project.Level)
	}
	if p.Permissions.Group != nil {
		level = max(level, p.Permissions.Group.Level)
	}
	return repoInfo{ID: p.ID, Owner: owner, Name: name, FullName: p.PathWithNamespace, HTMLURL: p.WebURL,
		Private: p.Visibility != "public", DefaultBranch: p.DefaultBranch, CanPush: level >= 30}
}

// splitFull splits "group/sub/name" into "group/sub" and "name".
func splitFull(full string) (owner, name string) {
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return "", full
	}
	return full[:i], full[i+1:]
}

func (gitlab) createRepo(ctx context.Context, c *client, tok string, o *ownerInfo, name, description string, private bool) (*repoInfo, error) {
	vis := "private"
	if !private {
		vis = "public"
	}
	body := map[string]any{"name": name, "path": name, "visibility": vis, "description": description, "initialize_with_readme": false}
	if o != nil && o.NamespaceID != "" {
		n, _ := strconv.ParseInt(o.NamespaceID, 10, 64)
		body["namespace_id"] = n
	}
	var p glProject
	if _, err := c.do(ctx, request{method: http.MethodPost, path: "/api/v4/projects", bearer: tok, body: body, kind: "create_repo"}, &p); err != nil {
		return nil, err
	}
	r := p.info()
	return &r, nil
}

func (gitlab) repos(ctx context.Context, c *client, tok string, page int, q string) ([]repoInfo, int, error) {
	v := pageQuery(page)
	v.Set("membership", "true")
	v.Set("min_access_level", "30")
	v.Set("order_by", "last_activity_at")
	if q != "" {
		v.Set("search", q)
	}
	var list []glProject
	h, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v4/projects", query: v, bearer: tok, kind: "list_repos"}, &list)
	if err != nil {
		return nil, 0, err
	}
	out := make([]repoInfo, len(list))
	for i, p := range list {
		out[i] = p.info()
	}
	return out, nextPage(h), nil
}

func (gitlab) getRepo(ctx context.Context, c *client, tok, owner, name string) (*repoInfo, error) {
	var p glProject
	if _, err := c.do(ctx, request{method: http.MethodGet, path: "/api/v4/projects/" + escapePath(owner+"/"+name), bearer: tok, kind: "get_repo"}, &p); err != nil {
		return nil, err
	}
	r := p.info()
	return &r, nil
}

var glSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)

// validOwner: a namespace is one or more path segments, a user's name or a
// group and its subgroups.
func (gitlab) validOwner(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if !glSegmentRe.MatchString(seg) || strings.HasSuffix(seg, ".git") || strings.HasSuffix(seg, ".atom") {
			return false
		}
	}
	return true
}

func (gitlab) validName(s string) bool {
	return commonName(s) && glSegmentRe.MatchString(s) && !strings.HasSuffix(s, ".atom")
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// commonName is what every platform keeps as a repository name: 1 to 100
// letters, digits, '.', '_' and '-', not '.' or '..' and not ending in .git.
func commonName(s string) bool {
	return nameRe.MatchString(s) && s != "." && s != ".." && !strings.HasSuffix(strings.ToLower(s), ".git")
}

var errNotThisPlatform = errors.New("the host does not answer as this kind of platform")
