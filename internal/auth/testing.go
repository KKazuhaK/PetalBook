package auth

// HashOps reports how many password hashes the service has started. Tests use
// it to check that a sign-in for an unknown user costs what any other does.
func HashOps(s *Service) int64 { return s.hasher.ops.Load() }

// HashPeak reports the most password hashes that ever ran at the same time.
func HashPeak(s *Service) int64 { return s.hasher.peak.Load() }

// CaptchaSolution reads the answer of an image challenge, without using it up
// (tests cannot read the picture).
func CaptchaSolution(s *Service, id string) string { return s.capStore.Get(id, false) }

// CaptchaHeld is how many image challenges are held in memory.
func CaptchaHeld(s *Service) int { return s.capStore.Len() }
