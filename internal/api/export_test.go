package api

import "net/http"

// NewWithMCPStandIn is New with the routes of /api taking OAuth access tokens
// the way /mcp will, so the permission rules of a grant are tested on the real
// routes.
func NewWithMCPStandIn(opt Options) http.Handler { return build(opt, true) }
