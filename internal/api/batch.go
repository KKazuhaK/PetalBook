package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Finding people to add, and adding many at once (docs/api.md, "Adding people
// in batches").

const (
	// batchBody is the most a batch may weigh: 1000 rows with a name and a few groups.
	batchBody = 1 << 20
	// peopleWindow and peoplePerWindow are the rate of the people search, per
	// person, in a fixed window from the person's first search.
	peopleWindow    = 10 * time.Second
	peoplePerWindow = 20
	// batchWindow and batchRowsPerWindow are the rows of batches (dry runs
	// too, all three kinds together) one person may send: a file of 1,000
	// checked and applied a few times over, never a sweep of the site.
	batchWindow        = 10 * time.Minute
	batchRowsPerWindow = 5000
)

// maxGateKeys bounds each gate: one entry per person who spent in the window.
const maxGateKeys = 10_000

// findPeople is the picker's search: the enabled people of a tenant whose
// username starts with, or whose name holds, the query. It gives the id,
// username and name of at most 20, and nothing else (no address). The tenant
// is the caller's own, which any member may search (the people they could
// share a space with); with ?group= it is that group's, for who manages it.
func (s *server) findPeople(c *call) {
	sc := c.ownScope()
	if id := c.r.URL.Query().Get("group"); id != "" && !s.single {
		g, err := store.GetGroup(c.r.Context(), s.opt.Store.Q(), store.AdminScopeOf(c.user()), id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !c.p.Access.Can(authz.TenantGroupsManage, g.TenantID)) {
			writeError(c.w, http.StatusNotFound, "not_found", "No such group.")
			return
		}
		if err != nil {
			s.fail(c.w, err)
			return
		}
		sc = store.InTenant(g.TenantID)
	}
	s.searchPeople(c, sc)
}

// findSpacePeople is the search of a space's dialog of members: the people of
// the space's tenant, for who may manage its members (a site administrator
// managing another tenant's space finds that tenant's people).
func (s *server) findSpacePeople(c *call) {
	t, err := store.SpaceTenants(c.r.Context(), s.opt.Store.Q(), []string{c.sp.Info.ID})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	tenant, ok := t[c.sp.Info.ID]
	if !ok {
		writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
		return
	}
	s.searchPeople(c, store.InTenant(tenant))
}

func (s *server) searchPeople(c *call, sc store.Scope) {
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
	if wait, ok, _ := s.peopleGate.Take(c.user().ID, 1, peoplePerWindow); !ok {
		c.w.Header().Set("Retry-After", retrySeconds(wait))
		writeError(c.w, http.StatusTooManyRequests, "rate_limited", "Searching too often; wait a moment.")
		return
	}
	l, err := store.SearchPeople(c.r.Context(), s.opt.Store.Q(), sc, q, limit)
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
	if wait, ok, _ := s.batchGate.Take(c.user().ID, len(req.Rows), batchRowsPerWindow); !ok {
		c.w.Header().Set("Retry-After", retrySeconds(wait))
		writeError(c.w, http.StatusTooManyRequests, "rate_limited", "Too many rows in a short time; wait a moment.")
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
	}
	// A dry run is audited too: it looks people up as the applied batch does.
	tenant := ""
	if t, err := store.SpaceTenants(c.r.Context(), s.opt.Store.Q(), []string{c.sp.Info.ID}); err == nil {
		tenant = t[c.sp.Info.ID]
	}
	s.audit(c, auth.AuditEvent{Kind: "members_batch", Actor: c.user().ID, Tenant: tenant,
		Detail: batchDetail(map[string]any{"space": c.sp.Info.ID, "name": c.sp.Info.Name, "dryRun": req.DryRun}, out)})
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
	s.auditGroup(c, tenant, "group_members_batch", "", batchDetail(map[string]any{"group": id, "dryRun": req.DryRun}, out))
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
	// Putting people into groups (now, or when an invite is accepted) is
	// managing the groups: without that key, a row that names one is refused
	// and no group is looked up.
	mayGroup := c.p.Access.Can(authz.TenantGroupsManage, tenant)
	if !mayGroup {
		groupNames = nil
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
			if strings.TrimSpace(name) == "" {
				continue
			}
			if !mayGroup {
				o.Status, o.Detail = spaces.BatchForbidden, "Putting people into groups needs the permission to manage groups."
				break
			}
			g, ok := groups[store.FoldName(strings.TrimSpace(name))]
			if !ok {
				o.Status, o.Detail = spaces.BatchInvalid, "No group is called "+strings.TrimSpace(name)+"."
				break
			}
			gids = append(gids, g)
		}
		switch {
		case o.Detail != "":
		case res[i].Hit != nil:
			h := res[i].Hit
			o.UserID, o.Username, o.Name = h.ID, h.Username, h.Name
			// A person already in the tenant keeps their role: changing it is
			// managing people, on their own page, not something a file does.
			if r.Role != "" && r.Role != h.Role {
				o.Detail = "The role is not changed for someone already in the organization; change it on their page."
			}
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
			// An address another account holds, or that has an open invite
			// already, has its own status.
			var ae *auth.Error
			if errors.As(errs[j], &ae) && (ae.Code == auth.CodeEmailTaken || ae.Code == auth.CodeAlreadyInvited) {
				out[i].Status = ae.Code
			}
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
				if _, _, _, _, err := s.spaces.AddGroupPeople(ctx, store.InTenant(tenant), g, ids, spaces.GroupJoin{By: c.user().ID}); err != nil {
					for i := range out {
						if out[i].Status == "added" && slices.Contains(ids, out[i].UserID) {
							out[i].Status, out[i].Detail = "failed", err.Error()
						}
					}
				}
			}
		}
	}
	s.audit(c, auth.AuditEvent{Kind: "invites_batch", Actor: c.user().ID, Tenant: tenant,
		Detail: batchDetail(map[string]any{"tenant": tenant, "sender": sender, "dryRun": req.DryRun}, out)})
	writeBatch(c, req.DryRun, out, map[string]any{"mail": map[string]any{"configured": sender != "", "sender": sender}})
}
