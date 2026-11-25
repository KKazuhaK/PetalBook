package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"petalbook.app/server/internal/store"
)

// maxRegistrations is how many clients may be registered at once (a variable
// for the tests).
var maxRegistrations = 1000

// Limits of dynamic client registration (RFC 7591), which anyone who can reach
// the server may call: the table stays small and cannot be filled by a flood.
const (
	maxRegisterBody   = 16 << 10
	registerWindow    = 10 * time.Minute
	registerPerWindow = 30
	unusedClientTTL   = 24 * time.Hour      // registered, never authorized
	idleClientTTL     = 30 * 24 * time.Hour // authorized before, no grant left
	clientIDPrefix    = "pbc_"
)

type registerRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	GrantTypes   []string `json:"grant_types"`
	ResponseType []string `json:"response_types"`
}

func fingerprint(name string, uris []string) string {
	b, _ := json.Marshal(uris)
	h := sha256.Sum256(append([]byte(name+"\x00"), b...))
	return hex.EncodeToString(h[:])
}

// registered finds a client that registered itself.
func (s *Service) registered(ctx context.Context, id string) (*store.OAuthClient, error) {
	if len(id) > 128 {
		return nil, errUnknownClient
	}
	c, err := store.GetOAuthClient(ctx, s.st.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnknownClient
	}
	return c, err
}

func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "use POST")
		return
	}
	var req registerRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRegisterBody)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body must be a JSON object")
		return
	}
	if req.AuthMethod != "" && req.AuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients are supported (token_endpoint_auth_method none)")
		return
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "grant_types may only be authorization_code and refresh_token")
			return
		}
	}
	for _, t := range req.ResponseType {
		if t != "code" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "response_types may only be code")
			return
		}
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > maxRedirects {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must list 1 to 10 URIs")
		return
	}
	for _, u := range req.RedirectURIs {
		if err := validateRedirect(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	name := cleanName(req.ClientName, "MCP client")
	now := s.now()
	fp := fingerprint(name, req.RedirectURIs)
	ip := s.cfg.ClientIP(r)
	ctx := r.Context()

	// A registration that already exists costs nothing and is not limited, so a
	// client that registers at every start never uses up anyone's allowance.
	out, err := store.FindOAuthClient(ctx, s.st.Q(), fp)
	created := false
	switch {
	case err == nil:
	case !errors.Is(err, store.ErrNotFound):
		s.fail(w, "registration", err)
		return
	default:
		if blocked, wait := s.regLimit.Blocked(ip); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many registrations from this address, try again later")
			return
		}
		s.regLimit.Fail(ip)
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			s.fail(w, "registration", err)
			return
		}
		c := store.OAuthClient{ID: clientIDPrefix + base64.RawURLEncoding.EncodeToString(id[:]), Name: name, RedirectURIs: req.RedirectURIs, Fingerprint: fp, Created: now}
		full := false
		err = s.st.Tx(ctx, func(q store.Q) error {
			if _, err := store.PruneOAuthClients(ctx, q, now.Add(-unusedClientTTL), now.Add(-idleClientTTL)); err != nil {
				return err
			}
			n, err := store.CountOAuthClients(ctx, q)
			if err != nil {
				return err
			}
			if n >= maxRegistrations {
				if ok, err := store.DeleteOldestUnusedOAuthClient(ctx, q); err != nil || !ok {
					full = err == nil
					return err
				}
			}
			return store.InsertOAuthClient(ctx, q, c)
		})
		switch {
		case full:
			w.Header().Set("Retry-After", "3600")
			oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many clients are registered, try again later")
			return
		case store.IsUnique(err): // the same details were registered at the same moment
			out, err = store.FindOAuthClient(ctx, s.st.Q(), fp)
			if err != nil {
				s.fail(w, "registration", err)
				return
			}
		case err != nil:
			s.fail(w, "registration", err)
			return
		default:
			out, created = &c, true
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		s.log.Info("oauth client registered", "client", out.ID, "name", out.Name)
	}
	writeJSON(w, status, map[string]any{
		"client_id": out.ID, "client_id_issued_at": out.Created.Unix(), "client_name": out.Name,
		"redirect_uris": out.RedirectURIs, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}
