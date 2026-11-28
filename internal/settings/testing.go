package settings

import "sync/atomic"

// ComputeCounter counts the times a tenant's (or the site's) settings are read
// from the database, rather than from the cache. Tests use it to check that a
// path asks the database nothing once the cache is warm.
func ComputeCounter(s *Service) *atomic.Int64 {
	var n atomic.Int64
	s.onCompute = func(string) { n.Add(1) }
	return &n
}
