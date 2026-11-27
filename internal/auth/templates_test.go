package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func p8(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// appleClaims checks a client secret's signature with the public key and
// returns its header's key id and its claims.
func appleClaims(t *testing.T, tok string, pub *ecdsa.PublicKey) (string, map[string]any) {
	t.Helper()
	obj, err := jose.ParseSigned(tok, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := obj.Verify(pub)
	if err != nil {
		t.Fatalf("the secret's signature: %v", err)
	}
	var c map[string]any
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatal(err)
	}
	return obj.Signatures[0].Header.KeyID, c
}

// Apple's client secret is an ES256 JWT of the team's key: iss the team, sub
// the client, aud Apple, under six months. It is signed at use, kept, and
// replaced once it nears its end, never earlier.
func TestAppleSecretIsSignedAndRenewedBeforeItEnds(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseAppleKey(" \n" + p8(t, key) + "\n")
	if err != nil || !parsed.Equal(key) {
		t.Fatalf("%v", err)
	}
	a := &appleSecret{key: parsed, teamID: "TEAM123456", keyID: "KEY1234567", clientID: "com.example.web"}
	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	s1, changed, err := a.at(t0)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	kid, c := appleClaims(t, s1, &key.PublicKey)
	iat, exp := int64(c["iat"].(float64)), int64(c["exp"].(float64))
	if kid != "KEY1234567" || c["iss"] != "TEAM123456" || c["sub"] != "com.example.web" || c["aud"] != "https://appleid.apple.com" ||
		iat != t0.Unix() || exp != t0.Add(180*24*time.Hour).Unix() {
		t.Fatalf("%s %v", kid, c)
	}
	if exp-iat > 15777000 {
		t.Fatalf("lives %d s, more than Apple takes", exp-iat)
	}
	// Kept while it has more than a week left: the same secret, not a new one.
	for _, d := range []time.Duration{time.Minute, 100 * 24 * time.Hour, 173*24*time.Hour - time.Second} {
		if s, changed, err := a.at(t0.Add(d)); err != nil || changed || s != s1 {
			t.Fatalf("after %v: a new secret (%v %v)", d, changed, err)
		}
	}
	// A week before its end it is replaced, before Apple would refuse it.
	t1 := t0.Add(173 * 24 * time.Hour)
	s2, changed, err := a.at(t1)
	if err != nil || !changed || s2 == s1 {
		t.Fatalf("not renewed: %v %v", changed, err)
	}
	_, c2 := appleClaims(t, s2, &key.PublicKey)
	if int64(c2["iat"].(float64)) != t1.Unix() || int64(c2["exp"].(float64)) != t1.Add(180*24*time.Hour).Unix() {
		t.Fatal(c2)
	}
	if s, changed, _ := a.at(t1.Add(time.Hour)); changed || s != s2 {
		t.Fatal("the renewed secret was not kept")
	}
	// Another key does not verify it.
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if obj, err := jose.ParseSigned(s2, []jose.SignatureAlgorithm{jose.ES256}); err != nil {
		t.Fatal(err)
	} else if _, err := obj.Verify(&other.PublicKey); err == nil {
		t.Fatal("verified with another key")
	}
}

// Only a P-256 key in PKCS #8 PEM (Apple's .p8) is taken.
func TestParseAppleKeyRefuses(t *testing.T) {
	t.Parallel()
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sec1, _ := x509.MarshalECPrivateKey(p256)
	for name, text := range map[string]string{
		"empty":       "",
		"not pem":     "MIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQg",
		"rsa":         p8(t, rk),
		"p-384":       p8(t, p384),
		"sec1, not 8": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})),
		"a public key": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: func() []byte {
			b, _ := x509.MarshalPKIXPublicKey(&p256.PublicKey)
			return b
		}()})),
	} {
		if _, err := parseAppleKey(text); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}

