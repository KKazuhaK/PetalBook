package settings

import "petalbook.app/server/internal/auditlog"

// The audit log's retention (3.6 c): one table for every tenant, so site only,
// and all of it the site's to set: there is no floor. A row is kept while it is
// younger than AuditKeepDays (0: no age limit) OR (AuditKeepMode "or") / AND
// ("and") among the newest AuditKeepRows rows. A row of an optional category is
// also deleted after AuditOptionalKeepDays. A category with its own days
// (audit.keepDays.<category>: 0 keeps it forever, 1 or more deletes it after
// that many days, none follows the rules above) is kept by those days alone.
const (
	AuditKeepDays         = "audit.keepDays"
	AuditKeepRows         = "audit.keepRows"
	AuditKeepMode         = "audit.keepMode"
	AuditOptionalKeepDays = "audit.optionalKeepDays"

	// AuditDefaultKeepDays is the default of AuditKeepDays.
	AuditDefaultKeepDays = 90
	// AuditMaxKeepDays is the most any of the days settings takes (ten years).
	AuditMaxKeepDays = 3650
)

// AuditCategoryKeepDays is the key of a category's own days.
func AuditCategoryKeepDays(c auditlog.Category) string { return AuditKeepDays + "." + string(c) }

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
	defs = append(defs,
		&Def{Key: AuditKeepDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(AuditDefaultKeepDays), Min: 0, Max: AuditMaxKeepDays, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
		&Def{Key: AuditKeepRows, Kind: Limit, Layers: []Layer{Site}, Default: int64(100_000), Min: 1000, Max: 1_000_000_000, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
		&Def{Key: AuditKeepMode, Kind: Choice, Layers: []Layer{Site}, Default: "or", Choices: []string{"or", "and"}, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
		&Def{Key: AuditOptionalKeepDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(30), Min: 1, Max: AuditMaxKeepDays, Lower: Lower{Mode: Allow}, ServerWide: auditWhy},
	)
	for _, c := range auditlog.Categories() {
		defs = append(defs, &Def{Key: AuditCategoryKeepDays(c.ID), Kind: Limit, Layers: []Layer{Site}, Default: nil, Min: 0, Max: AuditMaxKeepDays, Nullable: true, Lower: Lower{Mode: Allow}, ServerWide: auditWhy})
	}
	return defs
}
