package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"

	"petalbook.app/server/internal/store"
)

// A custom OAuth 2.0 sign-in provider (kind "oauth2", site-owned, social): plain
// OAuth 2.0, no OpenID Connect (no id_token, no discovery), as Discord or a
// self-hosted GitLab without OIDC are. The administrator gives the three
// addresses, the scopes, how the client signs in at the token endpoint, and the
// dotted paths into the userinfo answer that hold who the person is.
//
// The calls are made like GitHub's: the code is exchanged with PKCE (S256), the
// user's token is used for one GET of the userinfo address and then dropped,
// nothing is followed by redirect, and every answer is read up to 1 MiB.

const (
	oauth2ReqTimeout   = 10 * time.Second // each request (Config.OAuth2RequestTimeout)
	oauth2FlowTimeout  = 20 * time.Second // the whole callback (Config.OAuth2FlowTimeout)
	oauth2ReadLimit    = 1 << 20
	oauth2MaxScopes    = 32
	oauth2MaxPathParts = 8
	oauth2MaxPathLen   = 256
	// Auth methods of the token endpoint, as the provider's settings give them.
	oauth2AuthBasic = "client_secret_basic"
	oauth2AuthPost  = "client_secret_post"
)

// OAuth2Cfg is the OAuth 2.0 part of a provider's configuration (nothing in it
// is secret: the client secret is sealed in the row).
type OAuth2Cfg struct {
	AuthorizationEndpoint string       `json:"authorizationEndpoint"`
	TokenEndpoint         string       `json:"tokenEndpoint"`
	UserinfoEndpoint      string       `json:"userinfoEndpoint"`
	Scopes                []string     `json:"scopes"`
	AuthMethod            string       `json:"authMethod"`
	Fields                OAuth2Fields `json:"fields"`
	ClientID              string       `json:"clientId"`
}

// OAuth2Fields are the paths into the userinfo answer. Only the subject is
// required.
type OAuth2Fields struct {
	Subject       string `json:"subject"`
	Email         string `json:"email"`
	EmailVerified string `json:"emailVerified"`
	Name          string `json:"name"`
	Username      string `json:"username"`
}

func (f OAuth2Fields) all() []struct{ name, path string } {
	return []struct{ name, path string }{{"subject", f.Subject}, {"email", f.Email}, {"emailVerified", f.EmailVerified}, {"name", f.Name}, {"username", f.Username}}
}

func (c OAuth2Cfg) clone() *OAuth2Cfg {
	c.Scopes = append([]string(nil), c.Scopes...)
	return &c
}

var fieldSegRE = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// validFieldPath says whether p is a dotted path: 1 to 8 segments of 1 to 64
// of A-Z a-z 0-9 _ : -, at most 256 characters in all.
func validFieldPath(p string) bool {
	if p == "" || len(p) > oauth2MaxPathLen {
		return false
	}
	parts := strings.Split(p, ".")
	if len(parts) > oauth2MaxPathParts {
		return false
	}
	for _, s := range parts {
		if !fieldSegRE.MatchString(s) {
			return false
		}
	}
	return true
}

