package auth

import (
	"context"

	"petalbook.app/server/internal/store"
)

// notices are what the administrators should know.
func (s *Service) notices(ctx context.Context) []Notice {
	out := []Notice{}
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
