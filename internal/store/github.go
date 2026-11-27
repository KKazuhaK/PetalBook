package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// GitHubSpaceCounts is how many spaces sync through each of the installations.
func GitHubSpaceCounts(ctx context.Context, q Q, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	rows, err := q.Query(ctx, `SELECT installation_id, COUNT(DISTINCT space_id) FROM remotes
		WHERE installation_id IN (`+strings.Join(marks, ",")+`) AND installation_id <> 0 AND url <> '' AND cred_type = 'connection' AND provider = 'github' AND via = 'app'
		GROUP BY installation_id`, args...)
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

// GitHubInstallation is an installation the administrator has looked at: its
// account, to list it without asking GitHub. Who it is shared with is in
// provider_shares.
type GitHubInstallation struct {
	ID          int64
	Account     string
	AccountType string
	Updated     time.Time
}

const ghInstCols = `i.id, i.account, i.account_type, i.updated`

func scanGitHubInstallation(row interface{ Scan(...any) error }) (*GitHubInstallation, error) {
	var i GitHubInstallation
	var up int64
	if err := row.Scan(&i.ID, &i.Account, &i.AccountType, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	i.Updated = fromMS(up)
	return &i, nil
}

// GetGitHubInstallation returns what is stored of an installation, or ErrNotFound.
func GetGitHubInstallation(ctx context.Context, q Q, id int64) (*GitHubInstallation, error) {
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM provider_installations i WHERE i.provider_id = ? AND i.id = ?`, ProviderGitHub, id))
}

// PutGitHubInstallation stores an installation. Its sharing is left as it is.
func PutGitHubInstallation(ctx context.Context, q Q, i GitHubInstallation) error {
	_, err := q.Exec(ctx, `INSERT INTO provider_installations(provider_id, id, account, account_type, updated) VALUES (?,?,?,?,?)
		ON CONFLICT (provider_id, id) DO UPDATE SET account = excluded.account, account_type = excluded.account_type, updated = excluded.updated`,
		ProviderGitHub, i.ID, i.Account, i.AccountType, ms(i.Updated))
	return err
}

// SetGitHubInstallationTenants replaces the set of tenants an installation is
// shared with (an empty set stops sharing). Callers run it in a transaction.
func SetGitHubInstallationTenants(ctx context.Context, q Q, id int64, tenants []string) error {
	if _, err := q.Exec(ctx, `DELETE FROM provider_shares WHERE provider_id = ? AND installation_id = ?`, ProviderGitHub, id); err != nil {
		return err
	}
	for _, t := range tenants {
		if _, err := q.Exec(ctx, `INSERT INTO provider_shares(provider_id, installation_id, tenant_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, ProviderGitHub, id, t); err != nil {
			return err
		}
	}
	return nil
}

// GitHubInstallationTenants says which tenants each of the installations is
// shared with (sorted); an installation shared with none is not in the map.
func GitHubInstallationTenants(ctx context.Context, q Q, ids []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{ProviderGitHub}
	marks := make([]string, len(ids))
	for i, id := range ids {
		args, marks[i] = append(args, id), "?"
	}
	rows, err := q.Query(ctx, `SELECT installation_id, tenant_id FROM provider_shares
		WHERE provider_id = ? AND installation_id IN (`+strings.Join(marks, ",")+`) ORDER BY installation_id, tenant_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// sharedWith is the condition that installation column col is shared with a
// tenant in the scope: an installation shared with another tenant, or with
// none, is never one the scope's tenant can use.
func sharedWith(sc Scope, col string) (string, []any) {
	c, a := sc.cond("tenant_id")
	return col + ` IN (SELECT installation_id FROM provider_shares WHERE provider_id = ? AND installation_id <> 0 AND ` + c + `)`, append([]any{ProviderGitHub}, a...)
}

// GitHubInstallationShared says whether an installation is shared with the
// tenant of the scope.
func GitHubInstallationShared(ctx context.Context, q Q, sc Scope, id int64) (bool, error) {
	c, a := sharedWith(sc, "i.id")
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM provider_installations i WHERE i.provider_id = ? AND i.id = ? AND `+c, append([]any{ProviderGitHub, id}, a...)...).Scan(&n)
	return n > 0, err
}

// ListGitHubInstallationsOf lists the installations shared with the tenant of
// the scope, with an id above after, in id order, at most limit.
func ListGitHubInstallationsOf(ctx context.Context, q Q, sc Scope, after int64, limit int) ([]GitHubInstallation, error) {
	c, a := sharedWith(sc, "i.id")
	rows, err := q.Query(ctx, `SELECT `+ghInstCols+` FROM provider_installations i WHERE i.provider_id = ? AND i.id > ? AND `+c+` ORDER BY i.id LIMIT ?`,
		append(append([]any{ProviderGitHub, after}, a...), limit)...)
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

// GitHubInstallationOf returns the installation on an account (by login, case
// ignored) shared with the tenant of the scope, or ErrNotFound.
func GitHubInstallationOf(ctx context.Context, q Q, sc Scope, login string) (*GitHubInstallation, error) {
	c, a := sharedWith(sc, "i.id")
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM provider_installations i
		WHERE i.provider_id = ? AND lower(i.account) = ? AND `+c+` ORDER BY i.id LIMIT 1`, append([]any{ProviderGitHub, strings.ToLower(login)}, a...)...))
}

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
func DeleteGitHubProvider(ctx context.Context, q Q) error {
	return deleteProviderRows(ctx, q, `id = ?`, ProviderGitHub)
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
