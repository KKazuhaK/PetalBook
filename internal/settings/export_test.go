package settings

// SetBeforeTenantWrite makes f run in each tenant administrator's write
// transaction, just before the write.
func (s *Service) SetBeforeTenantWrite(f func()) { s.beforeTenantWrite = f }

// SetOnCompute makes f run each time a tenant's settings are read from the
// database.
func (s *Service) SetOnCompute(f func(tenant string)) { s.onCompute = f }
