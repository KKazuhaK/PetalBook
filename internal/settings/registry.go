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
)

// Definitions are the settings that exist, in the order the pages show them.
//
// Reserved, not defined here because their code is on other branches or in a
// later part of 3.3: 3.1's storage.remotes.require (choice, ordered null <
// any < github), storage.remotes.graceDays and storage.localSpaces.maxBytes
// (limits) join with layers site and tenant and a site Lower of lock, and a
// tenant may only tighten them; the quota.* limits of the next part (spaces,
// bytes and pages per person, with a person layer). 2.7's GitHub App settings
// have no tenant layer and never appear here.
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
