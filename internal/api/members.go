package api

import (
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

type memberResp struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	You      bool   `json:"you"`
}

// memberListResp is a member in the list of members, which also says how the
// groups add to them: ViaGroups is whether a group of theirs is a member of
// the space too, EffectiveRole the role their own and those groups' roles give
// together.
type memberListResp struct {
	memberResp
	ViaGroups     bool   `json:"viaGroups"`
	EffectiveRole string `json:"effectiveRole"`
}

func toMember(m store.Member, you string) memberResp {
	return memberResp{m.UserID, m.Username, m.Name, m.Role, m.UserID == you}
}

func (s *server) listMembers(c *call) {
	l, groups, err := s.spaces.MembersView(c.r.Context(), c.sp, c.user().ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]memberListResp, len(l))
	for i, m := range l {
		out[i] = memberListResp{toMember(m.Member, c.user().ID), m.ViaGroups, m.EffectiveRole}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Members []memberListResp  `json:"members"`
		Groups  []groupMemberResp `json:"groups"`
	}{out, toGroupMembers(groups)})
}

func (s *server) addMember(c *call) {
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	m, err := s.spaces.AddMember(c.r.Context(), c.sp, req.Username, req.Role)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	// The person is told they were added (an invitation to the space, as far as
	// there are any: the space is theirs to open from now on). Not when they added
	// themselves, and a failure is only logged: the member is added.
	if m.UserID != c.user().ID {
		err := store.NotifyUser(c.r.Context(), s.opt.Store.Q(), m.UserID, store.NotifyInvite, c.sp.Info.ID,
			map[string]string{"space": c.sp.Info.Name, "role": m.Role, "by": c.user().Name}, s.now())
		if err != nil {
			s.log.Warn("could not write a notification", "error", err)
		}
	}
	writeJSON(c.w, http.StatusCreated, toMember(*m, c.user().ID))
}

func (s *server) patchMember(c *call) {
	var req struct {
		Role string `json:"role"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	m, err := s.spaces.SetMemberRole(c.r.Context(), c.sp, c.r.PathValue("user"), req.Role)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toMember(*m, c.user().ID))
}

func (s *server) removeMember(c *call) {
	if err := s.spaces.RemoveMember(c.r.Context(), c.user(), c.sp, c.r.PathValue("user")); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}
