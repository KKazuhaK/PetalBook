package auth

// HashOps reports how many password hashes the service has started. Tests use
// it to check that a sign-in for an unknown user costs what any other does.
func HashOps(s *Service) int64 { return s.hasher.ops.Load() }

// HashPeak reports the most password hashes that ever ran at the same time.
func HashPeak(s *Service) int64 { return s.hasher.peak.Load() }
