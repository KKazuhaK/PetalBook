package api

// Server storage (milestone 3.1, docs/api.md "Server storage"): the site's
// settings, the per-space pin and exemption, eviction on request, and the
// storage blocks of the lists. The policy itself lives in internal/spaces.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// storageResp is what is not a setting of the server's storage: since when the
// requirement of a remote has had its present form, how many spaces it asks
// something of, and the Git the server runs. The policy (storage.*, cache.*) is
// under /api/admin/settings.
type storageResp struct {
	Remotes struct {
		Since *string `json:"since"`
	} `json:"remotes"`
	Git struct {
		Version      string `json:"version,omitempty"` // the site's only
		PartialClone bool   `json:"partialClone"`
	} `json:"git"`
	Affected struct {
		InGrace int `json:"inGrace"`
		Overdue int `json:"overdue"`
		Exempt  int `json:"exempt"`
	} `json:"affected"`
}

func (s *server) storageBody(set spaces.StorageSettings, aff spaces.Affected, site bool) storageResp {
	var r storageResp
	if !set.Remotes.Since.IsZero() {
		t := rfc3339(set.Remotes.Since)
		r.Remotes.Since = &t
	}
	version, partial := s.spaces.GitInfo()
	r.Git.PartialClone = partial
	if site {
		r.Git.Version = version
	}
	r.Affected.InGrace, r.Affected.Overdue, r.Affected.Exempt = aff.InGrace, aff.Overdue, aff.Exempt
	return r
}

// storageGetResp is the settings and, from 3.5, the summary of what the server
// holds, for the whole server or for one tenant.
type storageGetResp struct {
	storageResp
	Summary storageSummaryResp `json:"summary"`
}

// adminGetStorage answers the settings and the summary. A site administrator
// reads the server's (?tenant= narrows the summary); a tenant administrator the
// effective settings, read only, and the summary of their tenant. The summary is
// stored values only (docs/api.md, "GET /api/admin/storage").
func (s *server) adminGetStorage(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	var aff spaces.Affected
	if sc.All() {
		var err error
		if aff, err = s.spaces.Affected(c.r.Context()); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	sum, err := s.storageSummary(c, sc)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if !sc.All() {
		// A tenant sees its own spaces in their grace period or past it (the
		// exemptions are the site's to count).
		aff = spaces.Affected{InGrace: int(sum.Remotes.InGrace), Overdue: int(sum.Remotes.Overdue)}
	}
	writeJSON(c.w, http.StatusOK, storageGetResp{s.storageBody(s.spaces.Storage(), aff, c.can(authz.SiteStorageManage)), sum})
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
		IndexBytes: r.Storage.IndexBytes, Assets: "space", AssetCacheBytes: s.spaces.AssetBytes(r.ID),
		Remote: toNote(s.spaces.NoticeOfRow(r)),
	}
	if r.Storage.AssetsRepo {
		out.Assets = "repository"
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
	// A tenant's administrators reach their tenant's spaces, a site administrator any.
	sp, err := store.GetAdminSpace(ctx, s.opt.Store.Q(), c.adminReach(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
			return
		}
		s.fail(c.w, err)
		return
	}
	// The exemption from the requirement of a remote is the site's.
	if req.Exempt != nil {
		if e := s.allow(c, authz.SiteStorageManage, c.tenant()); e != nil {
			s.refuse(c.w, e)
			return
		}
	}
	if req.Pin != nil && *req.Pin && !sp.Storage.Pinned && !s.pinWithin(c, sp.ID, sp.TenantID) {
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
	row, err := store.GetAdminSpace(ctx, s.opt.Store.Q(), store.AllTenants(), id)
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
	assetTotal, assetBudget := s.spaces.AssetTotals()
	return map[string]any{
		"spaces":     map[string]int{"local": sums.Local, "full": sums.Full, "partial": sums.Partial, "evicted": sums.Evicted, "restoring": restoring},
		"cloneBytes": sums.CloneBytes, "indexBytes": sums.IndexBytes, "assetCacheBytes": assetTotal,
		"clonesBudget": set.Cache.ClonesBytes, "assetsBudget": assetBudget, "restoresWaiting": waiting,
		"remotes": map[string]int{"inGrace": aff.InGrace, "overdue": aff.Overdue},
	}, nil
}

// pinWithin says whether pinning the space keeps its tenant's pins within
// storage.pinnedBytes, answering 409 quota_exceeded (quota "pinnedBytes") when
// it does not: a tenant's administrators pin the disk the pinned spaces of the
// tenant take, at most that limit (0 by default: they cannot pin until the
// site allows it). A site administrator is not held to it. The limit is read
// from the settings' cache, the sums from two indexed reads; neither happens in
// a transaction.
func (s *server) pinWithin(c *call, id, tenant string) bool {
	if c.siteWide(authz.SiteStorageManage) || s.settings == nil {
		return true
	}
	ctx := c.r.Context()
	limit, _ := s.settings.LimitOf(ctx, tenant, settings.StoragePinnedBytes)
	q := s.opt.Store.Q()
	pinned, err := store.PinnedBytes(ctx, q, tenant, id)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	size, err := store.SpaceDiskBytes(ctx, q, id)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	used := pinned + size
	if limit > 0 && used <= limit {
		return true
	}
	writeJSON(c.w, http.StatusConflict, errorBody{Error: "quota_exceeded", Quota: "pinnedBytes", Limit: &limit, Used: &used,
		Message: "Pinning this space would keep more of the server's disk than this organisation may pin."})
	return false
}
