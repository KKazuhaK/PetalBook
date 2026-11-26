package settings

import "petalbook.app/server/internal/auditlog"

// The audit log's retention (3.6 c): one table for every tenant, so site only.
// A row is kept while it is younger than AuditKeepDays OR (AuditKeepMode "or")
// / AND ("and") among the newest AuditKeepRows rows; a row of a core category
// is never deleted before auditlog.CoreFloorDays, so AuditKeepDays cannot go
// below it. A row of an optional category is also deleted after
// AuditOptionalKeepDays.
const (
	AuditKeepDays         = "audit.keepDays"
	AuditKeepRows         = "audit.keepRows"
	AuditKeepMode         = "audit.keepMode"
	AuditOptionalKeepDays = "audit.optionalKeepDays"
)

const auditWhy = "the audit log is one table for every tenant"

// Definitions are the settings that exist, in the order the pages show them:
// baseDefinitions, then the audit log's. The switch of each optional audit
// category (audit.signins, audit.reads, audit.downloads, audit.api) is made
// from the registry of kinds, so a core category has none: nothing can turn
// it off.
func Definitions() []*Def {
	defs := baseDefinitions()
	for _, c := range auditlog.Categories() {
		if c.Core {
			continue
		}
		defs = append(defs, &Def{Key: c.Setting, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: c.Default, Lower: Lower{Mode: Allow}})
	}
	return append(defs,
		&Def{Key: AuditKeepDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(auditlog.CoreFloorDays), Min: auditlog.CoreFloorDays, Max: 3650, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
		&Def{Key: AuditKeepRows, Kind: Limit, Layers: []Layer{Site}, Default: int64(100_000), Min: 1000, Max: 1_000_000_000, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
		&Def{Key: AuditKeepMode, Kind: Choice, Layers: []Layer{Site}, Default: "or", Choices: []string{"or", "and"}, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
		&Def{Key: AuditOptionalKeepDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(30), Min: 1, Max: auditlog.CoreFloorDays, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
	)
}
