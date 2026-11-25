package settings

// SetBeforeTenantWrite makes f run in each tenant administrator's write
// transaction, just before the write.
func (s *Service) SetBeforeTenantWrite(f func()) { s.beforeTenantWrite = f }
