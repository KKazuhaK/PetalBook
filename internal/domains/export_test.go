package domains

// The faults the tests inject: one that fails the read of the due domains,
// and one that fails the lapse's transaction after the claim is written.

func (s *Service) SetDueFault(f func() error)   { s.dueFault = f }
func (s *Service) SetLapseFault(f func() error) { s.lapseFault = f }
