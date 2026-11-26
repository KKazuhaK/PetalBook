package spaces

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"petalbook.app/server/internal/store"
)

// Batches of people by file (docs/api.md, "Adding people in batches"): a row
// names a person by username or address, in the tenant of what they are added
// to. A batch is checked row by row first (dryRun) and applied by the same
// call without it; every row gets its own result, a refused row never stops
// the others.

// MaxBatchRows is the most rows a batch holds.
const MaxBatchRows = 1000

// batchChunk is how many rows one transaction takes: the unit of the cost.
const batchChunk = 200

// What a row's result says. Only BatchOK rows change anything.
const (
	BatchOK          = "ok"
	BatchNotFound    = "not_found"
	BatchOtherTenant = "other_tenant"
	BatchAlready     = "already_member"
	BatchInvalid     = "invalid"
	BatchDuplicate   = "duplicate"
	BatchForbidden   = "forbidden"
	// BatchDisabled: a disabled account of the tenant, which nobody may add and
	// whose address is never invited (it still holds it).
	BatchDisabled = "disabled"
)

// hideOutside is what a batch that is not the tenant administrator's tells of a
// row: a person of another tenant, and a disabled one of the tenant, are
// "not_found", as the single add tells them, so that a space owner cannot
// learn which usernames and addresses the site has.
func hideOutside(status string) string {
	if status == BatchOtherTenant || status == BatchDisabled {
		return BatchNotFound
	}
	return status
}

// BatchRow is one line of a batch: who (a username or an address) and in which role.
type BatchRow struct {
	Key  string
	Role string
}

// BatchResult is what became of one row (Row counts from 1).
type BatchResult struct {
	Row      int
	Key      string
	Role     string
	Status   string
	UserID   string
	Username string
	Name     string
}

// Resolved is a row's key looked up: Hit is set for a person of the tenant;
// Status is BatchOK for one, otherwise why not.
type Resolved struct {
	Hit    *store.PersonHit
	Status string
}

// ResolveKeys looks the keys up (a key with an @ is an address, any other a
// username) among the people of tenant: one statement per 200 keys of each
// kind. A person of another tenant is "other_tenant", a disabled one is
// "not_found" (nobody can be added who cannot sign in), a key that is
// neither a username nor an address is "invalid". A key that repeats an
// earlier one that named the same person is "duplicate".
func ResolveKeys(ctx context.Context, q store.Q, tenant string, keys []string) ([]Resolved, error) {
	out := make([]Resolved, len(keys))
	var names, emails []string
	norm := make([]string, len(keys))
	isMail := make([]bool, len(keys))
	for i, k := range keys {
		k = strings.TrimSpace(k)
		switch {
		case k == "" || len(k) > 254:
			out[i].Status = BatchInvalid
		case strings.Contains(k, "@"):
			key, ok := store.EmailKey(k)
			if !ok {
				out[i].Status = BatchInvalid
				continue
			}
			norm[i], isMail[i] = key, true
			emails = append(emails, key)
		default:
			norm[i] = asciiLower(k)
			names = append(names, norm[i])
		}
	}
	byName, byMail, err := store.FindPeople(ctx, q, names, emails)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range keys {
		if out[i].Status != "" {
			continue
		}
		var h store.PersonHit
		var ok bool
		if isMail[i] {
			h, ok = byMail[norm[i]]
		} else {
			h, ok = byName[norm[i]]
		}
		switch {
		case !ok:
			out[i].Status = BatchNotFound
		case h.TenantID != tenant:
			out[i].Status = BatchOtherTenant
		case h.Disabled:
			out[i].Status = BatchDisabled
		case seen[h.ID]:
			out[i].Status = BatchDuplicate
		default:
			seen[h.ID] = true
			out[i].Hit, out[i].Status = &h, BatchOK
		}
	}
	return out, nil
}

