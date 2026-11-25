package store

// What a space is and where it lives, kept with the space (milestone 3.5, part 4;
// docs/api.md, "Storage management"), and what a person is, kept with their usage
// row. Every list and the summary of the storage administration read these
// columns; nothing on a request walks a disk, a repository or a table.

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// Where a space's bytes live (spaces.location). The quotas count what is stored
// with us and nothing else (docs/design/milestones/phase-3-storage.md, "One item 3.1 left open, to be settled here"):
//
//   - LocationServer: only on this server (a space without a remote). Counted
//     in full.
//   - LocationOwn: the remote belongs to the person (a repository of their own,
//     reached with their token, key or connection). Shown, never counted; the
//     clone and index the server keeps for it are the pressure controller's.
//   - LocationTenant: the remote is the tenant's own storage target (3.6).
//     Shown, never counted.
//   - LocationSite: the repository is in the site's storage organisation (3.6).
//     Counted, from the size the provider's API reports (never by downloading).
//
// 3.6 adds the last two by changing locationOf and sizing the "site" spaces from
// the provider; nothing else here needs to learn about them.
const (
	LocationServer = "server"
	LocationOwn    = "own"
	LocationTenant = "tenant"
	LocationSite   = "site"
)

// LocationCounts reports whether a space at this location counts toward quotas.
func LocationCounts(location string) bool {
	return location == LocationServer || location == LocationSite
}

// locationOf is where a space lives, given where its remote goes through ("" for
// no remote; see remoteViaSQL). This is the seam of 3.6: the storage mode of
// the space or its tenant, and the site's storage organisation, decide here.
func locationOf(remoteVia string) string {
	if remoteVia == "" {
		return LocationServer
	}
	return LocationOwn
}

// remoteViaSQL is how a space follows its remote: "" when it follows none, the
// provider of a connection, or "git" for a plain address. It is the SQL of
// spaces.remote_via, with the space's id column at id.
func remoteViaSQL(id string) string {
	return `COALESCE((SELECT CASE WHEN r.cred_type = 'connection' THEN r.provider ELSE 'git' END FROM remotes r WHERE r.space_id = ` + id + ` AND r.url <> ''), '')`
}

func remoteStateSQL(id string) string {
	return `COALESCE((SELECT r.state FROM remotes r WHERE r.space_id = ` + id + ` AND r.url <> ''), '')`
}

