package oauth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/KKazuhaK/PetalBook/internal/secret"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The signing key is bound to this purpose when sealed, so a sealed value
// of another purpose copied into its row does not open.
const (
	keySpace   = "oauth"
	keyPurpose = "signing-key"
)

type signingKey struct {
	priv *ecdsa.PrivateKey
	kid  string
}

func newSigningKey(priv *ecdsa.PrivateKey) (*signingKey, error) {
	if priv.Curve != elliptic.P256() {
		return nil, errors.New("the signing key must be an ES256 key (ECDSA on curve P-256)")
	}
	jwk := jose.JSONWebKey{Key: &priv.PublicKey}
	tp, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, err
	}
	return &signingKey{priv: priv, kid: base64.RawURLEncoding.EncodeToString(tp)}, nil
}

func parseKey(pemBytes []byte) (*signingKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("no PEM private key found")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the signing key must be an ES256 key (ECDSA on curve P-256)")
	}
	return newSigningKey(ec)
}

// loadKey reads the signing key from the state database, making and storing it
// at the first start. Of two servers starting together on one database the first
// to store wins and the other uses that key.
func loadKey(ctx context.Context, st *store.Store, box *secret.Box, now time.Time) (*signingKey, error) {
	read := func() (*signingKey, bool, error) {
		sealed, ok, err := store.GetOAuthSigningKey(ctx, st.Q())
		if err != nil || !ok {
			return nil, false, err
		}
		pt, err := box.Open(keySpace, keyPurpose, sealed)
		if err != nil {
			return nil, true, fmt.Errorf("the stored signing key cannot be opened (was the server key changed?): %w", err)
		}
		k, err := parseKey(pt)
		return k, true, err
	}
	if k, ok, err := read(); ok || err != nil {
		return k, err
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	sealed, err := box.Seal(keySpace, keyPurpose, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		return nil, err
	}
	won, err := store.InsertOAuthSigningKey(ctx, st.Q(), sealed, now)
	if err != nil {
		return nil, err
	}
	if won {
		return newSigningKey(priv)
	}
	k, ok, err := read()
	if err == nil && !ok {
		err = errors.New("the signing key vanished")
	}
	return k, err
}
