package api

import (
	"net/http"
	"strconv"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// remoteBodyLimit caps the bodies of the remote endpoints: an address and a token.
const remoteBodyLimit = 64 << 10

type credentialResp struct {
	Type        string `json:"type"`
	Username    string `json:"username,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// A connection credential: the provider, how it syncs, and what it syncs.
	Provider       string        `json:"provider,omitempty"`
	Via            string        `json:"via,omitempty"`
	InstallationID *int64        `json:"installationId,omitempty"`
	Account        string        `json:"account,omitempty"`
	RepoID         *int64        `json:"repoId,omitempty"`
	User           *connUserResp `json:"user,omitempty"`
	Login          string        `json:"login,omitempty"`
}

type connUserResp struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type rewrittenResp struct {
	LastGood string `json:"lastGood"`
	Remote   string `json:"remote"`
}

type remoteResp struct {
	URL        string         `json:"url"`
	Branch     string         `json:"branch"`
	Provider   string         `json:"provider"`
	Credential credentialResp `json:"credential"`
	State      string         `json:"state"`
	LastSync   string         `json:"lastSync,omitempty"`
	Error      string         `json:"error"`
	ErrorCode  string         `json:"errorCode"`
	HostKey    string         `json:"hostKey,omitempty"`
	Rewritten  *rewrittenResp `json:"rewritten"`
}

func toRemote(v *spaces.RemoteView) remoteResp {
	r := remoteResp{URL: v.URL, Branch: v.Branch, Provider: v.Provider, State: v.State, Error: v.Error, ErrorCode: v.ErrorCode, HostKey: v.HostKey,
		Credential: credentialResp{Type: v.CredType, Username: v.Username, PublicKey: v.PublicKey, Fingerprint: v.Fingerprint}}
	if g := v.Connection; g != nil {
		c := &r.Credential
		c.Provider = v.Provider
		c.Via, c.Account, c.Login = g.Via, g.Account, g.Login
		repo := g.RepoID
		c.RepoID = &repo
		if g.Via == "app" {
			inst := g.InstallationID
			c.InstallationID = &inst
		} else {
			c.User = &connUserResp{ID: g.UserID, Username: g.Username}
		}
	}
	if !v.LastSync.IsZero() {
		r.LastSync = rfc3339(v.LastSync)
	}
	if v.Rewritten != nil {
		r.Rewritten = &rewrittenResp{LastGood: v.Rewritten.LastGood, Remote: v.Rewritten.Remote}
	}
	return r
}

func (s *server) getRemote(c *call) {
	v, err := s.spaces.Remote(c.r.Context(), c.sp)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toRemote(v))
}

func (s *server) remoteKey(c *call) {
	var req struct {
		Rotate bool `json:"rotate"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, remoteBodyLimit) {
		return
	}
	pub, fp, err := s.spaces.SSHKey(c.r.Context(), c.sp, req.Rotate)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		PublicKey   string `json:"publicKey"`
		Fingerprint string `json:"fingerprint"`
	}{pub, fp})
}

func (s *server) putRemote(c *call) {
	var req struct {
		URL        string `json:"url"`
		Branch     string `json:"branch"`
		Credential struct {
			Type     string `json:"type"`
			Username string `json:"username"`
			Token    string `json:"token"`
			Provider string `json:"provider"`
			Via      string `json:"via"`
		} `json:"credential"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, remoteBodyLimit) {
		return
	}
	// Connecting reaches the network and may clone a whole repository; the
	// client going away must not leave it half done.
	v, err := s.spaces.Connect(c.r.Context(), c.sp, spaces.ConnectRequest{URL: req.URL, Branch: req.Branch,
		CredType: req.Credential.Type, Username: req.Credential.Username, Token: req.Credential.Token,
		Provider: req.Credential.Provider, Via: req.Credential.Via, UserID: c.user().ID})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toRemote(v))
}

func (s *server) deleteRemote(c *call) {
	if err := s.spaces.Disconnect(c.r.Context(), c.sp); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) syncRemote(c *call) {
	v, err := s.spaces.Sync(c.r.Context(), c.sp)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toRemote(v))
}

func (s *server) acceptRemote(c *call) {
	var req struct {
		Commit string `json:"commit"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, remoteBodyLimit) {
		return
	}
	if req.Commit == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "accepting needs commit, the remote commit shown in rewritten.remote")
		return
	}
	v, err := s.spaces.Accept(c.r.Context(), c.sp, req.Commit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toRemote(v))
}

// --- history ------------------------------------------------------------------

type historyItemResp struct {
	Commit string `json:"commit"`
	Time   string `json:"time"`
	Author string `json:"author"`
	Path   string `json:"path"`
	Rev    string `json:"rev,omitempty"`
	Change string `json:"change"`
	// Via is "token" or "mcp" for a change made with an API token or through MCP.
	Via string `json:"via,omitempty"`
}

func (s *server) getHistory(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	s.viewedPage(c, id)
	limit := 30
	if l := c.r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > 100 {
			writeError(c.w, http.StatusBadRequest, "invalid", "limit must be a number from 1 to 100")
			return
		}
		limit = n
	}
	items, next, err := c.sp.Space.History(c.r.Context(), id, limit, c.r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]historyItemResp, len(items))
	for i, it := range items {
		out[i] = historyItemResp{Commit: it.Commit, Time: rfc3339(it.Time), Author: it.Author, Path: it.Path, Rev: it.Rev, Change: it.Change, Via: it.Via}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []historyItemResp `json:"items"`
		Next  string            `json:"next"`
	}{out, next})
}

func (s *server) getVersion(c *call) {
	id, ok := s.pageID(c)
	if !ok {
		return
	}
	s.viewedPage(c, id)
	v, err := c.sp.Space.Version(c.r.Context(), id, c.r.PathValue("commit"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	meta := v.Meta
	if meta == nil {
		meta = page.Meta{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Commit string    `json:"commit"`
		Time   string    `json:"time"`
		Author string    `json:"author"`
		Path   string    `json:"path"`
		Rev    string    `json:"rev"`
		Meta   page.Meta `json:"meta"`
		Body   string    `json:"body"`
	}{v.Commit, rfc3339(v.Time), v.Author, v.Path, v.Rev, meta, v.Body})
}
