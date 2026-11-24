package settings

import (
	"net/url"
	"strings"
)

// Keys of the settings of 3.3.
const (
	RegistrationOpen    = "registration.open"
	RegistrationDomains = "registration.domains"
	RegistrationPerHour = "registration.perHour"
	LegalTermsURL       = "legal.termsUrl"
	LegalPrivacyURL     = "legal.privacyUrl"
	DailyOwn            = "daily.own"
	DailyAPI            = "daily.api"
	DailyDefault        = "daily.default"
	// OfflineCopies is whether a person may keep offline copies of spaces on a
	// device (4.1): the four sync endpoints answer 403 offline_off when it is off.
	OfflineCopies = "offline.copies"

	// FilesMaxBytes is the largest file a write may create or replace (3.5):
	// site and tenant. The site's value is --max-upload and its lower rule
	// restricts a tenant to at most that (see Service.SetFileCeiling).
	FilesMaxBytes = "files.maxBytes"

	// RecycleDays is how long an item stays in the recycle bin (3.5); ExportsDays
	// and ExportsMaxBytes how long an export is kept and how much room all of them
	// may take. Site only: they use the server's disk.
	RecycleDays     = "recycle.days"
	ExportsDays     = "exports.days"
	ExportsMaxBytes = "exports.maxBytes"
	// UsersReuseUsernames is whether a username a purge freed may be taken again:
	// "after_purge" (anyone may, once the person is purged) or "never". Site only:
	// usernames are unique on the whole server.
	UsersReuseUsernames = "users.reuseUsernames"

	QuotaSpacesPerPerson = "quota.spacesPerPerson"
	QuotaBytesPerPerson  = "quota.bytesPerPerson"
	QuotaPagesPerPerson  = "quota.pagesPerPerson"
)

// Definitions are the settings that exist, in the order the pages show them.
//
// Reserved, not defined here yet: 3.1's storage.remotes.require (choice,
// ordered null < any < a provider's id), storage.remotes.graceDays and
// storage.localSpaces.maxBytes (limits) join with layers site and tenant and a
// site Lower of lock, and a tenant may only tighten them. Until then they are
// the site's own record (internal/spaces, /api/admin/storage), as are 3.1's
// hosts (what --remote-host sets) and cache settings, which stay site-only. 2.7's GitHub App settings have no tenant layer
// and never appear here. The person layer of the quota.* limits is an
// administrator's override on the user (users.quota), read by internal/quota,
// not something a person sets for themselves.
func Definitions() []*Def {
	return []*Def{
		{Key: RegistrationOpen, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: false, Lower: Lower{Mode: Lock}},
		{Key: RegistrationDomains, Kind: List, Layers: []Layer{Site, Tenant}, Default: []string{}, Lower: Lower{Mode: Allow}, Check: checkDomains},
		{Key: RegistrationPerHour, Kind: Limit, Layers: []Layer{Site, Tenant}, Default: int64(50), Min: 1, Max: 10000,
			Lower: Lower{Mode: Restrict, Max: i64(50)}},
		{Key: LegalTermsURL, Kind: Text, Layers: []Layer{Site, Tenant}, Default: "", Lower: Lower{Mode: Allow}, Check: checkHTTPS},
		{Key: LegalPrivacyURL, Kind: Text, Layers: []Layer{Site, Tenant}, Default: "", Lower: Lower{Mode: Allow}, Check: checkHTTPS},
		// What a space may do with the daily text (2.3's allow switches); the
		// site's value lives in the daily text's own record (see Service.Bind).
		{Key: DailyOwn, Kind: Bool, Layers: []Layer{Site, Tenant, Space}, Default: true, Lower: Lower{Mode: Allow}},
		{Key: DailyAPI, Kind: Bool, Layers: []Layer{Site, Tenant, Space}, Default: true, Lower: Lower{Mode: Allow}},
		// The default of the daily text: the shape of /api/admin/daily's default;
		// the tenant's comes between the space's and the site's. Validated by
		// the daily text's code (see Service.BindCheck).
		{Key: DailyDefault, Kind: Doc, Layers: []Layer{Site, Tenant}, Default: nil, Lower: Lower{Mode: Allow}},
		// Offline copies (4.1): allowed unless the site or the tenant turns them
		// off. No space or person layer: a person who is not allowed has none, and
		// one who is allowed chooses by which spaces they keep on the device.
		{Key: OfflineCopies, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: true, Lower: Lower{Mode: Allow}},
		// The size of one file (3.5). The default and the site's lower rule are the
		// site's --max-upload, which the server sets (SetFileCeiling).
		{Key: FilesMaxBytes, Kind: Limit, Layers: []Layer{Site, Tenant}, Default: int64(10 << 20), Min: 1, Max: 256 << 20,
			Lower: Lower{Mode: Restrict, Max: i64(10 << 20)}},
		{Key: RecycleDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(30), Min: 1, Max: 365, Lower: Lower{Mode: Allow}},
		{Key: ExportsDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(7), Min: 1, Max: 365, Lower: Lower{Mode: Allow}},
		{Key: ExportsMaxBytes, Kind: Limit, Layers: []Layer{Site}, Default: int64(20 << 30), Min: 1 << 20, Max: 1 << 50, Lower: Lower{Mode: Allow}},
		{Key: UsersReuseUsernames, Kind: Choice, Layers: []Layer{Site}, Default: "after_purge", Choices: []string{"after_purge", "never"}, Lower: Lower{Mode: Allow}},
		// What one person may use. Only the number of spaces is refused (0 is no
		// limit, as --max-spaces 0 was); bytes and pages warn. null is no limit.
		{Key: QuotaSpacesPerPerson, Kind: Limit, Layers: []Layer{Site, Tenant, Person}, Default: int64(20), Min: 0, Max: 10000, Lower: Lower{Mode: Allow}},
		{Key: QuotaBytesPerPerson, Kind: Limit, Layers: []Layer{Site, Tenant, Person}, Default: nil, Min: 1, Max: 1 << 50, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: QuotaPagesPerPerson, Kind: Limit, Layers: []Layer{Site, Tenant, Person}, Default: nil, Min: 1, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
	}
}

func i64(n int64) *int64 { return &n }

func checkHTTPS(v any) (any, error) {
	s := strings.TrimSpace(v.(string))
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(s) > 500 || u.User != nil {
		return nil, bad("an https address, or \"\"")
	}
	return s, nil
}

func checkDomains(v any) (any, error) {
	in := v.([]string)
	if len(in) > 200 {
		return nil, bad("at most 200 domains")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "@")))
		if d == "" || len(d) > 253 || !strings.Contains(d, ".") || strings.ContainsAny(d, " @/:") {
			return nil, bad("%q is not a domain", d)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out, nil
}
