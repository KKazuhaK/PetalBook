package api

import (
	"errors"
	"net/http"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/jobs"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// The attachment repository of a space (milestone 3.1, docs/api.md "Attachment
// repository").

type moveResp struct {
	State     string `json:"state"`
	Files     int64  `json:"files"`
	FilesDone int64  `json:"filesDone"`
	Bytes     int64  `json:"bytes"`
	BytesDone int64  `json:"bytesDone"`
	Error     string `json:"error"`
}

// assetsRemoteResp is a Remote with the move.
type assetsRemoteResp struct {
	remoteResp
	Move *moveResp `json:"move"`
}

func toAssetsRemote(v *spaces.AssetsView) assetsRemoteResp {
	out := assetsRemoteResp{remoteResp: toRemote(&v.RemoteView)}
	if m := v.Move; m != nil {
		out.Move = &moveResp{m.State, m.Files, m.FilesDone, m.Bytes, m.BytesDone, m.Error}
	}
	return out
}

func (s *server) getAssetsRemote(c *call) {
	v, err := s.spaces.AssetsRemote(c.r.Context(), c.sp)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toAssetsRemote(v))
}

func (s *server) putAssetsRemote(c *call) {
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
		// Sync makes (or finds) the repository on a provider, as storage.sync
		// does for a new space.
		Sync *struct {
			Provider    string `json:"provider"`
			Owner       string `json:"owner"`
			Name        string `json:"name"`
			Private     *bool  `json:"private"`
			Description string `json:"description"`
			Create      *bool  `json:"create"`
			Via         string `json:"via"`
		} `json:"sync"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, remoteBodyLimit) {
		return
	}
	if !s.assetsRepositoryMayStart(c) {
		return
	}
	var v *spaces.AssetsView
	var err error
	if g := req.Sync; g != nil {
		if p, ok := s.registry().Get(g.Provider); !ok || p.Descriptor().Type == connections.GitID {
			writeError(c.w, http.StatusBadRequest, "invalid", "sync.provider is not a provider of this server that makes repositories.")
			return
		}
		v, err = s.spaces.ConnectAssetsSync(c.r.Context(), c.sp, c.user().ID, g.Via, spaces.SyncCreate{Provider: g.Provider,
			Owner: g.Owner, Name: g.Name, Private: g.Private == nil || *g.Private, Description: g.Description, Create: g.Create == nil || *g.Create})
	} else {
		v, err = s.spaces.ConnectAssets(c.r.Context(), c.sp, spaces.ConnectRequest{URL: req.URL, Branch: req.Branch,
			CredType: req.Credential.Type, Username: req.Credential.Username, Token: req.Credential.Token,
			Provider: req.Credential.Provider, Via: req.Credential.Via, UserID: c.user().ID})
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toAssetsRemote(v))
}

// assetsRepositoryMayStart refuses a new attachment repository for a space of a
// tenant whose storage.assets rule leaves only "space" (a rule for the tenant
// restricting it to that, which is what the limits sheet's
// sync.assetsRepository was), with the code the space's creation gives
// (setting_restricted). A space that has one already may reconnect it. It
// answers false once it has answered.
func (s *server) assetsRepositoryMayStart(c *call) bool {
	if s.single {
		return true
	}
	ctx := c.r.Context()
	sp, err := store.GetSpace(ctx, s.opt.Store.Q(), store.AllTenants(), c.sp.Info.ID)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	r, err := s.assetsRule(ctx, sp.TenantID)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	if r.Allows("repository") {
		return true
	}
	if _, err := s.spaces.AssetsRemote(ctx, c.sp); err == nil {
		return true
	} else if !errors.Is(err, spaces.ErrNoAssets) {
		s.fail(c.w, err)
		return false
	}
	s.fail(c.w, &settings.Error{Code: settings.CodeRestricted, Msg: "The administrators do not allow attachments in a repository of their own."})
	return false
}

func (s *server) deleteAssetsRemote(c *call) {
	if err := s.spaces.DisconnectAssets(c.r.Context(), c.sp); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) moveAssets(c *call) {
	v, started, err := s.spaces.StartMove(c.r.Context(), c.sp, c.user().ID, c.user().Username)
	if errors.Is(err, jobs.ErrClosed) {
		// The server is stopping: the move is not added (as a bulk action).
		writeError(c.w, http.StatusServiceUnavailable, "unavailable", "The server is stopping: ask again once it has started.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	status := http.StatusOK
	if started {
		status = http.StatusAccepted
	}
	writeJSON(c.w, status, toAssetsRemote(v))
}
