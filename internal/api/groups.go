package api

import (
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Groups (milestone 3.2); the contract is "Groups" in docs/api.md.

type groupRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

func toGroupRefs(l []store.GroupRef) []groupRef {
	out := make([]groupRef, len(l))
	for i, g := range l {
		out[i] = groupRef{g.ID, g.Name, g.Source}
	}
	return out
}

type groupResp struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"`
	People      int               `json:"people"`
	Spaces      int               `json:"spaces"`
	MappedFrom  []auth.MappedFrom `json:"mappedFrom"`
	Created     string            `json:"created"`
	Updated     string            `json:"updated"`
}

func (s *server) toGroups(l []store.Group) []groupResp {
	var ids []string
	for _, g := range l {
		if g.Source == store.GroupSynced {
			ids = append(ids, g.ID)
		}
	}
	from := s.auth.MappedFromOf(ids)
	out := make([]groupResp, len(l))
	for i, g := range l {
		mf := from[g.ID]
		if mf == nil {
			mf = []auth.MappedFrom{}
		}
		out[i] = groupResp{g.ID, g.Name, g.Description, g.Source, g.People, g.Spaces, mf, rfc3339(g.Created), rfc3339(g.Updated)}
	}
	return out
}

type groupMemberResp struct {
	GroupID string `json:"groupId"`
	Name    string `json:"name"`
	Source  string `json:"source"`
	Role    string `json:"role"`
	People  int    `json:"people"`
	Yours   bool   `json:"yours"`
}

func toGroupMember(g store.SpaceGroup) groupMemberResp {
	return groupMemberResp{g.GroupID, g.Name, g.Source, g.Role, g.People, g.Yours}
}

func toGroupMembers(l []store.SpaceGroup) []groupMemberResp {
	out := make([]groupMemberResp, len(l))
	for i, g := range l {
		out[i] = toGroupMember(g)
	}
	return out
}

// failGroup answers the refusals of groups; false when err is none of them.
func (s *server) failGroup(w http.ResponseWriter, err error) bool {
	var inUse *spaces.GroupInUseError
	var missing *spaces.MissingUsersError
	var via *spaces.ViaGroupError
	switch {
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, errorBody{Error: "group_in_use", Message: inUse.Error(), Spaces: &inUse.Spaces, Mappings: &inUse.Mappings})
	case errors.As(err, &missing):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "user_not_found", Message: missing.Error(), Missing: missing.Missing})
	case errors.As(err, &via):
		writeError(w, http.StatusConflict, "member_via_group", via.Error())
	case errors.Is(err, spaces.ErrGroupNotFound):
		writeError(w, http.StatusNotFound, "group_not_found", "there is no such group")
	case errors.Is(err, spaces.ErrGroupExists):
		writeError(w, http.StatusConflict, "exists", "a group with that name exists already")
	case errors.Is(err, spaces.ErrGroupSynced):
		writeError(w, http.StatusConflict, "group_synced", err.Error())
	case errors.Is(err, spaces.ErrGroupNotSynced):
		writeError(w, http.StatusConflict, "group_not_synced", err.Error())
	case errors.Is(err, spaces.ErrGroupLimit):
		writeError(w, http.StatusConflict, "group_limit", err.Error()[len(spaces.ErrGroupLimit.Error())+2:])
	case errors.Is(err, spaces.ErrGroupNotMember):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, spaces.ErrNotInGroup):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, spaces.ErrGroupMember):
		writeError(w, http.StatusConflict, "exists", err.Error())
	default:
		return false
	}
	return true
}

// failAdminGroup is fail for the routes that name the group in the path: a group
// that is not there is a plain not_found (group_not_found is for a group named
// in a body).
func (s *server) failAdminGroup(w http.ResponseWriter, err error) {
	if errors.Is(err, spaces.ErrGroupNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "there is no such group")
		return
	}
	s.fail(w, err)
}

// listLimit reads limit (1 to max), answering 400 itself.
func listLimit(c *call, def, max int) (int, bool) {
	v := c.r.URL.Query().Get("limit")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		writeError(c.w, http.StatusBadRequest, "invalid", "limit is a number from 1 to "+strconv.Itoa(max))
		return 0, false
	}
	return n, true
}

