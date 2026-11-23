package api

// Server storage (milestone 3.1, docs/api.md "Server storage"): the site's
// settings, the per-space pin and exemption, eviction on request, and the
// storage blocks of the lists. The policy itself lives in internal/spaces.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

type storageResp struct {
	Remotes struct {
		Require   *string `json:"require"`
		Scope     string  `json:"scope"`
		GraceDays int     `json:"graceDays"`
		Since     *string `json:"since"`
	} `json:"remotes"`
	Hosts struct {
		Allowed  []string `json:"allowed"`
		FromFlag bool     `json:"fromFlag"`
	} `json:"hosts"`
	LocalSpaces struct {
		MaxBytes *int64 `json:"maxBytes"`
	} `json:"localSpaces"`
	Cache struct {
		Partial        bool   `json:"partial"`
		EvictAfterDays *int   `json:"evictAfterDays"`
		MinIdleHours   int    `json:"minIdleHours"`
		ClonesBytes    *int64 `json:"clonesBytes"`
		MinFreeBytes   *int64 `json:"minFreeBytes"`
	} `json:"cache"`
	Git struct {
		Version      string `json:"version"`
		PartialClone bool   `json:"partialClone"`
	} `json:"git"`
	Affected struct {
		InGrace int `json:"inGrace"`
		Overdue int `json:"overdue"`
		Exempt  int `json:"exempt"`
	} `json:"affected"`
}

func (s *server) storageBody(set spaces.StorageSettings, aff spaces.Affected) storageResp {
	var r storageResp
	r.Remotes.Require, r.Remotes.Scope, r.Remotes.GraceDays = set.Remotes.Require, set.Remotes.Scope, set.Remotes.GraceDays
	if !set.Remotes.Since.IsZero() {
		t := rfc3339(set.Remotes.Since)
		r.Remotes.Since = &t
	}
	r.Hosts.Allowed, r.Hosts.FromFlag = set.Hosts.Allowed, s.spaces.HostsFromFlag()
	r.LocalSpaces.MaxBytes = set.LocalSpaces.MaxBytes
	c := set.Cache
	r.Cache.Partial, r.Cache.EvictAfterDays, r.Cache.MinIdleHours, r.Cache.ClonesBytes, r.Cache.MinFreeBytes = c.Partial, c.EvictAfterDays, c.MinIdleHours, c.ClonesBytes, c.MinFreeBytes
	r.Git.Version, r.Git.PartialClone = s.spaces.GitInfo()
	r.Affected.InGrace, r.Affected.Overdue, r.Affected.Exempt = aff.InGrace, aff.Overdue, aff.Exempt
	return r
}

func (s *server) adminGetStorage(c *call) {
	aff, err := s.spaces.Affected(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.storageBody(s.spaces.Storage(), aff))
}

// section decodes one section of a PUT: an object of known keys.
func section(raw json.RawMessage, known ...string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, fmt.Errorf("%w: a section is an object", spaces.ErrInvalid)
	}
nextKey:
	for k := range m {
		for _, n := range known {
			if k == n {
				continue nextKey
			}
		}
		return nil, fmt.Errorf("%w: %q is not a setting here", spaces.ErrInvalid, k)
	}
	return m, nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

func decodeAs[T any](raw json.RawMessage, what string) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("%w: %s has the wrong type", spaces.ErrInvalid, what)
	}
	return v, nil
}

