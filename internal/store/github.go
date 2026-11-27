package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Credential type of a remote that syncs through a provider's connection (the
// remote's Provider says which: here only "github").
const CredConnection = "connection"

// ProviderGit is the provider of a remote with a plain address, a deploy key or a token.
const ProviderGit = "git"

// ProviderGitHub is the id of the GitHub provider: its row of providers, which its installations and shares belong to.
const ProviderGitHub = "github"

// CountGitHubSpaces is how many spaces sync through GitHub, for their own
// repository or their attachment repository (milestone 3.1): the counts below
// read both roles of remotes in one scan, a space counted once.
func CountGitHubSpaces(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM remotes WHERE url <> '' AND cred_type = 'connection' AND provider = 'github'`).Scan(&n)
	return
}

// CountGitSpaces is how many spaces sync to a plain Git address (a deploy key
// or a token), for the provider "git": their own repository or their
// attachment repository.
func CountGitSpaces(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM remotes WHERE url <> '' AND provider = 'git'`).Scan(&n)
	return
}

// CountUserGitHubSpaces is how many spaces sync through a user's authorisation.
func CountUserGitHubSpaces(ctx context.Context, q Q, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM remotes
		WHERE cred_user_id = ? AND cred_user_id <> '' AND url <> '' AND cred_type = 'connection' AND provider = 'github' AND via = 'user'`, userID).Scan(&n)
	return
}

// GitHubSpaceCounts is how many of the owner's spaces sync through each of
// the installations. A space of another owner that still names an installation
// (it synced through it before the installation changed hands) is not counted.
func GitHubSpaceCounts(ctx context.Context, q Q, o InstallOwner, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 || !o.Valid() {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	oc, oa := ownersSpace(o)
	rows, err := q.Query(ctx, `SELECT r.installation_id, COUNT(DISTINCT r.space_id) FROM remotes r JOIN spaces s ON s.id = r.space_id
		WHERE r.installation_id IN (`+strings.Join(marks, ",")+`) AND r.installation_id <> 0 AND r.url <> '' AND r.cred_type = 'connection' AND r.provider = 'github' AND r.via = 'app'
		AND `+oc+`
		GROUP BY r.installation_id`, append(args, oa...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// GitHubInstallation is an installation of the App connected to its one owner
// on this server: a person (OwnerUser) or a tenant (OwnerTenant).
type GitHubInstallation struct {
	ID          int64
	Account     string
	AccountID   int64
	AccountType string
	OwnerUser   string
	OwnerTenant string
	ClaimedBy   string
	Created     time.Time
	Updated     time.Time
}

// InstallOwner is who an installation belongs to: exactly one of a person and a tenant.
type InstallOwner struct {
	UserID   string
	TenantID string
}

// Valid says whether exactly one of the two is given.
func (o InstallOwner) Valid() bool { return (o.UserID == "") != (o.TenantID == "") }

// Owner is the installation's owner.
func (i *GitHubInstallation) Owner() InstallOwner {
	return InstallOwner{UserID: i.OwnerUser, TenantID: i.OwnerTenant}
}

// ErrInstallationTaken: the installation is connected to another owner on this server.
var ErrInstallationTaken = errors.New("the installation belongs to another owner on this server")

const ghInstCols = `i.id, i.account, i.account_id, i.account_type, COALESCE(i.owner_user, ''), COALESCE(i.owner_tenant, ''), i.claimed_by, i.created, i.updated`

func scanGitHubInstallation(row interface{ Scan(...any) error }) (*GitHubInstallation, error) {
	var i GitHubInstallation
	var cr, up int64
	if err := row.Scan(&i.ID, &i.Account, &i.AccountID, &i.AccountType, &i.OwnerUser, &i.OwnerTenant, &i.ClaimedBy, &cr, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	i.Created, i.Updated = fromMS(cr), fromMS(up)
	return &i, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetGitHubInstallation returns the installation's claim, or ErrNotFound when
// no one on this server has connected it.
func GetGitHubInstallation(ctx context.Context, q Q, id int64) (*GitHubInstallation, error) {
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM provider_installations i WHERE i.provider_id = ? AND i.id = ?`, ProviderGitHub, id))
}