// auditGroup writes a group's event; tenant is the group's ("" takes the
// caller's, or the person's the event is about).
func (s *server) auditGroup(c *call, tenant, kind, user string, detail map[string]any) {
	s.audit(c, auth.AuditEvent{Kind: kind, Actor: c.user().ID, User: user, Detail: detail, Tenant: tenant})
}

// --- administrators ------------------------------------------------------------------

func (s *server) adminListGroups(c *call) {
	q := c.r.URL.Query()
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	src := q.Get("source")
	if src != "" && src != store.GroupLocal && src != store.GroupSynced {
		writeError(c.w, http.StatusBadRequest, "invalid", "source is local or synced")
		return
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := s.spaces.ListGroups(c.r.Context(), sc, store.GroupFilter{Query: q.Get("q"), Source: src, UserID: q.Get("user"), After: q.Get("cursor"), Limit: limit})
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []groupResp `json:"items"`
		Next  string      `json:"next"`
	}{s.toGroups(rows), next})
}

func (s *server) adminGetGroup(c *call) {
	g, err := s.spaces.Group(c.r.Context(), store.ScopeOf(c.user()), c.r.PathValue("group"))
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.toGroups([]store.Group{*g})[0])
}

func (s *server) adminCreateGroup(c *call) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	tenant, ok := s.createIn(c)
	if !ok {
		return
	}
	g, err := s.spaces.CreateGroup(c.r.Context(), tenant, req.Name, req.Description, req.Source)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	s.auditGroup(c, g.TenantID, "group_created", "", map[string]any{"group": g.ID, "name": g.Name, "source": g.Source})
	writeJSON(c.w, http.StatusCreated, s.toGroups([]store.Group{*g})[0])
}

func (s *server) adminPatchGroup(c *call) {
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Source      *string `json:"source"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Source != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "the source of a group cannot change")
		return
	}
	g, changed, err := s.spaces.UpdateGroup(c.r.Context(), store.ScopeOf(c.user()), c.r.PathValue("group"), req.Name, req.Description)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	if len(changed) > 0 {
		d := map[string]any{"group": g.ID, "name": g.Name, "source": g.Source}
		for _, f := range changed {
			if f == "description" {
				d[f] = g.Description
			}
		}
		s.auditGroup(c, g.TenantID, "group_changed", "", d)
	}
	writeJSON(c.w, http.StatusOK, s.toGroups([]store.Group{*g})[0])
}

func (s *server) adminDeleteGroup(c *call) {
	force := false
	if q := c.r.URL.Query(); q.Has("force") {
		switch q.Get("force") {
		case "1":
			force = true
		case "0":
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "force is 1 or 0")
			return
		}
	}
	id := c.r.PathValue("group")
	ctx := c.r.Context()
	mappings := s.auth.GroupMappings(id)
	if force && mappings > 0 {
		if err := s.auth.UnmapGroup(ctx, id); err != nil {
			s.failAdminGroup(c.w, err)
			return
		}
	}
	g, err := s.spaces.DeleteGroup(ctx, store.ScopeOf(c.user()), id, force, mappings)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	s.auditGroup(c, g.TenantID, "group_deleted", "", map[string]any{"group": g.ID, "name": g.Name, "source": g.Source, "people": g.People, "spaces": g.Spaces})
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminGroupMembers(c *call) {
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	rows, next, err := s.spaces.GroupPeople(c.r.Context(), store.ScopeOf(c.user()), c.r.PathValue("group"), q.Get("q"), q.Get("cursor"), limit)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	type from struct {
		Provider string `json:"provider"`
		Value    string `json:"value"`
	}
	type item struct {
		UserID   string `json:"userId"`
		Username string `json:"username"`
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
		From     []from `json:"from"`
		Added    string `json:"added"`
	}
	items := make([]item, len(rows))
	for i, r := range rows {
		f := make([]from, len(r.From))
		for j, x := range r.From {
			f[j] = from{x.Provider, x.Value}
		}
		items[i] = item{r.UserID, r.Username, r.Name, r.Disabled, f, rfc3339(r.Added)}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []item `json:"items"`
		Next  string `json:"next"`
	}{items, next})
}

func (s *server) adminAddGroupMembers(c *call) {
	var req struct {
		UserIDs []string `json:"userIds"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, 64<<10) {
		return
	}
	id := c.r.PathValue("group")
	added, already, ids, err := s.spaces.AddGroupPeople(c.r.Context(), store.ScopeOf(c.user()), id, req.UserIDs)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	if added > 0 {
		s.auditGroup(c, "", "group_member_added", "", map[string]any{"group": id, "users": ids})
	}
	writeJSON(c.w, http.StatusOK, struct {
		Added   int `json:"added"`
		Already int `json:"already"`
	}{added, already})
}