// AddMembersBatch adds the people the rows name to the space. With dry it
// only checks: the results are the ones the same call would give, and
// nothing is written. It costs one lookup per 200 rows for the people, one
// for who is a member already, and one insert by key per added person, in
// one transaction per 200 rows (under the space lock like a single add).
// Another tenant's person and a disabled one are "not_found" (hideOutside).
func (m *Manager) AddMembersBatch(ctx context.Context, h *Handle, actorID string, rows []BatchRow, dry bool) ([]BatchResult, error) {
	if err := m.needMembers(h); err != nil {
		return nil, err
	}
	if h.Info.Personal {
		return nil, ErrPersonal
	}
	if len(rows) < 1 || len(rows) > MaxBatchRows {
		return nil, fmt.Errorf("%w: a batch holds 1 to %d rows", ErrInvalid, MaxBatchRows)
	}
	tenants, err := store.SpaceTenants(ctx, m.opt.Store.Q(), []string{h.Info.ID})
	if err != nil {
		return nil, err
	}
	tenant, ok := tenants[h.Info.ID]
	if !ok {
		return nil, ErrNotFound
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = r.Key
	}
	res, err := ResolveKeys(ctx, m.opt.Store.Q(), tenant, keys)
	if err != nil {
		return nil, err
	}
	out := make([]BatchResult, len(rows))
	var todo []int
	for i, r := range rows {
		out[i] = BatchResult{Row: i + 1, Key: strings.TrimSpace(r.Key), Role: r.Role, Status: hideOutside(res[i].Status)}
		if hit := res[i].Hit; hit != nil {
			out[i].UserID, out[i].Username, out[i].Name = hit.ID, hit.Username, hit.Name
			switch {
			case checkRole(r.Role) != nil:
				out[i].Status = BatchInvalid
			case viaAdminOwnership(h, hit.ID == actorID, r.Role) != nil:
				out[i].Status = BatchForbidden
			default:
				todo = append(todo, i)
			}
		}
	}
	for start := 0; start < len(todo); start += batchChunk {
		chunk := todo[start:min(start+batchChunk, len(todo))]
		ids := make([]string, len(chunk))
		for j, i := range chunk {
			ids[j] = out[i].UserID
		}
		var added []string
		var before, after store.Touched
		apply := func(q store.Q) error {
			if !dry {
				if err := store.LockSpace(ctx, q, h.Info.ID); err != nil {
					return err
				}
			}
			in, err := store.SpaceMembersAmong(ctx, q, h.Info.ID, ids)
			if err != nil {
				return err
			}
			if !dry {
				if before, err = store.SpaceCountedFor(ctx, q, h.Info.ID); err != nil {
					return err
				}
			}
			now := m.opt.Now()
			for _, i := range chunk {
				if in[out[i].UserID] {
					out[i].Status = BatchAlready
					continue
				}
				if dry {
					continue
				}
				switch err := store.AddMember(ctx, q, h.Info.ID, out[i].UserID, out[i].Role, now); {
				case err == nil:
					added = append(added, out[i].UserID)
				case errors.Is(err, store.ErrNotFound):
					out[i].Status = BatchNotFound
				default:
					return err
				}
			}
			if dry {
				return nil
			}
			after, err = store.SpaceCountedFor(ctx, q, h.Info.ID)
			return err
		}
		if dry {
			err = apply(m.opt.Store.Q())
		} else {
			err = m.opt.Store.Tx(ctx, apply)
		}
		if err != nil {
			return nil, err
		}
		if len(added) > 0 {
			m.MembershipChanged(added...)
			m.usageMoved(ctx, before, after)
		}
	}
	return out, nil
}

// AddGroupPeopleBatch adds the people the keys name to a local group, with
// the rules of AddGroupPeople (at most 500 per call, so a batch is two). The
// caller's own row is "forbidden": joining a group is a takeover of what it
// reaches when it gives more, which the single add asks a reason for.
// Another tenant's person and a disabled one are "not_found" (hideOutside).
// It returns the group's tenant for the audit row.
func (m *Manager) AddGroupPeopleBatch(ctx context.Context, sc store.Scope, id, actorID string, keys []string, dry bool) ([]BatchResult, string, error) {
	if err := m.needAccounts(); err != nil {
		return nil, "", err
	}
	if len(keys) < 1 || len(keys) > MaxBatchRows {
		return nil, "", fmt.Errorf("%w: a batch holds 1 to %d rows", ErrInvalid, MaxBatchRows)
	}
	g, err := store.GetGroup(ctx, m.opt.Store.Q(), sc, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", ErrGroupNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if g.Source != store.GroupLocal {
		return nil, "", ErrGroupSynced
	}
	res, err := ResolveKeys(ctx, m.opt.Store.Q(), g.TenantID, keys)
	if err != nil {
		return nil, "", err
	}
	out := make([]BatchResult, len(keys))
	var ids []string
	for i, k := range keys {
		out[i] = BatchResult{Row: i + 1, Key: strings.TrimSpace(k), Status: hideOutside(res[i].Status)}
		if hit := res[i].Hit; hit != nil {
			out[i].UserID, out[i].Username, out[i].Name = hit.ID, hit.Username, hit.Name
			if hit.ID == actorID {
				out[i].Status = BatchForbidden
				continue
			}
			ids = append(ids, hit.ID)
		}
	}
	fresh := map[string]bool{}
	for start := 0; start < len(ids); start += MaxAddPeople {
		chunk := ids[start:min(start+MaxAddPeople, len(ids))]
		var added []string
		if dry {
			in, _, err := store.LocalMembers(ctx, m.opt.Store.Q(), id, chunk)
			if err != nil {
				return nil, "", err
			}
			for _, u := range chunk {
				if !in[u] {
					added = append(added, u)
				}
			}
		} else if _, _, added, _, err = m.AddGroupPeople(ctx, sc, id, chunk, GroupJoin{By: actorID}); err != nil {
			return nil, "", err
		}
		for _, u := range added {
			fresh[u] = true
		}
	}
	for i := range out {
		if out[i].Status == BatchOK && !fresh[out[i].UserID] {
			out[i].Status = BatchAlready
		}
	}
	return out, g.TenantID, nil
}