func (s *server) adminPutStorage(c *call) {
	var req struct {
		Remotes     json.RawMessage `json:"remotes"`
		Hosts       json.RawMessage `json:"hosts"`
		LocalSpaces json.RawMessage `json:"localSpaces"`
		Cache       json.RawMessage `json:"cache"`
		Assets      json.RawMessage `json:"assets"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if len(bytes.TrimSpace(req.Assets)) > 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "the attachment cache settings are not available on this server yet")
		return
	}
	remotes, err1 := section(req.Remotes, "require", "scope", "graceDays")
	hosts, err2 := section(req.Hosts, "allowed")
	local, err3 := section(req.LocalSpaces, "maxBytes")
	cache, err4 := section(req.Cache, "partial", "evictAfterDays", "minIdleHours", "clonesBytes", "minFreeBytes")
	for _, err := range []error{err1, err2, err3, err4} {
		if err != nil {
			s.fail(c.w, err)
			return
		}
	}
	if hosts != nil && s.spaces.HostsFromFlag() {
		writeError(c.w, http.StatusConflict, "conflict", "the allowed hosts were given with --remote-host, which wins; remove the flag to set them here")
		return
	}
	patch := func(st *spaces.StorageSettings) error {
		for k, v := range remotes {
			var err error
			switch k {
			case "require":
				if isNull(v) {
					st.Remotes.Require = nil
				} else {
					var r string
					if r, err = decodeAs[string](v, "remotes.require"); err == nil {
						st.Remotes.Require = &r
					}
				}
			case "scope":
				st.Remotes.Scope, err = decodeAs[string](v, "remotes.scope")
			case "graceDays":
				st.Remotes.GraceDays, err = decodeAs[int](v, "remotes.graceDays")
			}
			if err != nil {
				return err
			}
		}
		for k, v := range hosts {
			if k == "allowed" {
				l, err := decodeAs[[]string](v, "hosts.allowed")
				if err != nil {
					return err
				}
				st.Hosts.Allowed = l
			}
		}
		for k, v := range local {
			if k == "maxBytes" {
				if isNull(v) {
					st.LocalSpaces.MaxBytes = nil
				} else {
					n, err := decodeAs[int64](v, "localSpaces.maxBytes")
					if err != nil {
						return err
					}
					st.LocalSpaces.MaxBytes = &n
				}
			}
		}
		for k, v := range cache {
			var err error
			switch k {
			case "partial":
				st.Cache.Partial, err = decodeAs[bool](v, "cache.partial")
			case "evictAfterDays":
				if isNull(v) {
					st.Cache.EvictAfterDays = nil
				} else {
					var n int
					if n, err = decodeAs[int](v, "cache.evictAfterDays"); err == nil {
						st.Cache.EvictAfterDays = &n
					}
				}
			case "minIdleHours":
				st.Cache.MinIdleHours, err = decodeAs[int](v, "cache.minIdleHours")
			case "clonesBytes", "minFreeBytes":
				var p *int64
				if !isNull(v) {
					var n int64
					if n, err = decodeAs[int64](v, "cache."+k); err == nil {
						p = &n
					}
				}
				if k == "clonesBytes" {
					st.Cache.ClonesBytes = p
				} else {
					st.Cache.MinFreeBytes = p
				}
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	set, aff, err := s.spaces.SetStorage(c.r.Context(), patch)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.storageBody(set, aff))
}

// spaceStorageItem is the storage block of an item of the administration's list.
type adminStorageResp struct {
	Cache           string      `json:"cache"`
	Pinned          bool        `json:"pinned"`
	Exempt          bool        `json:"exempt"`
	LastUsed        *string     `json:"lastUsed"`
	CloneBytes      int64       `json:"cloneBytes"`
	IndexBytes      int64       `json:"indexBytes"`
	AssetCacheBytes int64       `json:"assetCacheBytes"`
	Assets          string      `json:"assets"`
	Remote          *remoteNote `json:"remote"`
}

func (s *server) adminStorageOf(ctx context.Context, r store.AdminSpace) *adminStorageResp {
	hasRemote := r.CredType != ""
	out := &adminStorageResp{
		Cache:  s.spaces.CacheState(r.Storage.Cache, hasRemote, r.ID),
		Pinned: r.Storage.Pinned, Exempt: r.Storage.Exempt,
		IndexBytes: r.Storage.IndexBytes, Assets: "space",
		Remote: toNote(s.spaces.NoticeOfRow(r)),
	}
	if t := s.spaces.LastUsed(r.Storage.LastUsed, r.ID); !t.IsZero() {
		v := rfc3339(t)
		out.LastUsed = &v
	}
	if r.SizeBytes != nil && out.Cache != "evicted" {
		out.CloneBytes = *r.SizeBytes
	}
	return out
}

// adminPutSpaceStorage sets the pin and the exemption of one space.
func (s *server) adminPutSpaceStorage(c *call) {
	var req struct {
		Pin    *bool `json:"pin"`
		Exempt *bool `json:"exempt"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	id := c.r.PathValue("space")
	ctx := c.r.Context()
	if _, err := store.GetSpace(ctx, s.opt.Store.Q(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c.w, http.StatusNotFound, "not_found", "no such space")
			return
		}
		s.fail(c.w, err)
		return
	}
	if req.Pin != nil {
		if err := s.spaces.SetPinned(ctx, id, *req.Pin); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	if req.Exempt != nil {
		if err := s.spaces.SetExempt(ctx, id, *req.Exempt); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	row, err := store.GetAdminSpace(ctx, s.opt.Store.Q(), id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.adminSpaceItems(c, []store.AdminSpace{row})[0])
}

// adminEvictSpace evicts a space now, if the rules allow it.
func (s *server) adminEvictSpace(c *call) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.r.Context()), 5*time.Minute)
	defer cancel()
	if err := s.spaces.Evict(ctx, c.r.PathValue("space"), spaces.EvictOptions{AdminRequest: true}); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// storageStatus is the storage block of the administration's status.
func (s *server) storageStatus(ctx context.Context) (any, error) {
	sums, err := store.SumStorage(ctx, s.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	aff, err := s.spaces.Affected(ctx)
	if err != nil {
		return nil, err
	}
	restoring, waiting := s.spaces.RestoreCounts()
	set := s.spaces.Storage()
	return map[string]any{
		"spaces":     map[string]int{"local": sums.Local, "full": sums.Full, "partial": sums.Partial, "evicted": sums.Evicted, "restoring": restoring},
		"cloneBytes": sums.CloneBytes, "indexBytes": sums.IndexBytes, "assetCacheBytes": 0,
		"clonesBudget": set.Cache.ClonesBytes, "assetsBudget": nil, "restoresWaiting": waiting,
		"remotes": map[string]int{"inGrace": aff.InGrace, "overdue": aff.Overdue},
	}, nil
}
