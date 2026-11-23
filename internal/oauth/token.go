package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

const maxTokenBody = 16 << 10

// token is POST /oauth/token.
func (s *Service) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "use POST")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "Content-Type must be application/x-www-form-urlencoded")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenBody)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body is not a valid form")
		return
	}
	// Parameters travel in the body only: ParseForm also reads the URL query,
	// which proxies log.
	form := r.PostForm
	if form.Get("client_secret") != "" || form.Get("client_assertion") != "" {
		oauthError(w, http.StatusBadRequest, "invalid_client", "only public clients are supported: send client_id and no secret")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// ownerUsable says the person a grant acts for may still use the server.
func (s *Service) ownerUsable(ctx context.Context, userID string) (bool, error) {
	u, err := s.cfg.Users.CachedUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !u.Disabled, nil
}

func (s *Service) exchangeCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	now := s.now()
	key := codeKey(f.Get("code"))
	s.mu.Lock()
	s.prune(now)
	c := s.codes[key]
	delete(s.codes, key) // single use, whatever happens next
	replay, wasUsed := s.used[key]
	s.mu.Unlock()
	if wasUsed {
		// A code presented twice: someone else may hold it. End the grant it made.
		s.log.Warn("oauth authorization code replayed; revoking the grant", "grant", replay.gid)
		if _, err := s.Revoke(r.Context(), replay.gid, "code replay"); err != nil {
			s.fail(w, "revoking after a code replay", err)
			return
		}
	}
	if c == nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the authorization code is invalid, expired or already used")
		return
	}
	switch {
	case f.Get("client_id") != c.client.ID:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was issued to another client")
		return
	case f.Get("redirect_uri") != c.redirectURI:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	case !pkceOK(f.Get("code_verifier"), c.challenge):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	if res := f.Get("resource"); res != "" && res != c.resource {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the authorization request")
		return
	}
	ok, err := s.ownerUsable(r.Context(), c.userID)
	if err != nil {
		s.fail(w, "reading the owner", err)
		return
	}
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the account is no longer allowed to use this server")
		return
	}
	gid, err := randomToken(12)
	if err != nil {
		s.fail(w, "grant id", err)
		return
	}
	sec, err := randomToken(32)
	if err != nil {
		s.fail(w, "refresh token", err)
		return
	}
	refresh := RefreshPrefix + gid + "." + sec
	g := store.OAuthGrant{ID: gid, UserID: c.userID, ClientID: c.client.ID, ClientName: c.client.Name, Access: c.access, Resource: c.resource,
		Created: now, LastUsed: now, RefreshHash: hashToken(sec)}
	for _, id := range c.spaces {
		g.Spaces = append(g.Spaces, store.TokenSpace{ID: id})
	}
	// The grant and the hash of its refresh token are stored before the token
	// reaches the client.
	err = s.st.Tx(r.Context(), func(q store.Q) error {
		if err := store.InsertOAuthGrant(r.Context(), q, g); err != nil {
			return err
		}
		if c.client.registered() {
			return store.TouchOAuthClient(r.Context(), q, c.client.ID, now)
		}
		return nil
	})
	if err != nil {
		s.fail(w, "saving the grant", err)
		return
	}
	s.mu.Lock()
	s.used[key] = usedCode{gid: gid, expires: now.Add(codeTTL)}
	s.mu.Unlock()
	s.log.Info("oauth grant created", "grant", gid, "user", c.userID, "client", c.client.ID)
	body, err := s.tokenBody(&g, scopesOf(g.Access), refresh)
	if err != nil {
		s.fail(w, "signing", err)
		return
	}
	writeBytes(w, body)
}

func (c clientInfo) registered() bool { return !isCIMDClientID(c.ID) }

// scopesOf are the scopes of an access level: write includes read.
func scopesOf(access string) []string {
	if access == scopeWrite {
		return []string{scopeRead, scopeWrite}
	}
	return []string{scopeRead}
}

// replayEntry is the answer given to a refresh, kept in memory for the grace
// window so that the same request, repeated, gets the same answer.
type replayEntry struct {
	gid     string
	body    []byte
	expires time.Time
}

const maxReplay = 1000

