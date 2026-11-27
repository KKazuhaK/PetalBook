// Package secrets is the one home of every secret the server keeps for the site
// and for each tenant: the GitHub App's key, the mail password, the captcha
// secret, and what a tenant's own sender or sign-in will need. A secret is
// write-only: the API says whether one is set and when it changed, never its
// value, which goes only to the code that uses it (Open). The values are sealed
// with the server's key (internal/secret) and live in the state database, so a
// backup or a move of it carries them.
//
// Nothing here logs or audits a value.
package secrets

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/store"
)

// The names of the secrets of the site and of tenants.
const (
	GitHubAppKey        = "github.appKey"
	GitHubClientSecret  = "github.clientSecret"
	GitHubWebhookSecret = "github.webhookSecret"
	MailPassword        = "mail.password"
	CaptchaSecret       = "captcha.secret"
)

// MaxBytes is the longest secret (a private key in PEM form is the largest).
const MaxBytes = 16 << 10

// Spec says who may hold a secret of a name.
type Spec struct {
	Name string
	// Site and Tenant: whether the site, and a tenant, may hold one.
	Site, Tenant bool
	// Feature is the setting (a feature, a boolean) that must be on for a
	// tenant before it may hold the secret: what the site allows it. "" for
	// none.
	Feature string
	// ManagedBy names the endpoint that changes it, when the secret is checked
	// with the service it is for (the generic endpoints refuse to change it).
	ManagedBy string
}

// Known are the secrets that exist. A tenant's own sender is the only secret a
// tenant holds yet; its sign-in and App secrets come with their features.
var Known = []Spec{
	{Name: GitHubAppKey, Site: true, ManagedBy: "/api/admin/providers/github"},
	{Name: GitHubClientSecret, Site: true, ManagedBy: "/api/admin/providers/github"},
	{Name: GitHubWebhookSecret, Site: true, ManagedBy: "/api/admin/providers/github"},
	{Name: MailPassword, Site: true, Tenant: true, Feature: "mail.ownSender"},
	{Name: CaptchaSecret, Site: true},
}

// SpecOf returns the spec of a name.
func SpecOf(name string) (Spec, bool) {
	for _, s := range Known {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Error is a refusal, with the code of the API.
type Error struct{ Code, Msg string }

func (e *Error) Error() string { return e.Msg }

// Service reads and writes the secrets of one store.
type Service struct {
	st  *store.Store
	box atomic.Pointer[secret.Box]
	now func() time.Time
}

// Of returns the secrets of a store: one for all who ask, made with the box of
// the first that gives one (callers may ask at the same time).
func Of(st *store.Store, box *secret.Box) *Service {
	s := st.Shared("secrets", func() any { return &Service{st: st, now: time.Now} }).(*Service)
	if box != nil {
		s.box.CompareAndSwap(nil, box)
	}
	return s
}

// SetClock sets the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

func scopeKey(tenant string) string {
	if tenant == "" {
		return "site"
	}
	return "tenant:" + tenant
}

// check refuses a name or a value no secret can have, and a scope that may not
// hold the name.
func check(tenant, name string) error {
	sp, ok := SpecOf(name)
	switch {
	case !ok:
		return &Error{"not_found", "No such secret."}
	case tenant == "" && !sp.Site, tenant != "" && !sp.Tenant:
		return &Error{"not_found", "No such secret."}
	}
	return nil
}

// Set stores a secret in the transaction q, replacing the one of that name. by
// is who sets it (a user id).
func (s *Service) Set(ctx context.Context, q store.Q, tenant, name, value, by string) error {
	if err := check(tenant, name); err != nil {
		return err
	}
	// Stored as given: spaces at either end may be part of a secret.
	if value == "" {
		return &Error{"invalid", "A secret is not empty; clear it instead."}
	}
	if len(value) > MaxBytes {
		return &Error{"invalid", "A secret is at most 16 KiB."}
	}
	box := s.box.Load()
	if box == nil {
		return errors.New("secrets: no key to seal with")
	}
	sealed, err := box.Seal(scopeKey(tenant), name, []byte(value))
	if err != nil {
		return err
	}
	return store.PutSecret(ctx, q, store.SecretRow{TenantID: tenant, Name: name, Sealed: sealed, Updated: s.now(), UpdatedBy: by})
}

// Clear removes a secret in the transaction q.
func (s *Service) Clear(ctx context.Context, q store.Q, tenant, name string) error {
	if err := check(tenant, name); err != nil {
		return err
	}
	return store.DeleteSecret(ctx, q, tenant, name)
}

// Open returns the value of a secret, for the code that uses it; ok is false
// when there is none. A value this server's key cannot open is an error.
func (s *Service) Open(ctx context.Context, q store.Q, tenant, name string) (value string, ok bool, err error) {
	r, err := store.GetSecret(ctx, q, tenant, name)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	box := s.box.Load()
	if box == nil {
		return "", false, errors.New("secrets: no key to open with")
	}
	b, err := box.Open(scopeKey(tenant), name, r.Sealed)
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// Meta says whether a secret is set and when it changed: all the API tells.
type Meta struct {
	Name      string    `json:"name"`
	Set       bool      `json:"set"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// List tells, for each secret the scope may hold, whether it is set.
func (s *Service) List(ctx context.Context, q store.Q, tenant string) ([]Meta, error) {
	rows, err := store.ListSecrets(ctx, q, tenant)
	if err != nil {
		return nil, err
	}
	have := map[string]time.Time{}
	for _, r := range rows {
		have[r.Name] = r.Updated
	}
	out := []Meta{}
	for _, sp := range Known {
		if tenant == "" && !sp.Site || tenant != "" && !sp.Tenant {
			continue
		}
		at, set := have[sp.Name]
		out = append(out, Meta{Name: sp.Name, Set: set, UpdatedAt: at})
	}
	return out, nil
}

// Has says whether a secret is set.
func (s *Service) Has(ctx context.Context, q store.Q, tenant, name string) (bool, error) {
	_, err := store.GetSecret(ctx, q, tenant, name)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
