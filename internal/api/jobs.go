package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// Jobs (docs/api.md, "Jobs"): every background action is a row of jobs, and one
// shape answers for each kind. What a person may follow is what their keys let
// them do: bulk actions with tenant.bulk.run, exports with tenant.data.export,
// the actions of the recycle bin with tenant.recycle.read.

type jobResp struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	// Action is a bulk job's action.
	Action     string             `json:"action,omitempty"`
	State      string             `json:"state"`
	Total      int64              `json:"total"`
	Done       int64              `json:"done"`
	BytesTotal int64              `json:"bytesTotal"`
	BytesDone  int64              `json:"bytesDone"`
	Failed     []store.JobFailure `json:"failed"`
	Error      string             `json:"error"`
	Created    string             `json:"created"`
	Updated    string             `json:"updated"`
	// Expires is when an export's file goes (null for any other job).
	Expires *string        `json:"expires"`
	MadeBy  recycleUserRef `json:"madeBy"`
}

func jobOf(j *store.Job) jobResp {
	f := j.Failed
	if f == nil {
		f = []store.JobFailure{}
	}
	out := jobResp{ID: j.ID, Kind: j.Kind, Subject: j.Subject, State: j.State, Total: j.Total, Done: j.Done, BytesTotal: j.BytesTotal, BytesDone: j.BytesDone,
		Failed: f, Error: j.Error, Created: rfc3339(j.Created), Updated: rfc3339(j.Updated), MadeBy: recycleUserRef{j.MadeBy, j.MadeByName}}
	if j.Kind == bulkKindName {
		out.Action = bulkParamsOf(j).Action
	}
	if !j.Expires.IsZero() {
		e := rfc3339(j.Expires)
		out.Expires = &e
	}
	return out
}

// jobKeyOf is the key that lets a person follow the jobs of a kind. The jobs of
// a personal space's item (its move, restore and purge) are site_only: a
// tenant's administrator never reaches them (spaces/recycle.go).
func jobKeyOf(kind string) authz.Key {
	switch kind {
	case store.JobExport:
		return authz.TenantDataExport
	case store.JobRecycleMove, store.JobRecycleRestore, store.JobRecyclePurge:
		return authz.TenantRecycleRead
	case store.JobAuditPurge:
		return authz.SiteAuditPurge
	}
	return authz.TenantBulkRun
}

// jobKinds are the kinds a person may follow, in a list.
var jobKinds = []string{bulkKindName, store.JobExport, store.JobRecycleMove, store.JobRecycleRestore, store.JobRecyclePurge, store.JobAuditPurge}

func (s *server) adminGetJob(c *call) {
	j, err := s.jobs.Get(c.r.Context(), c.reach(), c.r.PathValue("id"))
	if err == nil && !c.can(jobKeyOf(j.Kind)) {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such job.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Job jobResp `json:"job"`
	}{jobOf(j)})
}

// adminListJobs is a page of the jobs the caller may follow, newest first:
// ?kind= and ?state= narrow it, ?cursor= continues it, ?tenant= narrows a site
// administrator's list to one tenant. A job that runs on this server is read
// from memory (its progress as it is now), the others from their rows.
func (s *server) adminListJobs(c *call) {
	q := c.r.URL.Query()
	limit := defaultAdminLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAdminLimit {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit is a number from 1 to "+strconv.Itoa(maxAdminLimit)+".")
			return
		}
		limit = n
	}
	var kinds []string
	if k := q.Get("kind"); k != "" {
		known := false
		for _, kk := range jobKinds {
			known = known || kk == k
		}
		if !known {
			writeError(c.w, http.StatusBadRequest, "invalid", "Kind is bulk, export, recycle.move, recycle.restore, recycle.purge or audit.purge.")
			return
		}
		kinds = []string{k}
	} else {
		for _, k := range jobKinds {
			if c.can(jobKeyOf(k)) {
				kinds = append(kinds, k)
			}
		}
	}
	if len(kinds) == 1 && !c.can(jobKeyOf(kinds[0])) {
		kinds = nil
	}
	state := q.Get("state")
	switch state {
	case "", store.JobQueued, store.JobRunning, store.JobDone, store.JobFailed, store.JobInterrupted, store.JobCancelled:
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "State is queued, running, done, failed, interrupted or cancelled.")
		return
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	var rows []*store.Job
	var next string
	if len(kinds) > 0 {
		var err error
		rows, next, err = store.ListJobs(c.r.Context(), s.opt.Store.Q(), sc, store.JobFilter{Kinds: kinds, State: state, After: q.Get("cursor"), Limit: limit})
		if errors.Is(err, store.ErrBadCursor) {
			writeError(c.w, http.StatusBadRequest, "invalid", "That cursor is not one this list gave.")
			return
		}
		if err != nil {
			s.fail(c.w, err)
			return
		}
	}
	items := make([]jobResp, len(rows))
	for i, j := range rows {
		if j.State == store.JobRunning {
			// Running here: the progress in memory is ahead of the row.
			if live, err := s.jobs.Get(c.r.Context(), sc, j.ID); err == nil {
				j = live
			}
		}
		items[i] = jobOf(j)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []jobResp `json:"items"`
		Next  string    `json:"next"`
	}{items, next})
}

// jobLimit is a limit of the jobs runner as the settings have it now (the
// settings cache, no query once warm): the default when it cannot be read.
func (s *server) jobLimit(tenant, key string, def int) int {
	e, err := s.settings.Get(context.Background(), tenant, key)
	if err != nil {
		return def
	}
	if n, ok := e.Value.(int64); ok && n > 0 {
		return int(n)
	}
	return def
}
