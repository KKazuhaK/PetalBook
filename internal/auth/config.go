package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"petalbook.app/server/internal/store"
)

const oidcImportedKey = "oidc_imported"

// importFlags turns the provider of the old --oidc-* flags into the provider
// "sso" of the state database, once. Nobody is locked out by it: the login mode
// stays dual, ssoOnly stays off, the provider has the legacy callback (the
// address the identity provider already knows), and identities are kept by
// issuer and subject, which have not changed. After that the database is what
// counts: flags that differ only warn.
func (s *Service) importFlags(ctx context.Context) error {
	c := s.cfg.OIDC
	if c == nil {
		return nil
	}
	raw, _, err := store.GetRecord(ctx, s.st.Q(), oidcImportedKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		var done struct {
			Slug string `json:"slug"`
		}
		_ = json.Unmarshal([]byte(raw), &done)
		rt := s.provs.get(done.Slug)
		if rt == nil || !s.flagsMatch(rt, c) {
			s.flagsIgnored.Store(true)
			s.log.Warn("the --oidc-* flags differ from the provider stored in the state database, which is what counts now; they change nothing and will be removed in a later version", "provider", done.Slug)
		}
		return nil
	}
	providerMu.Lock()
	defer providerMu.Unlock()
	name := c.Name
	if name == "" {
		name = "Single sign-on"
	}
	scopes := slices.Clone(c.Scopes)
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	now := s.now().UTC()
	cfg := provCfg{Issuer: c.Issuer, ClientID: c.ClientID, Scopes: scopes}
	rawCfg, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	row := store.ProviderRow{Slug: "sso", Kind: KindOIDC, Name: name, Enabled: true, AutoCreate: c.AutoCreate, LegacyCallback: true,
		Issuer: c.Issuer, Config: string(rawCfg), Created: now, Updated: now}
	if c.ClientSecret != "" {
		if row.SecretEnc, err = s.seal("provider:sso", c.ClientSecret); err != nil {
			return err
		}
	}
	err = store.InsertProvider(ctx, s.st.Q(), row)
	if store.IsUnique(err) {
		// A provider with this slug exists already (made by hand before the
		// import): leave it, and do not import again.
		s.log.Warn("a provider with the slug sso exists already; the --oidc-* flags were not imported")
	} else if err != nil {
		return err
	} else {
		s.log.Info("the --oidc-* flags became the sign-in provider \"sso\" of the state database; change it on the 安全 page, and remove the flags", "issuer", c.Issuer)
	}
	done, _ := json.Marshal(map[string]string{"slug": "sso"})
	if _, err := store.PutRecord(ctx, s.st.Q(), oidcImportedKey, string(done), s.now()); err != nil {
		return err
	}
	return s.reloadProviders(ctx)
}

func (s *Service) flagsMatch(rt *providerRT, c *OIDCConfig) bool {
	name := c.Name
	if name == "" {
		name = "Single sign-on"
	}
	scopes := c.Scopes
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	return rt.row.Kind == KindOIDC && rt.cfg.Issuer == c.Issuer && rt.cfg.ClientID == c.ClientID && rt.secret == c.ClientSecret &&
		rt.row.Name == name && rt.row.AutoCreate == c.AutoCreate && slices.Equal(rt.cfg.Scopes, scopes) && rt.secretErr == nil
}

// notices are what the administrators should know.
func (s *Service) notices(ctx context.Context) []Notice {
	out := []Notice{}
	if s.flagsIgnored.Load() {
		out = append(out, Notice{Code: "flags_ignored", Message: "The --oidc-* flags differ from the stored provider; they change nothing."})
	}
	for _, rt := range s.provs.all() {
		if rt.secretErr != nil {
			out = append(out, Notice{Code: "secret_unreadable", Slug: rt.row.Slug, Message: "A stored secret of the provider " + rt.row.Slug + " cannot be opened with this server's key."})
		}
	}
	if s.capSecretErr.Load() {
		out = append(out, Notice{Code: "secret_unreadable", Field: "captcha.secret", Message: "The stored captcha secret cannot be opened with this server's key."})
	}
	if s.pk != nil {
		if n, err := store.PasskeysOfOtherHost(ctx, s.st.Q(), s.pk.rpID); err == nil && n > 0 {
			out = append(out, Notice{Code: "passkey_host_changed", Message: "Some passkeys were made for another host than " + s.pk.rpID + " and cannot be used."})
		}
	}
	return out
}
