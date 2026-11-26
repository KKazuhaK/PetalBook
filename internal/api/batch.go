package api

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Finding people to add, and adding many at once (docs/api.md, "Adding people
// in batches").

const (
	// batchBody is the most a batch may weigh: 1000 rows with a name and a few groups.
	batchBody = 1 << 20
	// peopleWindow and peoplePerWindow are the rate of the people search, per person.
	peopleWindow    = 10 * time.Second
	peoplePerWindow = 20
)

// peopleGate counts searches per person in a sliding window. It holds the
// recent times of those who searched lately, and drops the rest as it goes.
type peopleGate struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

// take spends one search of id's allowance; false: wait (the time given).
func (g *peopleGate) take(id string, now time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil {
		g.seen = map[string][]time.Time{}
	}
	if len(g.seen) > 10_000 {
		for k, l := range g.seen {
			if now.Sub(l[len(l)-1]) >= peopleWindow {
				delete(g.seen, k)
			}
		}
	}
	l := g.seen[id]
	cut := 0
	for cut < len(l) && now.Sub(l[cut]) >= peopleWindow {
		cut++
	}
	l = l[cut:]
	if len(l) >= peoplePerWindow {
		g.seen[id] = l
		return l[0].Add(peopleWindow).Sub(now), false
	}
	g.seen[id] = append(l, now)
	return 0, true
}

// findPeople is the picker's search: the enabled people of the caller's own
// tenant whose username starts with, or whose name holds, the query. It gives
// the id, username and name of at most 20, and nothing else (no address).
func (s *server) findPeople(c *call) {
	q := strings.TrimSpace(c.r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(q); n < 2 || n > 80 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Q is 2 to 80 characters.")
		return
	}
	limit, ok := listLimit(c, 20, 20)
	if !ok {
		return
	}
	if s.single {
		writeError(c.w, http.StatusNotFound, "not_found", "There are no other people.")
		return
	}
	if wait, ok := s.peopleGate.take(c.user().ID, s.now()); !ok {
		c.w.Header().Set("Retry-After", retrySeconds(wait))
		writeError(c.w, http.StatusTooManyRequests, "rate_limited", "Searching too often; wait a moment.")
		return
	}
	l, err := store.SearchPeople(c.r.Context(), s.opt.Store.Q(), c.ownScope(), q, limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type item struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	out := make([]item, len(l))
	for i, p := range l {
		out[i] = item{p.ID, p.Username, p.Name}
	}
	writeJSON(c.w, http.StatusOK, struct {
		People []item `json:"people"`
	}{out})
}

func retrySeconds(d time.Duration) string {
	n := int((d + time.Second - 1) / time.Second)
	if n < 1 {
		n = 1
	}
	return itoa(n)
}

// batchRowResp is what became of one row.
type batchRowResp struct {
	Row      int    `json:"row"`
	User     string `json:"user"`
	Role     string `json:"role,omitempty"`
	Status   string `json:"status"`
	UserID   string `json:"userId,omitempty"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// Mail and Token are for invitations of a tenant batch: how the invitation
	// went ("sent", "none", "limited") and the token to hand over when it did
	// not go by mail (shown once, in this answer only).
	Mail  string `json:"mail,omitempty"`
	Token string `json:"token,omitempty"`
}

func summarize(rows []batchRowResp) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Status]++
	}
	return out
}

// batchDetail is the audit detail of a batch: the counts, and one entry per
// row (its number, who, and what became of it).
func batchDetail(base map[string]any, rows []batchRowResp) map[string]any {
	type entry struct {
		Row    int    `json:"row"`
		User   string `json:"user"`
		Status string `json:"status"`
		Role   string `json:"role,omitempty"`
	}
	per := make([]entry, len(rows))
	for i, r := range rows {
		per[i] = entry{r.Row, r.User, r.Status, r.Role}
	}
	base["rows"] = len(rows)
	base["counts"] = summarize(rows)
	base["results"] = per
	return base
}

func fromSpaceResults(l []spaces.BatchResult) []batchRowResp {
	out := make([]batchRowResp, len(l))
	for i, r := range l {
		out[i] = batchRowResp{Row: r.Row, User: r.Key, Role: r.Role, Status: r.Status, UserID: r.UserID, Username: r.Username, Name: r.Name}
	}
	return out
}

type batchReq struct {
	DryRun bool `json:"dryRun"`
	Rows   []struct {
		User   string   `json:"user"`
		Role   string   `json:"role"`
		Name   string   `json:"name"`
		Groups []string `json:"groups"`
	} `json:"rows"`
	// Tenant is the tenant a batch of new people is for (default: the caller's).
	Tenant string `json:"tenant"`
}

func (s *server) readBatch(c *call) (batchReq, bool) {
	var req batchReq
	if !s.readJSONMax(c.w, c.r, &req, false, batchBody) {
		return req, false
	}
	if n := len(req.Rows); n < 1 || n > spaces.MaxBatchRows {
		writeError(c.w, http.StatusBadRequest, "invalid", "Rows holds 1 to "+itoa(spaces.MaxBatchRows)+" rows.")
		return req, false
	}
	return req, true
}

func writeBatch(c *call, dry bool, rows []batchRowResp, extra map[string]any) {
	out := map[string]any{"dryRun": dry, "results": rows, "summary": summarize(rows)}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(c.w, http.StatusOK, out)
}

// addMembersBatch adds people to a space by username or address.
func (s *server) addMembersBatch(c *call) {
	req, ok := s.readBatch(c)
	if !ok {
		return
	}
	rows := make([]spaces.BatchRow, len(req.Rows))
	for i, r := range req.Rows {
		rows[i] = spaces.BatchRow{Key: r.User, Role: r.Role}
	}
	res, err := s.spaces.AddMembersBatch(c.r.Context(), c.sp, c.user().ID, rows, req.DryRun)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := fromSpaceResults(res)
	if !req.DryRun {
		now := s.now()
		for _, r := range out {
			if r.Status != spaces.BatchOK {
				continue
			}
			// Told they were added, as with a single add; a failure is only logged.
			err := store.NotifyUser(c.r.Context(), s.opt.Store.Q(), r.UserID, store.NotifyInvite, c.sp.Info.ID,
				map[string]string{"space": c.sp.Info.Name, "role": r.Role, "by": c.user().Name}, now)
			if err != nil {
				s.log.Warn("could not write a notification", "error", err)
			}
		}
		tenant := ""
		if t, err := store.SpaceTenants(c.r.Context(), s.opt.Store.Q(), []string{c.sp.Info.ID}); err == nil {
			tenant = t[c.sp.Info.ID]
		}
		s.audit(c, auth.AuditEvent{Kind: "members_batch", Actor: c.user().ID, Tenant: tenant,
			Detail: batchDetail(map[string]any{"space": c.sp.Info.ID, "name": c.sp.Info.Name}, out)})
	}
	writeBatch(c, req.DryRun, out, nil)
}

// adminAddGroupMembersBatch adds people to a local group by username or address.
func (s *server) adminAddGroupMembersBatch(c *call) {
	req, ok := s.readBatch(c)
	if !ok {
		return
	}
	keys := make([]string, len(req.Rows))
	for i, r := range req.Rows {
		keys[i] = r.User
	}
	id := c.r.PathValue("group")
	res, tenant, err := s.spaces.AddGroupPeopleBatch(c.r.Context(), store.ScopeOf(c.user()), id, c.user().ID, keys, req.DryRun)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	out := fromSpaceResults(res)
	if !req.DryRun {
		s.auditGroup(c, tenant, "group_members_batch", "", batchDetail(map[string]any{"group": id}, out))
	}
	writeBatch(c, req.DryRun, out, nil)
}

// adminAddMembersBatch adds people to the tenant: a person who has an account
// there is put into the groups the row names, a new address is invited.
func (s *server) adminAddMembersBatch(c *call) {
	req, ok := s.readBatch(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	tenant := req.Tenant
	if tenant == "" {
		tenant = c.user().TenantID
	}
	keys := make([]string, len(req.Rows))
	var groupNames []string
	for i, r := range req.Rows {
		keys[i] = r.User
		groupNames = append(groupNames, r.Groups...)
	}
	// What the caller may do in the tenant is decided before anything is read of it.
	if _, _, err := s.auth.InviteMany(ctx, c.user(), tenant, nil, true); err != nil {
		s.fail(c.w, err)
		return
	}
	res, err := spaces.ResolveKeys(ctx, s.opt.Store.Q(), tenant, keys)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	groups, err := store.LocalGroupsByName(ctx, s.opt.Store.Q(), store.InTenant(tenant), groupNames)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]batchRowResp, len(req.Rows))
	var specs []auth.InviteSpec
	var specRow []int
	addTo := map[string][]string{} // group id -> people
	newMail := map[string]bool{}   // the addresses invited so far in this batch
	for i, r := range req.Rows {
		o := batchRowResp{Row: i + 1, User: strings.TrimSpace(r.User), Role: r.Role, Status: res[i].Status}
		var gids []string
		for _, name := range r.Groups {
			g, ok := groups[store.FoldName(strings.TrimSpace(name))]
			if !ok && strings.TrimSpace(name) != "" {
				o.Status, o.Detail = spaces.BatchInvalid, "No group is called "+strings.TrimSpace(name)+"."
				break
			}
			if ok {
				gids = append(gids, g)
			}
		}
		switch {
		case o.Status == spaces.BatchInvalid && o.Detail != "":
		case res[i].Hit != nil:
			h := res[i].Hit
			o.UserID, o.Username, o.Name = h.ID, h.Username, h.Name
			switch {
			case len(gids) == 0:
				o.Status = spaces.BatchAlready
			case h.ID == c.user().ID:
				o.Status, o.Detail = spaces.BatchForbidden, "Join a group yourself from the group's page."
			default:
				o.Status = "added"
				for _, g := range gids {
					addTo[g] = append(addTo[g], h.ID)
				}
			}
		case o.Status == spaces.BatchNotFound && strings.Contains(o.User, "@"):
			if k, ok := store.EmailKey(o.User); ok && newMail[k] {
				o.Status = spaces.BatchDuplicate
				break
			} else if ok {
				newMail[k] = true
			}
			o.Status = "invited"
			specs, specRow = append(specs, auth.InviteSpec{Email: o.User, Name: r.Name, Role: r.Role, Groups: gids}), append(specRow, i)
		case o.Status == spaces.BatchNotFound:
			o.Status, o.Detail = spaces.BatchInvalid, "Nobody has this username; to invite someone, give an email address."
		}
		out[i] = o
	}
	made, errs, err := s.auth.InviteMany(ctx, c.user(), tenant, specs, req.DryRun)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	sender := s.auth.MailSender(tenant)
	for j, i := range specRow {
		if errs[j] != nil {
			out[i].Status, out[i].Detail = spaces.BatchInvalid, errs[j].Error()
			continue
		}
		if req.DryRun {
			out[i].Mail = auth.MailNone
			if sender != "" {
				out[i].Mail = auth.MailSent
			}
			continue
		}
		out[i].Mail = made[j].Mail
		if made[j].Mail != auth.MailSent {
			out[i].Token = made[j].Token
		}
	}
	if !req.DryRun {
		for g, all := range addTo {
			for start := 0; start < len(all); start += spaces.MaxAddPeople {
				ids := all[start:min(start+spaces.MaxAddPeople, len(all))]
				if _, _, _, _, err := s.spaces.AddGroupPeople(ctx, store.ScopeOf(c.user()), g, ids, spaces.GroupJoin{By: c.user().ID}); err != nil {
					for i := range out {
						if out[i].Status == "added" && slices.Contains(ids, out[i].UserID) {
							out[i].Status, out[i].Detail = "failed", err.Error()
						}
					}
				}
			}
		}
		s.audit(c, auth.AuditEvent{Kind: "invites_batch", Actor: c.user().ID, Tenant: tenant,
			Detail: batchDetail(map[string]any{"tenant": tenant, "sender": sender}, out)})
	}
	writeBatch(c, req.DryRun, out, map[string]any{"mail": map[string]any{"configured": sender != "", "sender": sender}})
}
