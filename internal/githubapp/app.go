package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// RefreshBefore is how long before its expiry a cached token is replaced.
const RefreshBefore = 5 * time.Minute

// Bounds of the memory an App uses.
const (
	MaxTokens       = 10_000
	MaxLookups      = 10_000
	LookupTTL       = 10 * time.Minute
	jwtReuse        = 8 * time.Minute
	jwtLifetime     = 9 * time.Minute
	jwtBackdate     = time.Minute
	installRepoPath = "/repos/%s/%s/installation"
)

// ParseKey reads the App's private key (PEM, PKCS#1 or PKCS#8 RSA).
func ParseKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(data))
	if block == nil {
		return nil, errors.New("not a PEM private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("not an RSA private key in PKCS#1 or PKCS#8 form")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the GitHub App key must be an RSA key")
	}
	return rk, nil
}

// Fingerprint is the SHA-256 fingerprint of the public half of a key, the form
// GitHub shows for an App's keys.
func Fingerprint(k *rsa.PrivateKey) string {
	pub, err := ssh.NewPublicKey(&k.PublicKey)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(pub)
}

// TokenSpec says which installation token is wanted.
type TokenSpec struct {
	Installation int64
	// RepoIDs narrows the token to these repositories; none means every
	// repository of the installation.
	RepoIDs []int64
	// Perms are the permissions asked for, e.g. {"contents": "write"}.
	Perms map[string]string
	// Once makes a token for one request: it is not kept.
	Once bool
}

func (s TokenSpec) key() string {
	ids := append([]int64(nil), s.RepoIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var b strings.Builder
	b.WriteString(strconv.FormatInt(s.Installation, 10))
	for _, id := range ids {
		b.WriteString("," + strconv.FormatInt(id, 10))
	}
	names := make([]string, 0, len(s.Perms))
	for k := range s.Perms {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		b.WriteString("|" + k + "=" + s.Perms[k])
	}
	return b.String()
}

type cachedToken struct {
	token   string
	expires time.Time
}

type flight struct {
	done    chan struct{}
	token   string
	expires time.Time
	err     error
}

type lookup struct {
	id      int64
	expires time.Time
}

// App is the credentials of the GitHub App: it signs the App's JWT, finds the
// installation of an account or repository, and makes installation tokens. It
// is safe for concurrent use; its mutex is never held while the network is used.
type App struct {
	c  *Client
	id string
	k  *rsa.PrivateKey

	mu      sync.Mutex
	jwtTok  string
	jwtMade time.Time
	tokens  map[string]cachedToken
	flights map[string]*flight
	lookups map[string]lookup
}

// NewApp makes the App's credentials. id is the numeric App id.
func (c *Client) NewApp(id string, key *rsa.PrivateKey) (*App, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, " \t\r\n/") {
		return nil, errors.New("the GitHub App id is missing or malformed")
	}
	if key == nil {
		return nil, errors.New("the GitHub App private key is missing")
	}
	return &App{c: c, id: id, k: key, tokens: map[string]cachedToken{}, flights: map[string]*flight{}, lookups: map[string]lookup{}}, nil
}

// Client is the client the App was made from.
func (a *App) Client() *Client { return a.c }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// JWT is the App's JWT: iat a minute in the past (clock drift), exp nine
// minutes ahead (GitHub allows ten); one is reused for eight minutes.
func (a *App) JWT() (string, error) {
	now := a.c.now()
	a.mu.Lock()
	if a.jwtTok != "" && now.Before(a.jwtMade.Add(jwtReuse)) && !now.Before(a.jwtMade) {
		t := a.jwtTok
		a.mu.Unlock()
		return t, nil
	}
	a.mu.Unlock()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-jwtBackdate).Unix(), "exp": now.Add(jwtLifetime).Unix(), "iss": a.id})
	signing := b64(hdr) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.k, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign the App's JWT: %w", err)
	}
	t := signing + "." + b64(sig)
	a.mu.Lock()
	a.jwtTok, a.jwtMade = t, now
	a.mu.Unlock()
	return t, nil
}

func (a *App) asApp(ctx context.Context, method, path string, query map[string][]string, body, out any) (http.Header, error) {
	j, err := a.JWT()
	if err != nil {
		return nil, err
	}
	return a.c.do(ctx, request{method: method, base: a.c.api, path: path, query: query, bearer: j, body: body}, out)
}

