package forge

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// What a platform's REST API gives back, in the shapes the provider uses.

type account struct {
	ID    string
	Login string
	// Admin: the host says the account is an administrator of the host.
	Admin bool
}

// ownerInfo is a place a repository can be created: the person's own
// namespace, or a group or organisation.
type ownerInfo struct {
	Login string // as the platform spells it (a group's full path)
	Group bool
	// NamespaceID is GitLab's id of the namespace a project is created in; ""
	// for the person's own.
	NamespaceID string
}

type repoInfo struct {
	ID            int64
	Owner, Name   string
	FullName      string
	HTMLURL       string
	Private       bool
	DefaultBranch string
	CanPush       bool
}

type tokens struct {
	Access         string
	AccessExpires  time.Time
	Refresh        string
	RefreshExpires time.Time
}

// driver is what differs between a GitLab and a Gitea: the OAuth endpoints and
// the REST calls. Everything above it (connecting a person, renewing, the
// credential of a space, the administration) is the same.
type driver interface {
	typ() string
	// scopes are what the OAuth application is asked for: the least that creates,
	// reads and writes repositories.
	scopes() []string
	authorizeURL(base, clientID, redirect, state, challenge string) string
	tokenPath() string
	// revokePath is "" for a platform that cannot revoke a token.
	revokePath() string
	// probe reads the host's version without signing in; "" when the platform
	// does not say without one.
	probe(ctx context.Context, c *client) (version string, err error)
	minVersion(v string) bool
	// version reads the host's version with a person's token, for a platform that
	// does not say it to anyone signed out; "" when probe already says it.
	version(ctx context.Context, c *client, tok string) (string, error)
	// refreshExpires is when a refresh token ends, zero when it never does or the
	// host does not say.
	refreshExpires(refresh string, now time.Time) time.Time

	user(ctx context.Context, c *client, tok string) (*account, error)
	// owners lists places the person may create repositories in, a page at a time
	// (the person's own namespace is the caller's to add).
	owners(ctx context.Context, c *client, tok string, me *account, page int, q string) ([]ownerInfo, int, error)
	// findOwner looks up one place the person may create repositories in; nil
	// when there is none such.
	findOwner(ctx context.Context, c *client, tok string, me *account, owner string) (*ownerInfo, error)
	createRepo(ctx context.Context, c *client, tok string, o *ownerInfo, name, description string, private bool) (*repoInfo, error)
	repos(ctx context.Context, c *client, tok string, page int, q string) ([]repoInfo, int, error)
	getRepo(ctx context.Context, c *client, tok, owner, name string) (*repoInfo, error)

	validOwner(s string) bool
	validName(s string) bool
	// maxOwnerDepth is how many path segments an owner may have (1 on a platform
	// without subgroups).
	maxOwnerDepth() int
	gitUser() string
}

func idString(n int64) string { return strconv.FormatInt(n, 10) }

// minorOf reads "major.minor" from the start of a version.
func minorOf(v string) (major, minor int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(parts[0])
	digits := parts[1]
	for i, r := range digits {
		if r < '0' || r > '9' {
			digits = digits[:i]
			break
		}
	}
	b, err2 := strconv.Atoi(digits)
	return a, b, err1 == nil && err2 == nil
}

// escapePath escapes a project path for use as one path segment ("a/b" becomes "a%2Fb").
func escapePath(p string) string { return url.PathEscape(p) }