// refresh answers a refresh token. A grant has exactly one live refresh token.
//   - The live token is replaced: a new access and refresh token are made, the
//     new hash is stored, and the answer is remembered (in memory only) for the
//     grace window under the token it replaced.
//   - That replaced token, inside the grace window, gets exactly the
//     remembered answer: a lost response and two simultaneous requests are the
//     same thing. Nothing changes in the database. After a restart there is no
//     remembered answer: refused, the grant stays.
//   - A replaced token after the grace window ends the whole grant.
//   - An unknown token is refused and changes nothing.
//
// Everything that can refuse the request is checked before anything is stored.
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	ctx := r.Context()
	gid, sec, ok := splitRefresh(f.Get("refresh_token"))
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is not valid")
		return
	}
	h := hashToken(sec)
	s.refreshMu.Lock() // one refresh at a time: a second request with the same token sees the first one's answer
	defer s.refreshMu.Unlock()
	now := s.now()
	s.pruneReplay(now)

	g, err := store.GetOAuthGrant(ctx, s.st.Q(), gid, true)
	if errors.Is(err, store.ErrNotFound) || (err == nil && f.Get("client_id") != g.ClientID) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is not valid")
		return
	}
	if err != nil {
		s.fail(w, "reading the grant", err)
		return
	}
	live := equalStrings(g.RefreshHash, h)
	ri := -1
	for i, rt := range g.Retired {
		if equalStrings(rt.Hash, h) {
			ri = i
		}
	}
	if !live && ri < 0 {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is not valid") // a guess must not end anyone's grant
		return
	}
	end := func(reason string) {
		if _, err := s.Revoke(ctx, gid, reason); err != nil {
			s.fail(w, "ending a grant", err)
			return
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the grant has ended; authorize again")
	}
	switch {
	case !live && now.Sub(g.Retired[ri].At) > RefreshGrace:
		s.log.Warn("oauth refresh token reused after the grace window; grant revoked", "grant", gid)
		end("refresh token reused")
		return
	case now.Sub(g.LastUsed) > IdleExpiry:
		end("idle")
		return
	}
	usable, err := s.ownerUsable(ctx, g.UserID)
	if err != nil {
		s.fail(w, "reading the owner", err)
		return
	}
	if !usable {
		// Refused, and the grant is kept: it works again if the account does.
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the account is no longer allowed to use this server")
		return
	}
	// Refusals that leave the grant as it is.
	if res := f.Get("resource"); res != "" && res != g.Resource {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the grant")
		return
	}
	scopes := scopesOf(g.Access)
	if req := strings.Fields(f.Get("scope")); len(req) > 0 {
		for _, sc := range req {
			if !contains(scopes, sc) {
				oauthError(w, http.StatusBadRequest, "invalid_scope", "scope is wider than the grant")
				return
			}
		}
		scopes = req
	}

	if !live { // inside the grace window
		if body := s.replayFor(h, gid, now); body != nil {
			writeBytes(w, body)
			return
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token was already used; authorize again if this keeps happening")
		return
	}

	secNew, err := randomToken(32)
	if err != nil {
		s.fail(w, "refresh token", err)
		return
	}
	body, err := s.tokenBody(g, scopes, RefreshPrefix+gid+"."+secNew)
	if err != nil {
		s.fail(w, "signing", err)
		return
	}
	// The new hash is stored before the answer leaves. The grant may have been
	// revoked, or refreshed by another process, since it was read.
	won, err := store.RotateOAuthRefresh(ctx, s.st.Q(), gid, h, hashToken(secNew), now, maxRetired)
	if err != nil {
		s.fail(w, "refreshing", err)
		return
	}
	if !won {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is not valid")
		return
	}
	s.rememberReplay(h, gid, body, now.Add(RefreshGrace))
	writeBytes(w, body)
}

// rememberReplay keeps an answer for the grace window. It holds tokens, so it
// is only in memory, never logged, small, and dropped with its grant.
func (s *Service) rememberReplay(h, gid string, body []byte, until time.Time) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if len(s.replay) < maxReplay {
		s.replay[h] = replayEntry{gid: gid, body: body, expires: until}
	}
}

func (s *Service) replayFor(h, gid string, now time.Time) []byte {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if e, ok := s.replay[h]; ok && e.gid == gid && now.Before(e.expires) {
		return e.body
	}
	return nil
}

func (s *Service) pruneReplay(now time.Time) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	for k, e := range s.replay {
		if !now.Before(e.expires) {
			delete(s.replay, k)
		}
	}
}

func (s *Service) dropReplay(gid string) {
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	for k, e := range s.replay {
		if e.gid == gid {
			delete(s.replay, k)
		}
	}
}

func writeBytes(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type accessClaims struct {
	jwt.Claims
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
	GrantID  string `json:"gid"`
}

// tokenBody makes the token response: a new access token and the refresh token.
func (s *Service) tokenBody(g *store.OAuthGrant, scopes []string, refresh string) ([]byte, error) {
	jti, err := randomToken(12)
	if err != nil {
		return nil, err
	}
	now := s.now()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key.priv},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", s.key.kid))
	if err != nil {
		return nil, err
	}
	claims := accessClaims{
		Claims: jwt.Claims{Issuer: s.issuer, Subject: g.UserID, Audience: jwt.Audience{g.Resource}, ID: jti,
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(AccessTTL))},
		ClientID: g.ClientID, Scope: strings.Join(scopes, " "), GrantID: g.ID,
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"access_token": raw, "token_type": "Bearer", "expires_in": int(AccessTTL / time.Second),
		"refresh_token": refresh, "scope": strings.Join(scopes, " "),
	})
}