// Token returns an installation access token. Unless spec.Once, it is cached
// until RefreshBefore its expiry, and callers asking for the same token at once
// share one request. A 404 or 422 means the installation, or a repository in
// it, is not there (see IsStatus).
func (a *App) Token(ctx context.Context, spec TokenSpec) (string, error) {
	if spec.Installation <= 0 || len(spec.Perms) == 0 {
		return "", errors.New("github: an installation and permissions are needed for a token")
	}
	if spec.Once {
		t, _, err := a.mint(ctx, spec)
		return t, err
	}
	ck := spec.key()
	a.mu.Lock()
	if c, ok := a.tokens[ck]; ok && a.c.now().Add(RefreshBefore).Before(c.expires) {
		a.mu.Unlock()
		return c.token, nil
	}
	if f, ok := a.flights[ck]; ok {
		a.mu.Unlock()
		select {
		case <-f.done:
			return f.token, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	a.flights[ck] = f
	delete(a.tokens, ck)
	a.mu.Unlock()

	f.token, f.expires, f.err = a.mint(ctx, spec)

	a.mu.Lock()
	if f.err == nil {
		a.storeLocked(ck, cachedToken{token: f.token, expires: f.expires})
	}
	delete(a.flights, ck)
	a.mu.Unlock()
	close(f.done)
	return f.token, f.err
}

// storeLocked keeps a token; when the cache is full, expired tokens go first,
// then the one that expires first.
func (a *App) storeLocked(key string, t cachedToken) {
	a.tokens[key] = t
	if len(a.tokens) <= MaxTokens {
		return
	}
	now := a.c.now()
	for k, v := range a.tokens {
		if !now.Before(v.expires) {
			delete(a.tokens, k)
		}
	}
	for len(a.tokens) > MaxTokens {
		first, at := "", time.Time{}
		for k, v := range a.tokens {
			if first == "" || v.expires.Before(at) {
				first, at = k, v.expires
			}
		}
		delete(a.tokens, first)
	}
}

func (a *App) mint(ctx context.Context, spec TokenSpec) (string, time.Time, error) {
	body := map[string]any{"permissions": spec.Perms}
	if len(spec.RepoIDs) > 0 {
		body["repository_ids"] = spec.RepoIDs
	}
	var out struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	if _, err := a.asApp(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(spec.Installation, 10)+"/access_tokens", nil, body, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Token == "" || out.Expires.IsZero() {
		return "", time.Time{}, errors.New("github: the access token answer is incomplete")
	}
	return out.Token, out.Expires, nil
}

// ForgetTokens drops every cached token (the key or the App changed).
func (a *App) ForgetTokens() {
	a.mu.Lock()
	a.tokens = map[string]cachedToken{}
	a.lookups = map[string]lookup{}
	a.mu.Unlock()
}

func validName(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\ ?#%") && s != "." && s != ".."
}

// lookupInstallation finds an installation id with a JWT call, remembered for
// LookupTTL; a 404 is not remembered.
func (a *App) lookupInstallation(ctx context.Context, key, path string) (int64, error) {
	now := a.c.now()
	a.mu.Lock()
	if l, ok := a.lookups[key]; ok && now.Before(l.expires) {
		a.mu.Unlock()
		return l.id, nil
	}
	a.mu.Unlock()
	var out struct {
		ID int64 `json:"id"`
	}
	if _, err := a.asApp(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, errors.New("github: no installation id in the answer")
	}
	a.mu.Lock()
	if len(a.lookups) >= MaxLookups {
		for k := range a.lookups {
			delete(a.lookups, k)
			break
		}
	}
	a.lookups[key] = lookup{id: out.ID, expires: now.Add(LookupTTL)}
	a.mu.Unlock()
	return out.ID, nil
}

// ForgetLookup drops what is remembered about where an account or repository is installed.
func (a *App) ForgetLookup(kind, name string) {
	a.mu.Lock()
	delete(a.lookups, kind+":"+strings.ToLower(name))
	a.mu.Unlock()
}

// RepoInstallation is the installation that reaches owner/name.
func (a *App) RepoInstallation(ctx context.Context, owner, name string) (int64, error) {
	if !validName(owner) || !validName(name) {
		return 0, fmt.Errorf("github: unusable repository %q/%q", owner, name)
	}
	return a.lookupInstallation(ctx, "repo:"+strings.ToLower(owner+"/"+name), fmt.Sprintf(installRepoPath, owner, name))
}

// OrgInstallation is the installation on an organisation.
func (a *App) OrgInstallation(ctx context.Context, org string) (int64, error) {
	if !validName(org) {
		return 0, fmt.Errorf("github: unusable organisation %q", org)
	}
	return a.lookupInstallation(ctx, "org:"+strings.ToLower(org), "/orgs/"+org+"/installation")
}

// UserInstallation is the installation on a user's account.
func (a *App) UserInstallation(ctx context.Context, login string) (int64, error) {
	if !validName(login) {
		return 0, fmt.Errorf("github: unusable account %q", login)
	}
	return a.lookupInstallation(ctx, "user:"+strings.ToLower(login), "/users/"+login+"/installation")
}
