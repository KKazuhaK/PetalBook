package api

// SetAuditDownloadMax changes the most rows one audit download holds, and
// returns what it was.
func SetAuditDownloadMax(n int) int { old := auditDownloadMax; auditDownloadMax = n; return old }

// HoldAuditDownload makes the user's audit download count as running, until
// the returned function is called.
func HoldAuditDownload(userID string) func() {
	auditDownloads.Store(userID, struct{}{})
	return func() { auditDownloads.Delete(userID) }
}
