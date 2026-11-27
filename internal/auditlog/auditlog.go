// Package auditlog is the one registry of the audit log's kinds: every kind a
// row may have is registered here once, with its category and who may see it.
// The writers (internal/auth's queue, internal/notify, the API handlers) name a
// kind as a string; a source-scan test fails when one is not registered here.
// The filters, the retention, the tenant administrators' list of hidden kinds
// and the web's kind list are all derived from this table.
package auditlog

import (
	"context"
	"sort"
)

// Category groups kinds. The core categories are always written; the optional
// ones have a setting each. How long each is kept is the site's to set
// (audit.keepDays and the like): there is no floor.
type Category string

const (
	// Core: no setting can turn them off.
	Security Category = "security" // sign-in failures, lockouts, passkeys, identities, ended sessions, security settings, providers, secrets
	Admin    Category = "admin"    // settings, rules, plans, people, invites, groups, roles, tenants, tokens and grants
	Data     Category = "data"     // deleting, restoring, purging, exporting, taking over and transferring spaces, space views
	System   Category = "system"   // rows lost, and the like

	// Optional: a setting each (Info.Setting), site and tenant layers.
	Signins   Category = "signins"   // successful sign-ins
	Reads     Category = "reads"     // a member opening a page or file
	Downloads Category = "downloads" // attachments and file downloads
	API       Category = "api"       // token and MCP calls
)

// Info describes a category.
type Info struct {
	ID Category
	// Core categories can never be turned off. An optional one is switched by
	// the boolean setting Setting (the layers of the settings registry), on
	// unless Default says otherwise.
	Core    bool
	Setting string
	Default bool
}

var categories = []Info{
	{ID: Security, Core: true},
	{ID: Admin, Core: true},
	{ID: Data, Core: true},
	{ID: System, Core: true},
	{ID: Signins, Setting: "audit.signins", Default: true},
	{ID: Reads, Setting: "audit.reads"},
	{ID: Downloads, Setting: "audit.downloads"},
	{ID: API, Setting: "audit.api"},
}

var categoryByID = func() map[Category]Info {
	m := make(map[Category]Info, len(categories))
	for _, c := range categories {
		m[c.ID] = c
	}
	return m
}()

// Categories are the categories in the order the pages show them: the core
// ones, then the optional ones.
func Categories() []Info { return append([]Info(nil), categories...) }

// CategoryInfo is the category id names.
func CategoryInfo(id Category) (Info, bool) { c, ok := categoryByID[id]; return c, ok }

// Visibility says who sees the rows of a kind.
type Visibility uint8

const (
	// Tenant: the administrators of the tenant a row belongs to see it, the
	// site administrators' own changes to that tenant included.
	Tenant Visibility = iota
	// Site: only the server's administrators see it (the server's own doings:
	// providers, mail, security, rate limits, lockouts, dropped rows).
	Site
)

// Kind is one registered kind of audit row.
type Kind struct {
	Name       string
	Category   Category
	Visibility Visibility
	// reserved: written by a later part; the scan does not require a writer.
	reserved bool
}

// Core says whether the kind's category is a core one.
func (k Kind) Core() bool { return categoryByID[k.Category].Core }

// Capacities a row may carry (the column is reserved for acting as a tenant's
// administrator): "" is none.
const (
	CapacitySiteAdmin   = "site_admin"
	CapacityTenantAdmin = "tenant_admin"
)

type capacityKey struct{}

// WithCapacity is ctx carrying the capacity a request acts in: every audit row
// written for the request records it (internal/api sets it once per request).
func WithCapacity(ctx context.Context, c string) context.Context {
	return context.WithValue(ctx, capacityKey{}, c)
}

// CapacityOf is the capacity ctx carries; "" for none.
func CapacityOf(ctx context.Context) string {
	c, _ := ctx.Value(capacityKey{}).(string)
	return c
}

// ValidCapacity says whether c is a capacity a filter may name.
func ValidCapacity(c string) bool {
	return c == CapacitySiteAdmin || c == CapacityTenantAdmin
}

var (
	byName   = map[string]*Kind{}
	ordered  []*Kind
	siteOnly []string
)

func init() {
	for _, k := range registered {
		k := k
		if _, dup := byName[k.Name]; dup {
			panic("auditlog: kind registered twice: " + k.Name)
		}
		if _, ok := categoryByID[k.Category]; !ok {
			panic("auditlog: kind " + k.Name + " has no such category " + string(k.Category))
		}
		byName[k.Name] = &k
		ordered = append(ordered, &k)
		if k.Visibility == Site {
			siteOnly = append(siteOnly, k.Name)
		}
	}
	sort.Strings(siteOnly)
}

// Lookup is the registered kind called name.
func Lookup(name string) (Kind, bool) {
	k, ok := byName[name]
	if !ok {
		return Kind{}, false
	}
	return *k, true
}

// Kinds are every registered kind, by category (the order of Categories) and
// then by name.
func Kinds() []Kind {
	out := make([]Kind, len(ordered))
	for i, k := range ordered {
		out[i] = *k
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := catIndex(out[i].Category), catIndex(out[j].Category)
		if ci != cj {
			return ci < cj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func catIndex(c Category) int {
	for i, x := range categories {
		if x.ID == c {
			return i
		}
	}
	return len(categories)
}

// SiteOnlyKinds are the kinds a tenant administrator does not see, by name.
// The slice is shared: do not change it.
func SiteOnlyKinds() []string { return siteOnly }

// CategoryOf is the category a row of kind is filed under. A kind that is not
// registered (a row written by a build that had it) is filed under admin.
func CategoryOf(kind string) Category {
	if k, ok := byName[kind]; ok {
		return k.Category
	}
	return Admin
}

// Reserved says whether kind is registered for a later part and has no writer
// yet.
func Reserved(kind string) bool {
	k, ok := byName[kind]
	return ok && k.reserved
}
