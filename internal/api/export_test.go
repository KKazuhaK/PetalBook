package api

// HeaderPurpose is what a stored header value is bound to, for tests that
// write settings as an earlier version saved them.
var HeaderPurpose = headerPurpose

// SetSyncPagesBytes changes how much of the pages one answer of sync/pages
// holds, and returns what it was.
func SetSyncPagesBytes(n int) int { old := syncPagesBytes; syncPagesBytes = n; return old }