// resolvePath follows a dotted path in a decoded JSON document (maps, slices,
// json.Number, string, bool, nil: decode with UseNumber). A segment names a
// member of an object; a segment of digits indexes an array, and only an
// array (an object's key that looks like a number is a key). A JSON null is
// found, as nil. ok is false when nothing is there.
func resolvePath(doc any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, ok := arrayIndex(seg)
			if !ok || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// arrayIndex reads a canonical array index (no sign, no leading zero).
func arrayIndex(seg string) (int, bool) {
	if seg == "" || len(seg) > 9 || (len(seg) > 1 && seg[0] == '0') {
		return 0, false
	}
	for _, r := range seg {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	i, err := strconv.Atoi(seg)
	return i, err == nil
}

var integerRE = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// oauth2Subject reads the subject from what its path found: a string (trimmed)
// or an integer as written (surrounding whitespace of a string is trimmed). A float, an exponent, a bool, an object, a null or
// nothing is refused. The rules of a sub are the OpenID Connect ones.
func oauth2Subject(v any) (string, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = strings.TrimSpace(x)
	case json.Number:
		if !integerRE.MatchString(x.String()) {
			return "", false
		}
		s = x.String()
	default:
		return "", false
	}
	if !usableSubject(s) {
		return "", false
	}
	return s, true
}

// usableSubject is the rule of an account's id at a provider: 1 to 255 bytes
// of text without control characters.
func usableSubject(s string) bool {
	return s != "" && len(s) <= 255 && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func (s *Service) oauth2Config(rt *providerRT) *oauth2.Config {
	o := rt.cfg.OAuth2
	style := oauth2.AuthStyleInHeader
	if o.AuthMethod == oauth2AuthPost {
		style = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{ClientID: o.ClientID, ClientSecret: rt.secret, RedirectURL: s.redirectURL(rt), Scopes: o.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: o.AuthorizationEndpoint, TokenURL: o.TokenEndpoint, AuthStyle: style}}
}

func (s *Service) oauth2ReqLimit() time.Duration {
	if s.cfg.OAuth2RequestTimeout > 0 {
		return s.cfg.OAuth2RequestTimeout
	}
	return oauth2ReqTimeout
}

func (s *Service) oauth2FlowLimit() time.Duration {
	if s.cfg.OAuth2FlowTimeout > 0 {
		return s.cfg.OAuth2FlowTimeout
	}
	return oauth2FlowTimeout
}

// oauth2Client is the client of one callback: the server's, which follows no
// redirect (a token or userinfo address that redirects is an error).
func (s *Service) oauth2Client() *http.Client {
	c := *s.httpClient()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

var errUserinfo = errors.New("the userinfo answer is not usable")

// oauth2Userinfo asks the userinfo address for the user: GET, bearer token,
// 200, a JSON object of at most 1 MiB.
func (s *Service) oauth2Userinfo(ctx context.Context, hc *http.Client, o *OAuth2Cfg, token string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, s.oauth2ReqLimit())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PetalBook")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the userinfo address answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauth2ReadLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > oauth2ReadLimit {
		return nil, errors.New("the userinfo answer is over 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, errUserinfo
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errUserinfo // something after the object
	}
	return doc, nil
}

// oauth2Claims exchanges the code and reads the user. The user's token lives
// only in this call. A non-empty second value is the sso_error code.
func (s *Service) oauth2Claims(ctx context.Context, rt *providerRT, code, verifier string) (claimed, string) {
	if rt.secretErr != nil || rt.cfg.OAuth2 == nil {
		return claimed{}, "provider_unavailable"
	}
	o := rt.cfg.OAuth2
	hc := s.oauth2Client()
	ctx, cancel := context.WithTimeout(ctx, s.oauth2FlowLimit())
	defer cancel()
	xctx, xcancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, hc), s.oauth2ReqLimit())
	tok, err := s.oauth2Config(rt).Exchange(xctx, code, oauth2.VerifierOption(verifier))
	xcancel()
	if err != nil || tok.AccessToken == "" {
		s.log.Warn("the OAuth 2.0 provider refused the code", "provider", rt.row.Slug)
		return claimed{}, "idp_error"
	}
	doc, err := s.oauth2Userinfo(ctx, hc, o, tok.AccessToken)
	if err != nil {
		s.log.Warn("could not read the OAuth 2.0 user", "provider", rt.row.Slug, "reason", userinfoReason(err))
		return claimed{}, "idp_error"
	}
	c, ok := oauth2Identity(rt, doc)
	if !ok {
		s.log.Warn("the OAuth 2.0 user has no usable subject", "provider", rt.row.Slug)
		return claimed{}, "idp_error"
	}
	return c, ""
}

// userinfoReason is what may be logged of a failed userinfo call: never the
// address (it may carry what the administrator typed).
func userinfoReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timeout"
		}
		return "the request failed"
	}
	return err.Error()
}