// settleDerived sets the columns of a space that follow its remote (remote_via,
// remote_state, location) from the remotes table, and, when the space started or
// stopped counting, its counted bytes: what the last measurement found for a
// space that counts, nothing for one that does not. It writes only what changed.
func settleDerived(ctx context.Context, q Q, id string) error {
	var oldLoc, oldVia, oldState, via, state string
	var oldBytes int64
	var size sql.NullInt64
	err := q.QueryRow(ctx, `SELECT location, remote_via, remote_state, usage_bytes, size_bytes, `+remoteViaSQL("spaces.id")+`, `+remoteStateSQL("spaces.id")+` FROM spaces WHERE id = ?`, id).
		Scan(&oldLoc, &oldVia, &oldState, &oldBytes, &size, &via, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	loc, bytes := locationOf(via), oldBytes
	if LocationCounts(loc) != LocationCounts(oldLoc) {
		bytes = 0
		if LocationCounts(loc) {
			bytes = size.Int64
		}
	}
	if loc == oldLoc && via == oldVia && state == oldState && bytes == oldBytes {
		return nil
	}
	_, err = q.Exec(ctx, `UPDATE spaces SET location = ?, remote_via = ?, remote_state = ?, usage_bytes = ? WHERE id = ?`, loc, via, state, bytes, id)
	return err
}

// --- what a person is -----------------------------------------------------------

// activeEvery is the least time between two writes of a person's last_active.
const activeEvery = time.Hour

// maxNoted bounds the memory of who was written recently.
const maxNoted = 200_000

type noted struct {
	mu     sync.Mutex
	active map[string]int64 // person -> the last_active written (ms)
	wrote  map[string]bool  // people whose first_write is known to be set
	busy   map[string]bool  // people whose first_write is being written
}

// NoteActive records that a session or a token of the person was used: at most
// one write an hour for each person (the time of the last write is held in
// memory, and a write is skipped while it is recent). The usage row carries a
// copy, so the list of people is one index.
func (s *Store) NoteActive(ctx context.Context, userID string, at time.Time) error {
	s.people.mu.Lock()
	if last, ok := s.people.active[userID]; ok && at.UnixMilli()-last < activeEvery.Milliseconds() {
		s.people.mu.Unlock()
		return nil
	}
	if s.people.active == nil || len(s.people.active) >= maxNoted {
		s.people.active = map[string]int64{}
	}
	s.people.active[userID] = at.UnixMilli()
	s.people.mu.Unlock()
	return s.Tx(ctx, func(q Q) error {
		res, err := q.Exec(ctx, `UPDATE users SET last_active = ? WHERE id = ? AND deleted_at IS NULL AND (last_active IS NULL OR last_active < ?)`, ms(at), userID, ms(at)-activeEvery.Milliseconds())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = q.Exec(ctx, `UPDATE usage SET last_active = ? WHERE kind = 'user' AND id = ?`, ms(at), userID)
		return err
	})
}

// NoteWrite records that the person changed something in a space. The first time
// sets users.first_write and works out whether they are still dormant; after
// that it is a lookup in memory.
func (s *Store) NoteWrite(ctx context.Context, userID string, at time.Time) error {
	s.people.mu.Lock()
	if s.people.wrote[userID] || s.people.busy[userID] {
		s.people.mu.Unlock()
		return nil
	}
	if s.people.busy == nil {
		s.people.busy = map[string]bool{}
	}
	s.people.busy[userID] = true
	s.people.mu.Unlock()
	err := s.Tx(ctx, func(q Q) error {
		var tenant string
		res, err := q.Exec(ctx, `UPDATE users SET first_write = ? WHERE id = ? AND first_write IS NULL AND deleted_at IS NULL`, ms(at), userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if err := q.QueryRow(ctx, `SELECT tenant_id FROM users WHERE id = ? AND deleted_at IS NULL`, userID).Scan(&tenant); err != nil {
			return err
		}
		return addUsage(ctx, q, usageDelta{kind: UsageUser, id: userID, tenant: tenant, refresh: true})
	})
	s.people.mu.Lock()
	delete(s.people.busy, userID)
	s.people.mu.Unlock()
	if err != nil {
		return err
	}
	s.people.mu.Lock()
	if s.people.wrote == nil || len(s.people.wrote) >= maxNoted {
		s.people.wrote = map[string]bool{}
	}
	s.people.wrote[userID] = true
	s.people.mu.Unlock()
	return nil
}

// hasConnectionSQL is the condition that a person has a connection: a GitHub
// authorization or one of any provider of 2.7b. id is the SQL of their id.
func hasConnectionSQL(id string) string {
	return `(EXISTS (SELECT 1 FROM github_authorizations ga WHERE ga.user_id = ` + id + `) OR EXISTS (SELECT 1 FROM provider_authorizations pa WHERE pa.user_id = ` + id + `))`
}

// settleConnected makes the person's usage row say whether they have a
// connection (a GitHub authorization, any provider of 2.7b), and the tenant's
// count with it. Call it in the transaction that adds or removes a connection.
//
// Two such transactions for one person (a GitHub disconnect beside another
// provider's connect) must not each decide from a view without the other's
// change. On PostgreSQL the usage row is locked first, in a statement of its own,
// so the look at the connections below takes its snapshot only once any other
// transaction that held the row has committed. SQLite has one writer at a time.
func settleConnected(ctx context.Context, q Q, userID string) error {
	if isPostgres(q) {
		var one int
		err := q.QueryRow(ctx, `SELECT 1 FROM usage WHERE kind = 'user' AND id = ? FOR UPDATE`, userID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	var tenant string
	var have, want int64
	err := q.QueryRow(ctx, `SELECT u.tenant_id, u.connected, CASE WHEN `+hasConnectionSQL("u.id")+` THEN 1 ELSE 0 END
		FROM usage u WHERE u.kind = 'user' AND u.id = ?`, userID).Scan(&tenant, &have, &want)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && have == want) {
		return nil
	}
	if err != nil {
		return err
	}
	return addUsage(ctx, q,
		usageDelta{kind: UsageUser, id: userID, tenant: tenant, connected: want - have},
		usageDelta{kind: UsageTenant, id: tenant, tenant: tenant, connected: want - have})
}

// personFlags are what a person's usage row says of them that a tenant's row
// sums: whether they are connected and whether they are dormant. A person who
// leaves the tenant's people (a deletion) takes them out of the tenant's row;
// one who comes back puts them in.
func personFlags(ctx context.Context, q Q, userID string) (connected, dormant int64, err error) {
	err = q.QueryRow(ctx, `SELECT connected, dormant FROM usage WHERE kind = 'user' AND id = ?`, userID).Scan(&connected, &dormant)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return
}

// settleRestored is settleDerived for a space just put back, whose row came from
// the recycle bin's copy: the tenant's row already holds what the copy said, and
// takes the difference if the remote row did not come back as it was.
func settleRestored(ctx context.Context, q Q, id string) error {
	before, err := readSpaceUsage(ctx, q, id)
	if err != nil {
		return err
	}
	if err := settleDerived(ctx, q, id); err != nil {
		return err
	}
	after, err := readSpaceUsage(ctx, q, id)
	if err != nil {
		return err
	}
	return addUsage(ctx, q, after.changed(before)...)
}

// locationSQL is locationOf for the SQL of the daily recount: where a space
// lives, given the SQL of its remote_via. Keep the two in step.
func locationSQL(via string) string {
	return `(CASE WHEN ` + via + ` = '' THEN '` + LocationServer + `' ELSE '` + LocationOwn + `' END)`
}

// settleSpaceFacts puts the columns that follow the remote right for every
// space that has them wrong (each is one statement over the spaces that differ),
// and the counted bytes of the spaces that do not count to nothing. It reports
// how many spaces it changed. It is the first step of the daily recount.
func settleSpaceFacts(ctx context.Context, q Q) (int, error) {
	via, state := remoteViaSQL("spaces.id"), remoteStateSQL("spaces.id")
	// A space that goes back to where its bytes count (server) counts what it was
	// last measured at, as the change of its remote does; the SET list reads the
	// columns as they were.
	res, err := q.Exec(ctx, `UPDATE spaces SET remote_via = `+via+`, remote_state = `+state+`, location = `+locationSQL(via)+`,
		usage_bytes = CASE WHEN `+locationSQL(via)+` = '`+LocationServer+`' AND location NOT IN ('`+LocationServer+`', '`+LocationSite+`') THEN COALESCE(size_bytes, 0) ELSE usage_bytes END
		WHERE remote_via <> `+via+` OR remote_state <> `+state+` OR location <> `+locationSQL(via))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	res, err = q.Exec(ctx, `UPDATE spaces SET usage_bytes = 0 WHERE usage_bytes <> 0 AND location NOT IN ('`+LocationServer+`', '`+LocationSite+`')`)
	if err != nil {
		return 0, err
	}
	m, _ := res.RowsAffected()
	return int(n + m), nil
}
