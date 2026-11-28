package settings

// SetBeforeTenantWrite makes f run in each tenant administrator's write
// transaction, just before the write.
func (s *Service) SetBeforeTenantWrite(f func()) { s.beforeTenantWrite = f }

// SetOnCompute makes f run each time a tenant's settings are read from the
// database.
func (s *Service) SetOnCompute(f func(tenant string)) { s.onCompute = f }

// SetAfterPlanRows makes f run each time a tenant's computation has read the
// plans' rules, before it uses them.
func (s *Service) SetAfterPlanRows(f func()) { s.afterPlanRows = f }

// SetBoundaryFault makes the timer's reads of the boundaries fail while f
// answers true.
func (s *Service) SetBoundaryFault(f func() bool) { s.boundaryFault = f }

// Looser is whether v is looser than above under d's strictness.
func Looser(d *Def, v, above any) bool { return d.looser(v, above) }

// HeldTo is what the layer below the site is held to and sits on.
func HeldTo(d *Def, site SiteView, rule *RuleView) (Lower, any, bool) { return d.heldTo(site, rule) }
