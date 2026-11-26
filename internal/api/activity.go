package api

import (
	"net/http"

	"petalbook.app/server/internal/auth"
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

// activityOn says whether the caller's tenant has the kind's category on.
func (s *server) activityOn(c *call, kind string) bool {
	return s.auth != nil && c.p != nil && s.auth.AuditWanted(c.tenant(), kind)
}

// activity records that the caller opened or downloaded something in the space
// they have open, at most once an hour for each person, kind and thing. A
// personal space is never recorded (its owner's reading is not the tenant
// administrators' business), and an administrator's view has its own core row.
func (s *server) activity(c *call, kind, what string, detail map[string]any) {
	if c.sp == nil || c.sp.Info.Personal || c.sp.Info.Viewing || !s.activityOn(c, kind) {
		return
	}
	if !s.activities.first(c.p.User.ID+"\x00"+c.sp.Info.ID+"\x00"+kind+"\x00"+what, s.now()) {
		return
	}
	detail["space"] = c.sp.Info.ID
	s.audit(c, auth.AuditEvent{Kind: kind, Actor: c.p.User.ID, User: c.p.User.ID, Tenant: c.tenant(), Detail: detail})
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
