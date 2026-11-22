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

func toMember(m store.Member, you string) memberResp {
	return memberResp{m.UserID, m.Username, m.Name, m.Role, m.UserID == you}
}

func (s *server) listMembers(c *call) {
	l, err := s.spaces.Members(c.r.Context(), c.sp)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]memberResp, len(l))
	for i, m := range l {
		out[i] = toMember(m, c.user().ID)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Members []memberResp `json:"members"`
	}{out})
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
