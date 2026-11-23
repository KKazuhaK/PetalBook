package api

import (
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/spaces"
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
			Via      string `json:"via"`
		} `json:"credential"`
		GitHub *struct {
			Owner       string `json:"owner"`
			Name        string `json:"name"`
			Private     *bool  `json:"private"`
			Description string `json:"description"`
			Create      *bool  `json:"create"`
			Via         string `json:"via"`
		} `json:"github"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, remoteBodyLimit) {
		return
	}
	var v *spaces.AssetsView
	var err error
	if g := req.GitHub; g != nil {
		v, err = s.spaces.ConnectAssetsGitHub(c.r.Context(), c.sp, c.user().ID, g.Via, spaces.GitHubCreate{
			Owner: g.Owner, Name: g.Name, Private: g.Private == nil || *g.Private, Description: g.Description, Create: g.Create == nil || *g.Create})
	} else {
		v, err = s.spaces.ConnectAssets(c.r.Context(), c.sp, spaces.ConnectRequest{URL: req.URL, Branch: req.Branch,
			CredType: req.Credential.Type, Username: req.Credential.Username, Token: req.Credential.Token,
			Via: req.Credential.Via, UserID: c.user().ID})
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toAssetsRemote(v))
}

func (s *server) deleteAssetsRemote(c *call) {
	if err := s.spaces.DisconnectAssets(c.r.Context(), c.sp); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) moveAssets(c *call) {
	v, started, err := s.spaces.StartMove(c.r.Context(), c.sp)
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