// A multi-tenant issuer is the template with the token's own tid in it, and
// a tid is a tenant id; anything else is refused.
func TestTenantIssuer(t *testing.T) {
	t.Parallel()
	const pattern = "https://login.microsoftonline.com/{tenantid}/v2.0"
	const tid = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	const other = "9188040d-6c67-4c5b-b112-36a304b66dad"
	for _, c := range []struct {
		name, iss, tid string
		want           bool
	}{
		{"its tenant", "https://login.microsoftonline.com/" + tid + "/v2.0", tid, true},
		{"personal accounts' tenant", "https://login.microsoftonline.com/" + other + "/v2.0", other, true},
		{"another tenant's issuer", "https://login.microsoftonline.com/" + other + "/v2.0", tid, false},
		{"the pattern itself", pattern, "{tenantid}", false},
		{"the pattern, the tid given", pattern, tid, false},
		{"common", "https://login.microsoftonline.com/common/v2.0", "common", false},
		{"no tid", "https://login.microsoftonline.com//v2.0", "", false},
		{"upper case tid", "https://login.microsoftonline.com/" + strings.ToUpper(tid) + "/v2.0", strings.ToUpper(tid), false},
		{"a path in the tid", "https://login.microsoftonline.com/" + tid + "/x/v2.0", tid + "/x", false},
		{"v1 issuer", "https://sts.windows.net/" + tid + "/", tid, false},
		{"another host", "https://login.evil.test/" + tid + "/v2.0", tid, false},
		{"a suffix", "https://login.microsoftonline.com/" + tid + "/v2.0/", tid, false},
	} {
		if got := tenantIssuerOK(pattern, c.iss, c.tid); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if tenantIssuerOK("https://login.microsoftonline.com/common/v2.0", "https://login.microsoftonline.com/common/v2.0", tid) {
		t.Error("a pattern without {tenantid}")
	}
	if tenantIssuerOK("https://x/{tenantid}/{tenantid}", "https://x/"+tid+"/"+tid, tid) {
		t.Error("a pattern with two")
	}
}

// What a template fills is its own and is copied: a caller cannot change the
// list, and the Microsoft template is the multi-tenant one.
func TestTemplatesAreCopies(t *testing.T) {
	t.Parallel()
	l := ProviderTemplates()
	l[0].Scopes[0] = "changed"
	if providerTemplates[0].Scopes[0] == "changed" {
		t.Fatal("the list is shared")
	}
	ids := map[string]ProviderTemplate{}
	for _, tm := range ProviderTemplates() {
		ids[tm.ID] = tm
		if tm.Class != ClassSocial || !strings.HasPrefix(tm.Issuer, "https://") || tm.Icon == "" {
			t.Errorf("%+v", tm)
		}
		if tm.Kind == KindOIDC && (tm.AuthorizationEndpoint == "" || tm.TokenEndpoint == "" || tm.JWKSURI == "" || len(tm.authMethods) == 0) {
			t.Errorf("%s has no addresses", tm.ID)
		}
	}
	for _, id := range []string{TemplateGoogle, TemplateMicrosoft, TemplateApple, TemplateGitHub, TemplateGitLabCom} {
		if _, ok := ids[id]; !ok {
			t.Errorf("no %s", id)
		}
	}
	if !multiTenant(ids[TemplateMicrosoft].Issuer) || multiTenant(ids[TemplateGoogle].Issuer) {
		t.Fatal("multi-tenant")
	}
	if ids[TemplateApple].ResponseMode != "form_post" || ids[TemplateApple].Secret != "apple" {
		t.Fatal("apple")
	}
	if got := appleUserName(`{"name":{"firstName":" Ann ","lastName":"Lee"},"email":"x@y"}`); got != "Ann Lee" {
		t.Fatal(got)
	}
	if appleUserName(`not json`) != "" || appleUserName(strings.Repeat("x", 5000)) != "" {
		t.Fatal("apple user")
	}
}
