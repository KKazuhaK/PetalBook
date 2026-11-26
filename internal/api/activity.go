package api

import (
	"context"
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/spaces"
)

// The optional audit categories that write rows (internal/auditlog): page and
// file reads, downloads, and calls made with a token or through MCP. Each is
// switched on per tenant (the audit.reads, audit.downloads and audit.api
// settings). While a category is off, a request pays one read of the settings'
// cached snapshot and nothing else: no table, no wrapper, no queue. The rows
// name things by id (space, page, file path), never by title, so a reader sees
// names only as far as their own permissions let them resolve the ids. Nothing
// of a body or a secret is ever recorded.

// maxActivityAudit bounds the table that keeps reads and downloads to one row
// an hour for each person and thing.
const maxActivityAudit = 100000

// activityWanted says whether opening something in the space is recorded, and
// in which tenant: a personal space never is (its owner's reading is not the
// tenant administrators' business), nor is an administrator's view (it has its
// own core row), and for any other the tenant is the space's own, whoever reads
// it: the owner of the content decides what is recorded about it. While the
// category is off it costs one read of the settings' cached snapshot.
func (s *server) activityWanted(info *spaces.Info, own, kind string) (string, bool) {
	if s.auth == nil || info == nil || info.Personal || info.Viewing {
		return "", false
	}
	tenant := info.TenantID
	if tenant == "" {
		tenant = own
	}
	return tenant, s.auth.AuditWanted(tenant, kind)
}

// writeActivity notes that p opened or downloaded what (field: val) names in
// the space, at most once an hour for each person, kind and thing, and writes
// the row. Callers have asked activityWanted first, so the details are built
// only here.
func (s *server) writeActivity(ctx context.Context, p *auth.Principal, info *spaces.Info, tenant, ip, kind, field, val string) {
	if !s.activities.first(p.User.ID+"\x00"+info.ID+"\x00"+kind+"\x00"+field+":"+val, s.now()) {
		return
	}
	s.auth.AuditIn(ctx, auth.AuditEvent{Kind: kind, Actor: p.User.ID, User: p.User.ID, Tenant: tenant, IP: ip,
		Detail: map[string]any{field: val, "space": info.ID}})
}

// activity records that the caller opened or downloaded something (a page, or
// a file by its path) in the space they have open. Call it once the thing is
// found, never before: an id that names nothing is not a read.
func (s *server) activity(c *call, kind, field, val string) {
	if c.p == nil || c.sp == nil {
		return
	}
	if tenant, ok := s.activityWanted(&c.sp.Info, c.tenant(), kind); ok {
		s.writeActivity(c.r.Context(), c.p, &c.sp.Info, tenant, s.auth.ClientAddr(c.r), kind, field, val)
	}
}

// activity is the same for a tool of the MCP endpoint, so that both paths
// record the same.
func (c *mcpCall) activity(info *spaces.Info, kind, field, val string) {
	s := c.s
	if tenant, ok := s.activityWanted(info, c.tenant(), kind); ok {
		ip := ""
		if r := mcpRequestFrom(c.ctx); r != nil {
			ip = s.auth.ClientAddr(r)
		}
		s.writeActivity(c.ctx, c.p, info, tenant, ip, kind, field, val)
	}
}

// apiCalls starts the record of a request made with a token (or through MCP) if
// the person's tenant has the api category on (the tenant is passed: the acting
// scope is not set yet when a token is read). It returns the writer to answer
// with, and a function to call when the request is over; with the category off
// it returns w and nil.
func (s *server) apiCalls(w http.ResponseWriter, c *call, route, tenant string) (http.ResponseWriter, func()) {
	if s.auth == nil || !s.auth.AuditWanted(tenant, "api_call") {
		return w, nil
	}
	sw, ok := w.(*statusWriter)
	if !ok {
		sw = &statusWriter{ResponseWriter: w}
	}
	method := c.r.Method
	return sw, func() {
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		via := "token"
		if route == mcpPath {
			via = "mcp"
		}
		s.audit(c, auth.AuditEvent{Kind: "api_call", Actor: c.p.User.ID, User: c.p.User.ID, Tenant: tenant,
			Detail: map[string]any{"method": method, "route": route, "status": status, "via": via}})
	}
}