// ClaimGitHubInstallation connects an installation to its owner (i.OwnerUser
// or i.OwnerTenant), or refreshes what is stored of it when that owner has it
// already. It is one statement: of two owners claiming at once, one wins and
// the other gets ErrInstallationTaken, as does any owner while another has it.
func ClaimGitHubInstallation(ctx context.Context, q Q, i GitHubInstallation) error {
	if !i.Owner().Valid() || i.ID <= 0 {
		return errors.New("store: an installation is claimed by exactly one owner")
	}
	res, err := q.Exec(ctx, `INSERT INTO provider_installations(provider_id, id, account, account_id, account_type, owner_user, owner_tenant, claimed_by, created, updated)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (provider_id, id) DO UPDATE SET account = excluded.account, account_id = excluded.account_id, account_type = excluded.account_type, updated = excluded.updated
		WHERE COALESCE(provider_installations.owner_user, '') = COALESCE(excluded.owner_user, '')
			AND COALESCE(provider_installations.owner_tenant, '') = COALESCE(excluded.owner_tenant, '')`,
		ProviderGitHub, i.ID, i.Account, i.AccountID, i.AccountType, nullIfEmpty(i.OwnerUser), nullIfEmpty(i.OwnerTenant), i.ClaimedBy, ms(i.Created), ms(i.Updated))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInstallationTaken
	}
	return nil
}

// ownerCond is the condition that an installation row (its columns prefixed
// with p) belongs to o.
func ownerCond(p string, o InstallOwner) (string, []any) {
	if o.UserID != "" {
		return p + `owner_user = ?`, []any{o.UserID}
	}
	return p + `owner_tenant = ?`, []any{o.TenantID}
}

// maxOwnedInstallations bounds the installations one owner's lists read.
const maxOwnedInstallations = 100

