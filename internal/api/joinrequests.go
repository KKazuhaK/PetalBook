package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// Requests to join an organisation by email domain (3.6 d5, docs/api.md
// "Joining by email domain"): a tenant's administrators list them, and
// approve or deny each (tenant.users.manage, features.domains). The person's
// side is the waiting page's (mailapi.go).

type joinDecidedByResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// joinRequestResp is a request as the tenant's administrators list it.
type joinRequestResp struct {
	ID        string             `json:"id"`
	Username  string             `json:"username"`
	Name      string             `json:"name"`
	Email     string             `json:"email"`
	Domain    string             `json:"domain"`
	State     string             `json:"state"`
	Created   string             `json:"created"`
	Expires   string             `json:"expires"`
	DecidedAt *string            `json:"decidedAt"`
	DecidedBy *joinDecidedByResp `json:"decidedBy"`
	// User is the account an approval made (null otherwise).
	User *string `json:"user"`
}

func (s *server) joinRequestOf(c *call, r store.JoinRequest, names map[string]string) joinRequestResp {
	out := joinRequestResp{ID: r.ID, Username: r.Username, Name: r.Name, Email: r.Email, Domain: r.Domain, State: r.StateAt(s.now()),
		Created: rfc3339(r.Created), Expires: rfc3339(r.Expires), DecidedAt: optTime(r.DecidedAt)}
	if r.DecidedBy != "" {
		name, ok := names[r.DecidedBy]
		if !ok {
			if u, err := store.GetUser(c.r.Context(), s.opt.Store.Q(), store.AllTenants(), r.DecidedBy); err == nil {
				name = u.Name
			}
			names[r.DecidedBy] = name
		}
		out.DecidedBy = &joinDecidedByResp{r.DecidedBy, name}
	}
	if r.MadeUser != "" {
		u := r.MadeUser
		out.User = &u
	}
	return out
}

// joinTenant is the {tenant} of a join route: one whose people the caller
// manages, else not found.
func (s *server) joinTenant(c *call) (string, bool) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantUsersManage, id) || !s.knownTenant(id) {
		tenantNotFound(c)
		return "", false
	}
	return id, true
}

// adminListJoinRequests is GET …/join-requests[?state=pending|all][&limit=][&cursor=]:
// newest first; the pending ones (that have not expired) by default.
func (s *server) adminListJoinRequests(c *call) {
	t, ok := s.joinTenant(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	f := store.JoinFilter{PendingOnly: true, Limit: 50}
	switch q.Get("state") {
	case "", "pending":
	case "all":
		f.PendingOnly = false
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "State is pending or all.")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit must be from 1 to 200.")
			return
		}
		f.Limit = n
	}
	if cur := q.Get("cursor"); cur != "" {
		at, id, ok := strings.Cut(cur, "/")
		n, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil || id == "" {
			writeError(c.w, http.StatusBadRequest, "invalid", "That is not a cursor of this list.")
			return
		}
		f.AfterCreated, f.AfterID = time.UnixMilli(n), id
	}
	limit := f.Limit
	f.Limit++
	rows, err := store.ListJoinRequests(c.r.Context(), s.opt.Store.Q(), t, f, s.now())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		n := strconv.FormatInt(last.Created.UnixMilli(), 10) + "/" + last.ID
		next = &n
	}
	names := map[string]string{}
	out := make([]joinRequestResp, len(rows))
	for i, r := range rows {
		out[i] = s.joinRequestOf(c, r, names)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []joinRequestResp `json:"items"`
		Next  *string           `json:"next"`
	}{out, next})
}

// adminApproveJoinRequest is POST …/join-requests/{id}/approve {"role"}: the
// account is made in the tenant (role member, the default).
func (s *server) adminApproveJoinRequest(c *call) {
	t, ok := s.joinTenant(c)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if !s.readJSON(c.w, c.r, &req, true) {
		return
	}
	r, _, err := s.auth.ApproveJoinRequest(c.r.Context(), c.user(), t, c.r.PathValue("id"), req.Role, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.joinRequestOf(c, r, map[string]string{c.user().ID: c.user().Name}))
}

// adminDenyJoinRequest is POST …/join-requests/{id}/deny: the username is
// free again, and the person is mailed.
func (s *server) adminDenyJoinRequest(c *call) {
	t, ok := s.joinTenant(c)
	if !ok {
		return
	}
	var req struct{}
	if !s.readJSON(c.w, c.r, &req, true) {
		return
	}
	r, err := s.auth.DenyJoinRequest(c.r.Context(), c.user(), t, c.r.PathValue("id"), s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.joinRequestOf(c, r, map[string]string{c.user().ID: c.user().Name}))
}