func (s *server) adminRemoveGroupMember(c *call) {
	id, user := c.r.PathValue("group"), c.r.PathValue("user")
	if err := s.spaces.RemoveGroupPerson(c.r.Context(), store.ScopeOf(c.user()), id, user); err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	s.auditGroup(c, "", "group_member_removed", user, map[string]any{"group": id, "users": []string{user}})
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminGroupSpaces(c *call) {
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	rows, next, err := s.spaces.GroupSpaces(c.r.Context(), store.ScopeOf(c.user()), c.r.PathValue("group"), c.r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.failAdminGroup(c.w, err)
		return
	}
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	items := make([]item, len(rows))
	for i, r := range rows {
		items[i] = item{r.ID, r.Name, r.Role}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []item `json:"items"`
		Next  string `json:"next"`
	}{items, next})
}

// --- everyone signed in --------------------------------------------------------------

func (s *server) findGroups(c *call) {
	q := c.r.URL.Query().Get("q")
	if n := utf8.RuneCountInString(q); n < 1 || n > 80 {
		writeError(c.w, http.StatusBadRequest, "invalid", "q is 1 to 80 characters")
		return
	}
	limit, ok := listLimit(c, 20, 20)
	if !ok {
		return
	}
	l, err := s.spaces.FindGroups(c.r.Context(), c.ownScope(), q, limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Groups []groupRef `json:"groups"`
	}{toGroupRefs(l)})
}

// --- groups in a space ---------------------------------------------------------------

func (s *server) effectiveMembers(c *call) {
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	rows, next, err := s.spaces.EffectiveMembers(c.r.Context(), c.sp, q.Get("q"), q.Get("cursor"), limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type via struct {
		GroupID string `json:"groupId"`
		Name    string `json:"name"`
		Role    string `json:"role"`
	}
	type item struct {
		UserID     string `json:"userId"`
		Username   string `json:"username"`
		Name       string `json:"name"`
		Role       string `json:"role"`
		DirectRole string `json:"directRole"`
		Groups     []via  `json:"groups"`
		You        bool   `json:"you"`
	}
	items := make([]item, len(rows))
	for i, r := range rows {
		g := make([]via, len(r.Groups))
		for j, x := range r.Groups {
			g[j] = via{x.GroupID, x.Name, x.Role}
		}
		items[i] = item{r.UserID, r.Username, r.Name, spaces.EffectiveRole(r.DirectRole, r.Groups), r.DirectRole, g, r.UserID == c.user().ID}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []item `json:"items"`
		Next  string `json:"next"`
	}{items, next})
}

func (s *server) addSpaceGroup(c *call) {
	var req struct {
		GroupID string `json:"groupId"`
		Role    string `json:"role"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	g, err := s.spaces.AddSpaceGroup(c.r.Context(), c.sp, c.user().ID, req.GroupID, req.Role)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.auditGroup(c, "", "space_group_added", "", map[string]any{"space": c.sp.Info.ID, "group": g.GroupID, "role": g.Role})
	writeJSON(c.w, http.StatusCreated, toGroupMember(*g))
}

func (s *server) patchSpaceGroup(c *call) {
	var req struct {
		Role string `json:"role"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	g, old, err := s.spaces.SetSpaceGroupRole(c.r.Context(), c.sp, c.user().ID, c.r.PathValue("group"), req.Role)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if old != g.Role {
		s.auditGroup(c, "", "space_group_changed", "", map[string]any{"space": c.sp.Info.ID, "group": g.GroupID, "role": map[string]string{"old": old, "new": g.Role}})
	}
	writeJSON(c.w, http.StatusOK, toGroupMember(*g))
}

func (s *server) deleteSpaceGroup(c *call) {
	id := c.r.PathValue("group")
	old, err := s.spaces.RemoveSpaceGroup(c.r.Context(), c.sp, id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.auditGroup(c, "", "space_group_removed", "", map[string]any{"space": c.sp.Info.ID, "group": id, "role": old})
	c.w.WriteHeader(http.StatusNoContent)
}