// ListGitHubInstallationsOwned lists an owner's installations by id (at most 100).
func ListGitHubInstallationsOwned(ctx context.Context, q Q, o InstallOwner) ([]GitHubInstallation, error) {
	if !o.Valid() {
		return nil, nil
	}
	c, a := ownerCond("i.", o)
	rows, err := q.Query(ctx, `SELECT `+ghInstCols+` FROM provider_installations i WHERE i.provider_id = ? AND `+c+` ORDER BY i.id LIMIT ?`,
		append(append([]any{ProviderGitHub}, a...), maxOwnedInstallations)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubInstallation
	for rows.Next() {
		i, err := scanGitHubInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// GitHubInstallationOwnedOn returns the owner's installation on an account (by
// login, case ignored), or ErrNotFound.
func GitHubInstallationOwnedOn(ctx context.Context, q Q, o InstallOwner, login string) (*GitHubInstallation, error) {
	if !o.Valid() {
		return nil, ErrNotFound
	}
	c, a := ownerCond("i.", o)
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM provider_installations i
		WHERE i.provider_id = ? AND lower(i.account) = ? AND `+c+` ORDER BY i.id LIMIT 1`, append([]any{ProviderGitHub, strings.ToLower(login)}, a...)...))
}

// SpaceRef names a space in a list of those something affects.
type SpaceRef struct {
	ID, Name, TenantID string
}

// maxAffectedSpaces bounds the spaces a list of affected spaces names.
const maxAffectedSpaces = 200

// ownersSpace is the condition that a remote r of a space s is the owner's to
// sync through its installation: an organisation's space (one of the tenant
// that is no one's personal space), or a person's space (of the default
// tenant, or a personal one) whose remote names that person.
func ownersSpace(o InstallOwner) (string, []any) {
	if o.UserID != "" {
		return `r.cred_user_id = ? AND (s.tenant_id = ? OR s.personal_of IS NOT NULL)`, []any{o.UserID, DefaultTenantID}
	}
	return `s.tenant_id = ? AND s.personal_of IS NULL`, []any{o.TenantID}
}

// onInstallation is the condition that a remote r syncs through installation ?.
const onInstallation = `r.installation_id = ? AND r.installation_id <> 0 AND r.url <> '' AND r.cred_type = 'connection' AND r.provider = 'github' AND r.via = 'app'`

// SpacesOnGitHubInstallation lists the owner's spaces that sync through an
// installation (their own repository or their attachment repository), by name,
// at most 200, with how many there are in all: each one scan of
// remotes_installation. Another owner's spaces that still name the
// installation (from before it changed hands) are not the owner's to see.
func SpacesOnGitHubInstallation(ctx context.Context, q Q, o InstallOwner, id int64) ([]SpaceRef, int, error) {
	if !o.Valid() {
		return nil, 0, nil
	}
	oc, oa := ownersSpace(o)
	var n int
	if err := q.QueryRow(ctx, `SELECT COUNT(DISTINCT r.space_id) FROM remotes r JOIN spaces s ON s.id = r.space_id WHERE `+onInstallation+` AND `+oc,
		append([]any{id}, oa...)...).Scan(&n); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT s.id, s.name, s.tenant_id FROM spaces s
		WHERE s.id IN (SELECT r.space_id FROM remotes r WHERE `+onInstallation+` AND `+oc+`) ORDER BY s.name_key, s.id LIMIT ?`,
		append(append([]any{id}, oa...), maxAffectedSpaces)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []SpaceRef
	for rows.Next() {
		var r SpaceRef
		if err := rows.Scan(&r.ID, &r.Name, &r.TenantID); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, n, rows.Err()
}

// AccessLost is the code a remote shows when the installation it syncs through
// is no longer connected to the space's owner on this server.
const AccessLost = "access_lost"

// DisconnectGitHubInstallation removes an owner's claim of an installation and
// marks every remote that synced through it as having lost GitHub access (the
// spaces and their notes stay; the remote rows stay too, so the same
// installation connected again by that owner restores them). It reports the
// ids of the spaces marked; ErrNotFound when the owner has no such installation.
func DisconnectGitHubInstallation(ctx context.Context, q Q, id int64, o InstallOwner, msg string, now time.Time) ([]string, error) {
	if !o.Valid() {
		return nil, ErrNotFound
	}
	var ids []string
	err := atomically(ctx, q, func(q Q) error {
		c, a := ownerCond("", o)
		res, err := q.Exec(ctx, `DELETE FROM provider_installations WHERE provider_id = ? AND id = ? AND `+c, append([]any{ProviderGitHub, id}, a...)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		oc, oa := ownersSpace(o)
		rows, err := q.Query(ctx, `SELECT DISTINCT r.space_id FROM remotes r JOIN spaces s ON s.id = r.space_id WHERE `+onInstallation+` AND `+oc,
			append([]any{id}, oa...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, sid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Only those spaces' remotes, in batches of the ids found above.
		for rest := ids; len(rest) > 0; {
			n := min(len(rest), 500)
			marks := strings.Repeat(",?", n-1)
			args := []any{RemoteError, AccessLost, msg, ms(now), id}
			for _, sid := range rest[:n] {
				args = append(args, sid)
			}
			if _, err := q.Exec(ctx, `UPDATE remotes SET state = ?, error_kind = ?, last_error = ?, updated = ?
				WHERE installation_id = ? AND installation_id <> 0 AND url <> '' AND cred_type = 'connection' AND provider = 'github' AND via = 'app'
				AND space_id IN (?`+marks+`)`, args...); err != nil {
				return err
			}
			sargs := []any{RemoteError, RemoteError}
			for _, sid := range rest[:n] {
				sargs = append(sargs, sid)
			}
			if _, err := q.Exec(ctx, `UPDATE spaces SET remote_state = ? WHERE remote_state <> ? AND id IN (?`+marks+`)
				AND EXISTS (SELECT 1 FROM remotes r WHERE r.space_id = spaces.id AND r.role = 'content' AND `+onInstallation+`)`, append(sargs, id)...); err != nil {
				return err
			}
			// Their grace period under the storage requirement starts again now:
			// time to get a new repository (a space whose remote is only an
			// attachment repository has not lost its content remote). Only
			// when the remote synced well since the last loss, or there was
			// none: connecting and disconnecting again without a successful
			// sync does not give more time. The value is never cleared on a
			// reconnect (a space with a usable remote is not asked for one,
			// and the first loss comes back if it is disconnected again); a
			// later loss after a good sync replaces it.
			largs := []any{ms(now)}
			for _, sid := range rest[:n] {
				largs = append(largs, sid)
			}
			if _, err := q.Exec(ctx, `UPDATE spaces SET remote_lost_at = ? WHERE id IN (?`+marks+`)
				AND EXISTS (SELECT 1 FROM remotes r WHERE r.space_id = spaces.id AND r.role = 'content' AND `+onInstallation+`
					AND (spaces.remote_lost_at = 0 OR r.last_sync > spaces.remote_lost_at))`, append(largs, id)...); err != nil {
				return err
			}
			rest = rest[n:]
		}
		return nil
	})
	return ids, err
}

// ClaimedBySpaceOwner is the SQL that a remote r of a space s syncs through an
// installation the space's owner holds on this server: an organisation's
// space (s.tenant_id) through the tenant's own, a person's through the one of
// the person the remote names (r.cred_user_id).
const ClaimedBySpaceOwner = `EXISTS (SELECT 1 FROM provider_installations pi WHERE pi.provider_id = 'github' AND pi.id = r.installation_id
	AND (pi.owner_tenant = s.tenant_id OR (pi.owner_user IS NOT NULL AND pi.owner_user = r.cred_user_id)))`

// usableRemote is the SQL that a remote r of a space s can still be synced on
// the face of it: a GitHub remote through an installation needs the
// installation still connected to the space's owner (else it lost access).
const usableRemote = `NOT (r.provider = 'github' AND r.via = 'app' AND NOT ` + ClaimedBySpaceOwner + `)`

// MaxGitHubLeftovers is how many reminders of empty repositories a user keeps
// at a provider: the newest.
const MaxGitHubLeftovers = 20

// UserTenant returns the tenant of a user, or ErrNotFound.
func UserTenant(ctx context.Context, q Q, userID string) (string, error) {
	var t string
	err := q.QueryRow(ctx, `SELECT tenant_id FROM users WHERE id = ?`, userID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}

// MemberStanding is every role through which a user reaches a space (their
// direct role, if any, then the roles of their groups that are members of it)
// and whether they are disabled; ErrNotFound when they reach it by neither
// (or are not a user). Their permission is the union of the roles.
func MemberStanding(ctx context.Context, q Q, spaceID, userID string) (roles []string, disabled bool, err error) {
	rows, err := q.Query(ctx, `SELECT x.role, u.disabled FROM users u JOIN (
			SELECT role, 0 AS ord FROM space_members WHERE space_id = ? AND user_id = ?
			UNION ALL
			SELECT sg.role, 1 AS ord FROM space_groups sg JOIN group_members gm ON gm.group_id = sg.group_id
				WHERE sg.space_id = ? AND gm.user_id = ?
		) x ON 1 = 1
		WHERE u.id = ? ORDER BY x.ord, x.role`, spaceID, userID, spaceID, userID, userID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var d int64
		if err := rows.Scan(&role, &d); err != nil {
			return nil, false, err
		}
		roles = append(roles, role)
		disabled = d != 0
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(roles) == 0 {
		return nil, false, ErrNotFound
	}
	return roles, disabled, nil
}

// --- the GitHub App's record ----------------------------------------------------------

// The GitHub row of providers (id "github") is both what people may sign in
// with and where repositories are connected. Its App is the JSON under the
// key "app" of its config (id, key fingerprint, slug and what GitHub last said
// of it); the App's client id is "clientId" there, as a sign-in provider keeps
// it. These keys belong to whoever sets the App up (PutGitHubApp,
// CompareAndSetGitHubApp), and UpdateProvider keeps them as they are stored.
// The key, the client secret and the webhook secret are in secrets.
const (
	githubAppKey      = "app"
	githubClientIDKey = "clientId"
	githubBaseURLKey  = "baseUrl"
)

// GitHubApp is the App's own writes to the GitHub row.
type GitHubApp struct {
	// App is the App's record as JSON.
	App string
	// ClientID is the App's OAuth client id ("" when the App has none: people
	// cannot sign in or connect with it).
	ClientID string
	// BaseURL is GitHub's web address, which sign-in goes to.
	BaseURL string
}

func configMap(raw string) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if raw == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// keepAppConfig is next with the App's keys of cur (the stored config).
func keepAppConfig(cur, next string) (string, error) {
	have, err := configMap(cur)
	if err != nil {
		return next, nil // a stored config nobody can read has no App in it
	}
	app, ok := have[githubAppKey]
	if !ok {
		return next, nil
	}
	m, err := configMap(next)
	if err != nil {
		return "", err
	}
	m[githubAppKey] = app
	if v, ok := have[githubClientIDKey]; ok {
		m[githubClientIDKey] = v
	}
	if v, ok := have[githubBaseURLKey]; ok {
		m[githubBaseURLKey] = v
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// GetGitHubApp returns the App's record, or false when there is no GitHub row
// or it holds no App.
func GetGitHubApp(ctx context.Context, q Q) (app string, ok bool, err error) {
	var cfg string
	err = q.QueryRow(ctx, `SELECT config FROM providers WHERE id = ? AND kind = 'github'`, ProviderGitHub).Scan(&cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	m, err := configMap(cfg)
	if err != nil {
		return "", false, err
	}
	raw, ok := m[githubAppKey]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	return string(raw), true, nil
}

// PutGitHubApp stores the App on the GitHub row, making the row when there is
// none (a sign-in provider that is off at first, which the administrator may
// turn on). A row an administrator made for signing in with GitHub keeps its
// settings and takes the App's client: its own sealed secret is dropped, the
// App's is in secrets. The row is marked as one that holds repositories.
func PutGitHubApp(ctx context.Context, q Q, a GitHubApp, now time.Time) error {
	return atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `UPDATE providers SET id = id WHERE id = ?`, ProviderGitHub); err != nil {
			return err
		}
		var cfg string
		err := q.QueryRow(ctx, `SELECT config FROM providers WHERE id = ? AND kind = 'github'`, ProviderGitHub).Scan(&cfg)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return err
		}
		m, err := configMap(cfg)
		if err != nil {
			return err
		}
		if !missing {
			// A row people signed in with on one GitHub never takes another's App.
			if err := refuseGitHubRehost(ctx, q, ProviderGitHub, baseURLOf(m), a.BaseURL); err != nil {
				return err
			}
		}
		m[githubAppKey] = json.RawMessage(a.App)
		m[githubClientIDKey], _ = json.Marshal(a.ClientID)
		m[githubBaseURLKey], _ = json.Marshal(a.BaseURL)
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if missing {
			_, err := q.Exec(ctx, `INSERT INTO providers(id, owner, slug, kind, name, enabled, signin, repos, config, status_ok, jit_tenant, created, updated)
				VALUES (?, 'site', ?, 'github', 'GitHub', 0, 1, 1, ?, 1, ?, ?, ?)`,
				ProviderGitHub, ProviderGitHub, string(b), DefaultTenantID, ms(now), ms(now))
			return err
		}
		_, err = q.Exec(ctx, `UPDATE providers SET config = ?, secret_enc = '', signin = 1, repos = 1, updated = ?, groups_version = groups_version + 1 WHERE id = ?`,
			string(b), ms(now), ProviderGitHub)
		return err
	})
}

// GitHubHostError refuses to set an App up on a GitHub row that people linked
// on another GitHub host (Links of them): their links name accounts of that
// host, which the App's host would read as its own.
type GitHubHostError struct {
	Have, Want string
	Links      int
}

func (e *GitHubHostError) Error() string {
	return "the GitHub provider is on " + e.Have + ", not " + e.Want
}

// baseURLOf is the base URL of a GitHub config ("" when it has none).
func baseURLOf(m map[string]json.RawMessage) string {
	var s string
	if raw, ok := m[githubBaseURLKey]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// refuseGitHubRehost refuses (GitHubHostError) to move a GitHub provider row
// from host have to host want while accounts are linked to it: its links are
// keyed by GitHub's numeric user ids, which are only unique on one host, so
// an account of the new host with the same id would sign in as one of them.
// Every write of a GitHub row's base URL asks it, the row locked.
func refuseGitHubRehost(ctx context.Context, q Q, id, have, want string) error {
	if SameGitHubHost(have, want) {
		return nil
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM linked_accounts WHERE provider_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return &GitHubHostError{Have: githubHostOr(have), Want: githubHostOr(want), Links: n}
	}
	return nil
}

// githubHostOr is a stored base URL, github.com when there is none.
func githubHostOr(base string) string {
	if strings.TrimSpace(base) == "" {
		return "https://github.com"
	}
	return base
}

// SameGitHubHost says whether two base URLs are the same GitHub (scheme and
// host, case ignored; an empty one is github.com).
func SameGitHubHost(a, b string) bool {
	norm := func(s string) string {
		u, err := url.Parse(githubHostOr(s))
		if err != nil {
			return s
		}
		return strings.ToLower(u.Scheme + "://" + u.Host)
	}
	return norm(a) == norm(b)
}

// CompareAndSetGitHubApp replaces the App's record by next only if it still is
// old (what the caller read): the row is locked, so an App saved meanwhile is
// never overwritten. It reports whether it wrote.
func CompareAndSetGitHubApp(ctx context.Context, q Q, old, next string, now time.Time) (bool, error) {
	var wrote bool
	err := atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `UPDATE providers SET id = id WHERE id = ?`, ProviderGitHub); err != nil {
			return err
		}
		var cfg string
		if err := q.QueryRow(ctx, `SELECT config FROM providers WHERE id = ? AND kind = 'github'`, ProviderGitHub).Scan(&cfg); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		m, err := configMap(cfg)
		if err != nil {
			return err
		}
		if string(m[githubAppKey]) != old {
			return nil
		}
		m[githubAppKey] = json.RawMessage(next)
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE providers SET config = ?, updated = ? WHERE id = ?`, string(b), ms(now), ProviderGitHub); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

// DeleteGitHubProvider removes the GitHub row: its links (what people signed in
// with and what they connected), installations, shares and reminders go with it.
// A ProviderInUseError while a space syncs through it.
func DeleteGitHubProvider(ctx context.Context, q Q) error {
	return atomically(ctx, q, func(q Q) error {
		if err := lockUnusedProvider(ctx, q, ProviderGitHub); err != nil {
			return err
		}
		return deleteProviderRows(ctx, q, `id = ?`, ProviderGitHub)
	})
}

// ListProviderRepoLinks returns every repository link at a provider (to revoke
// the grants before the provider goes), by user id.
func ListProviderRepoLinks(ctx context.Context, q Q, providerID string) ([]RepoLink, error) {
	rows, err := q.Query(ctx, `SELECT `+repoLinkRead+` FROM linked_accounts WHERE provider_id = ? AND `+repoLinkSQL+` ORDER BY user_id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoLink
	for rows.Next() {
		a, err := scanRepoLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SpaceOwnership is the tenant of a space and whether it is someone's
// personal space: whose credentials it may sync with. ErrNotFound when it is gone.
func SpaceOwnership(ctx context.Context, q Q, id string) (tenantID string, personal bool, err error) {
	var p sql.NullString
	err = q.QueryRow(ctx, `SELECT tenant_id, personal_of FROM spaces WHERE id = ?`, id).Scan(&tenantID, &p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return tenantID, p.Valid, err
}
