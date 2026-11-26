package api

import sheets "petalbook.app/server/internal/limits"

// Boundary is what the limits sheet tells the server when a dated value starts
// or ends, for a test that has no clock the sheet's timer follows.
func (h *Handler) DropLimits() { h.s.limits = nil }

// Boundary is the same, as a server that could not read the sheet has none.
func (h *Handler) Boundary(tenant string, a sheets.Applied) { h.s.auditBoundary(tenant, a) }
