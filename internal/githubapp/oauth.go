package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Tokens are a user's access token and, when the App expires them, the refresh
// token that renews it. A zero expiry means the token does not expire.
type Tokens struct {
	Access         string
	AccessExpires  time.Time
	Refresh        string
	RefreshExpires time.Time
}

// PKCE makes a code verifier (64 random characters) and its S256 challenge.
func PKCE() (verifier, challenge string) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var raw [64]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("githubapp: no randomness: " + err.Error())
	}
	b := make([]byte, 64)
	for i, x := range raw {
		b[i] = alphabet[int(x)%len(alphabet)]
	}
	sum := sha256.Sum256(b)
	return string(b), base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomState is 32 random bytes, URL-safe.
func RandomState() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("githubapp: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// AuthorizeURL is where the user is sent to authorise the App. login, when not
// empty, suggests the account.
func (c *Client) AuthorizeURL(clientID, redirect, state, challenge, login string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if login != "" {
		q.Set("login", login)
	}
	return c.web + "/login/oauth/authorize?" + q.Encode()
}

type tokenAnswer struct {
	AccessToken    string `json:"access_token"`
	ExpiresIn      int64  `json:"expires_in"`
	RefreshToken   string `json:"refresh_token"`
	RefreshExpires int64  `json:"refresh_token_expires_in"`
	Error          string `json:"error"`
	Description    string `json:"error_description"`
}

func (c *Client) tokens(ctx context.Context, form url.Values) (*Tokens, error) {
	var out tokenAnswer
	if _, err := c.do(ctx, request{method: http.MethodPost, base: c.web, path: "/login/oauth/access_token", form: form}, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, &OAuthError{Code: out.Error, Description: out.Description}
	}
	if out.AccessToken == "" {
		return nil, errors.New("github oauth: the answer has no access token")
	}
	now := c.now()
	t := &Tokens{Access: out.AccessToken, Refresh: out.RefreshToken}
	if out.ExpiresIn > 0 {
		t.AccessExpires = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.RefreshToken != "" && out.RefreshExpires > 0 {
		t.RefreshExpires = now.Add(time.Duration(out.RefreshExpires) * time.Second)
	}
	return t, nil
}

// ExchangeCode trades the code GitHub sent back for the user's tokens.
func (c *Client) ExchangeCode(ctx context.Context, clientID, secret, code, redirect, verifier string) (*Tokens, error) {
	return c.tokens(ctx, url.Values{"client_id": {clientID}, "client_secret": {secret}, "code": {code},
		"redirect_uri": {redirect}, "code_verifier": {verifier}})
}

// Refresh renews a user's tokens. GitHub replaces the refresh token with every
// renewal, and the old one stops working.
func (c *Client) Refresh(ctx context.Context, clientID, secret, refreshToken string) (*Tokens, error) {
	return c.tokens(ctx, url.Values{"client_id": {clientID}, "client_secret": {secret},
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

// IsBadRefresh reports whether err says the refresh token expired or was revoked.
func IsBadRefresh(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && (oe.Code == "bad_refresh_token" || oe.Code == "invalid_grant")
}

// RevokeGrant asks GitHub to revoke the user's authorisation of the App.
func (c *Client) RevokeGrant(ctx context.Context, clientID, secret, accessToken string) error {
	_, err := c.do(ctx, request{method: http.MethodDelete, base: c.api, path: "/applications/" + clientID + "/grant",
		basicUser: clientID, basicPw: secret, body: map[string]string{"access_token": accessToken}}, nil)
	return err
}

// ParseID reads a decimal id.
func ParseID(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}