// oauth2Identity reads who signed in from the userinfo document by the
// provider's paths. false: there is no usable subject.
func oauth2Identity(rt *providerRT, doc map[string]any) (claimed, bool) {
	f := rt.cfg.OAuth2.Fields
	var unresolved []string
	get := func(path string) (any, bool) {
		if path == "" {
			return nil, false
		}
		v, ok := resolvePath(doc, path)
		if !ok || v == nil {
			unresolved = append(unresolved, path)
			return nil, false
		}
		return v, true
	}
	text := func(path string, max int) string {
		v, ok := get(path)
		if !ok {
			return ""
		}
		str, _ := v.(string)
		return clip(strings.TrimSpace(str), max)
	}
	sv, _ := resolvePath(doc, f.Subject)
	sub, ok := oauth2Subject(sv)
	if !ok {
		return claimed{}, false
	}
	email := text(f.Email, 254)
	verified := false
	if v, ok := get(f.EmailVerified); ok {
		switch x := v.(type) {
		case bool:
			verified = x
		case string:
			verified = x == "true"
		}
	}
	c := claimed{subject: sub, name: text(f.Name, 100), preferred: text(f.Username, 100), email: email, unresolved: unresolved,
		src: emailSource{template: trustTemplate(rt.row), verified: verified}}
	if c.label() == "" {
		c.labelOverride = clip(firstNonEmpty(c.name, sub), 100)
	}
	return c, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// oauth2Endpoint checks an address of the provider: https, no userinfo part,
// no query, no fragment, at most 2048 characters. The site's own providers are
// not held to public addresses (the same as its OpenID Connect, SAML and
// GitHub Enterprise ones).
func oauth2Endpoint(name, raw string) (string, error) {
	if strings.ContainsAny(raw, "?#") {
		return "", providerInvalid("%s must be an https address without a query or a fragment.", name)
	}
	return httpsOnly(name, raw, true)
}

// applyOAuth2 checks and takes the OAuth 2.0 part of a provider request into b.
// Nothing is fetched: there is nothing to discover.
func (s *Service) applyOAuth2(ctx context.Context, b *built, cur *providerRT, in *OAuth2Input, creating bool, mark func(string)) error {
	if b.cfg.OAuth2 == nil {
		b.cfg.OAuth2 = &OAuth2Cfg{AuthMethod: oauth2AuthBasic}
	}
	o := b.cfg.OAuth2
	subjectChanged, hostChanged := false, false
	if in != nil {
		for _, e := range []struct {
			name string
			in   *string
			dst  *string
		}{{"oauth2.authorizationEndpoint", in.AuthorizationEndpoint, &o.AuthorizationEndpoint}, {"oauth2.tokenEndpoint", in.TokenEndpoint, &o.TokenEndpoint},
			{"oauth2.userinfoEndpoint", in.UserinfoEndpoint, &o.UserinfoEndpoint}} {
			if e.in == nil {
				continue
			}
			u, err := oauth2Endpoint(e.name, *e.in)
			if err != nil {
				return err
			}
			if u != *e.dst {
				mark(e.name)
				if e.in == in.UserinfoEndpoint && hostOf(u) != hostOf(*e.dst) {
					hostChanged = true
				}
			}
			*e.dst = u
		}
		if in.Scopes != nil {
			sc, err := cleanList("oauth2.scopes", *in.Scopes, oauth2MaxScopes)
			if err != nil {
				return err
			}
			if !slices.Equal(sc, o.Scopes) {
				mark("oauth2.scopes")
			}
			o.Scopes = sc
		}
		if in.AuthMethod != nil {
			if *in.AuthMethod != oauth2AuthBasic && *in.AuthMethod != oauth2AuthPost {
				return providerInvalid("oauth2.authMethod is %s or %s.", oauth2AuthBasic, oauth2AuthPost)
			}
			if *in.AuthMethod != o.AuthMethod {
				mark("oauth2.authMethod")
			}
			o.AuthMethod = *in.AuthMethod
		}
		if in.ClientID != nil {
			id, err := cleanText("oauth2.clientId", *in.ClientID, 512)
			if err != nil {
				return err
			}
			if id != o.ClientID {
				mark("oauth2.clientId")
			}
			o.ClientID = id
		}
		if in.ClientSecret != nil {
			if *in.ClientSecret == "" || len(*in.ClientSecret) > 16*1024 {
				return providerInvalid("oauth2.clientSecret is 1 byte to 16 KiB.")
			}
			b.secret, b.secretSet = *in.ClientSecret, true
			mark("oauth2.clientSecret")
		}
		if f := in.Fields; f != nil {
			for _, e := range []struct {
				name     string
				in       *string
				dst      *string
				required bool
			}{{"subject", f.Subject, &o.Fields.Subject, true}, {"email", f.Email, &o.Fields.Email, false}, {"emailVerified", f.EmailVerified, &o.Fields.EmailVerified, false},
				{"name", f.Name, &o.Fields.Name, false}, {"username", f.Username, &o.Fields.Username, false}} {
				if e.in == nil {
					continue
				}
				v := strings.TrimSpace(*e.in)
				if !(v == "" && !e.required) && !validFieldPath(v) {
					return providerInvalid("oauth2.fields.%s is a dotted path: up to %d names of 1 to 64 letters, digits, _ : or -, at most %d characters.", e.name, oauth2MaxPathParts, oauth2MaxPathLen)
				}
				if v != *e.dst {
					mark("oauth2.fields." + e.name)
					if e.name == "subject" {
						subjectChanged = true
					}
				}
				*e.dst = v
			}
		}
	}
	if o.AuthorizationEndpoint == "" || o.TokenEndpoint == "" || o.UserinfoEndpoint == "" {
		return providerInvalid("oauth2.authorizationEndpoint, oauth2.tokenEndpoint and oauth2.userinfoEndpoint are required.")
	}
	if o.ClientID == "" {
		return providerInvalid("oauth2.clientId is required.")
	}
	if !validFieldPath(o.Fields.Subject) {
		return providerInvalid("oauth2.fields.subject is required: the path of the person's id in the userinfo answer.")
	}
	if creating && b.secret == "" {
		return providerInvalid("oauth2.clientSecret is required.")
	}
	if o.AuthMethod == "" {
		o.AuthMethod = oauth2AuthBasic
	}
	if o.Scopes == nil {
		o.Scopes = []string{}
	}
	// An account is found by the provider's id and the subject: what the
	// subject is, and the server it comes from, cannot change under the people
	// who are linked, or they would be somebody else (or nobody).
	if !creating && (subjectChanged || hostChanged) {
		n, err := store.CountLinks(ctx, s.st.Q(), cur.row.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return coded(409, "subject_fixed", "%d account(s) are linked to this provider, so the path of the subject and the host of the userinfo address cannot change. Delete the provider, or remove its linked accounts, first.", n)
		}
	}
	return nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
