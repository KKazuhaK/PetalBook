package settings

import (
	"net/url"
	"regexp"
	"strings"
)

// Keys of the settings of 3.3.
const (
	RegistrationOpen    = "features.registration"
	RegistrationDomains = "registration.domains"
	RegistrationPerHour = "registration.perHour"
	LegalTermsURL       = "legal.termsUrl"
	LegalPrivacyURL     = "legal.privacyUrl"
	DailyOwn            = "daily.own"
	DailyAPI            = "daily.api"
	DailyDefault        = "daily.default"
	// OfflineCopies is whether a person may keep offline copies of spaces on a
	// device (4.1): the four sync endpoints answer 403 feature_off (features.offline) when it is off.
	OfflineCopies = "features.offline"

	// FilesMaxBytes is the largest file a write may create or replace (3.5):
	// site and tenant. The site's value is --max-upload and its lower rule
	// restricts a tenant to at most that (see Service.SetFileCeiling).
	FilesMaxBytes = "files.maxBytes"

	// RecycleDays is how long an item stays in the recycle bin (3.5); ExportsDays
	// and ExportsMaxBytes how long an export is kept and how much room all of them
	// may take. Site only: they use the server's disk. ExportsDays may be forced
	// for one tenant (an export's expiry is set for the tenant that made it);
	// RecycleDays and ExportsMaxBytes are server-wide.
	RecycleDays     = "recycle.days"
	ExportsDays     = "exports.days"
	ExportsMaxBytes = "exports.maxBytes"
	// JobsMaxRunning is how many bulk actions, exports and moves run at once on
	// the server; JobsPerTenant how many for one organisation. Site only (no
	// tenant layer, as quota.*): a site rule for one tenant, or its plan, may
	// raise or cap its own. Read at each dispatch of the jobs runner.
	JobsMaxRunning = "jobs.maxRunning"
	JobsPerTenant  = "jobs.perTenant"
	// UsersReuseUsernames is whether a username a purge freed may be taken again:
	// "after_purge" (anyone may, once the person is purged) or "never". Site only:
	// usernames are unique on the whole server.
	UsersReuseUsernames = "users.reuseUsernames"
	// UsersEmailNonASCII is whether an address with characters outside ASCII
	// (an internationalised address: a Unicode local part, an IDN domain) may
	// be set (3.6). Site only: addresses are unique on the whole server. Off,
	// such an address is refused (400 email_not_ascii); on, it is compared in
	// store.EmailKey form. Mail to one needs a relay with SMTPUTF8.
	UsersEmailNonASCII = "users.emailNonAscii"
	// AccountsMultipleEmails is whether a person may have more than one address
	// on their account (3.6): add one (it counts once its link is followed),
	// make a verified one primary, remove one that is not. Off by default; the
	// site allows, restricts or locks it and a tenant decides inside. Off
	// again, the extra addresses are kept but neither shown nor used to sign
	// in. A feature: the routes that add, verify, make primary and remove
	// another address belong to it.
	AccountsMultipleEmails = "accounts.multipleEmails"

	// StorageAssets is where the attachments of a new space on a provider go:
	// "space" (its own repository) or "repository" (<name>-assets). Site and
	// tenant; the space's owner chooses inside what is left (see AssetsRule).
	StorageAssets = "storage.assets"
	// StorageAssetsOwnExempt is whether a tenant that stores on its own target is
	// left out of the site's StorageAssets default and lock. Site only; a site
	// administrator may force it for one tenant.
	StorageAssetsOwnExempt = "storage.assets.ownStorageExempt"

	// The features: whole things a tenant may or may not have. A site
	// administrator turns one off for one tenant with a rule (value false,
	// locked), or a plan does; a feature is on unless a rule says otherwise
	// (MailOwnSender: off unless).
	FeatureOwnSSO          = "features.ownSso"
	FeatureDomains         = "features.domains"
	FeatureManagedAccounts = "features.managedAccounts"
	FeatureIdPAdmins       = "features.idpAdmins"
	FeatureAdminView       = "features.adminView"
	FeatureAuditDownload   = "features.auditDownload"
	// FeatureTenantAudit is whether a tenant's administrators read the
	// tenant's audit log (the tenant console's audit tab and its routes). On
	// unless a rule says otherwise; a tenant made after the site's
	// TenantsNewAuditDefault was set gets a locked rule from it (StampNewTenant),
	// and a site administrator changes it for one tenant by hand. The rows are
	// written either way; the site audit is not bound by it.
	FeatureTenantAudit    = "features.tenantAudit"
	MailOwnSender         = "mail.ownSender"
	FeatureOwnCaptcha     = "features.ownCaptcha"
	FeatureOwnConnections = "features.ownConnections"
	FeatureOwnDaily       = "features.ownDaily"
	// FeatureGitHubConnect is whether people and organisations may install the
	// site's GitHub App on their own GitHub account and sync with it: a site
	// switch, turned off for one tenant by a rule.
	FeatureGitHubConnect = "features.githubConnect"

	// StorageMode is where a tenant's new spaces may sync: on the server's
	// storage, on a target of the person's own, or on a target of the tenant's.
	StorageMode = "storage.mode"
	// StoragePinnedBytes is what the tenant's administrators may pin.
	StoragePinnedBytes = "storage.pinnedBytes"

	// What a tenant may use in all: set for a tenant by a site rule or its
	// plan, never by the tenant (no tenant layer); null is no limit.
	// QuotaPeople is counted and shown, not refused.
	QuotaPeople = "quota.people"
	QuotaSpaces = "quota.spaces"
	QuotaBytes  = "quota.bytes"
	QuotaPages  = "quota.pages"

	// What a tenant may ask of the server: requests per minute with a token and
	// with MCP, mail a day, outgoing calls a minute. null is no limit (for the
	// outgoing calls, the site's own outbound.tenantPerMinute).
	RateAPIPerMinute      = "rate.apiPerMinute"
	RateMCPPerMinute      = "rate.mcpPerMinute"
	RateMailPerDay        = "rate.mailPerDay"
	RateOutboundPerMinute = "rate.outboundPerMinute"

	// Machine access: whether people may make API tokens, and whether MCP and the
	// authorization server for it answer. Site and tenant: the site switches one
	// off for everybody (a lock) or for one tenant (a rule), a tenant for itself.
	FeatureTokens = "features.tokens"
	FeatureMCP    = "features.mcp"
	// FeatureTenancy is whether tenants besides the default one may exist.
	FeatureTenancy = "features.tenancy"
	// FeaturePasskeys is whether passkeys may be used to sign in.
	FeaturePasskeys = "features.passkeys"

	// TenantsNewAuditDefault is the value of FeatureTenantAudit a tenant is
	// made with (off by default). Site level only: it is read once, when the
	// tenant is made, and a later change touches no tenant that exists.
	TenantsNewAuditDefault = "tenants.newAuditDefault"

	// Organisations people create themselves (docs/design/3-6-c.md, 2): the
	// switch, how many one person may make, an optional cap per hour for the
	// whole server, and the plan a new one starts with (a plan's slug, "" for
	// none). Site level only: the tenant does not exist yet, and making one takes
	// the server's resources.
	FeatureOrgCreation   = "features.orgCreation"
	OrgCreationPerPerson = "orgCreation.perPerson"
	OrgCreationPerHour   = "orgCreation.perHour"
	OrgCreationPlan      = "orgCreation.plan"
	orgCreationWhy       = "a tenant does not exist yet; creating one takes the site's resources"

	// DomainsChecksPerMinute is how many checks of a claimed email domain's
	// DNS record people may ask for in a minute on the whole server ("Check
	// now"; each tenant has its own hour and each domain its own minute too).
	DomainsChecksPerMinute = "domains.checksPerMinute"

	// SigninSocial is which of the site's social sign-in providers the people
	// of a tenant may use (3.6 d3): their slugs, or ["*"] for every one. Site
	// and tenant: the site's value is the default, and its lower rule (or a
	// lock in a rule for one tenant) says whether a tenant prunes it. It is
	// read after a sign-in has found its account: an identity of a provider
	// the account's tenant does not allow is refused (sso_error=not_allowed).
	SigninSocial = "signin.social"

	// The sign-in policy, the same for every tenant: the sign-in page comes
	// before any tenant is known.
	SigninMode           = "signin.mode" // dual, sso_first, sso_redirect or local_only
	SigninSSOOnly        = "signin.ssoOnly"
	SigninPasskeyRoles   = "signin.passkeyRoles" // the roles that must hold a passkey: admin, member
	SigninLockoutEnabled = "signin.lockout.enabled"
	SigninLockoutMinutes = "signin.lockout.minutes"
	SigninLockoutScope   = "signin.lockout.scope" // ip_account, ip or account
	SigninPerMinute      = "signin.perMinute"
	SigninMaxFailures    = "signin.maxFailures"
	SigninWindowMinutes  = "signin.windowMinutes"
	SessionsIdleDays     = "sessions.idleDays"
	SessionsMaxDays      = "sessions.maxDays"
	CaptchaProvider      = "captcha.provider" // image, turnstile, recaptcha or hcaptcha
	CaptchaContexts      = "captcha.contexts" // {login, invite, setup, forgot, register: {on, when, failures}}

	// The server's storage policy (3.1): the requirement of a remote, the limit
	// on spaces that have none, the hosts a remote may be on, and the cache of
	// clones and attachments on the server's own disk.
	StorageRemotesRequire   = "storage.remotes.require" // "" (none), "any" or a provider's id
	StorageRemotesScope     = "storage.remotes.scope"   // new or all
	StorageRemotesGraceDays = "storage.remotes.graceDays"
	StorageLocalMaxBytes    = "storage.localSpaces.maxBytes"
	StorageHosts            = "storage.hosts"
	CachePartial            = "cache.partial"
	CacheEvictAfterDays     = "cache.evictAfterDays"
	CacheMinIdleHours       = "cache.minIdleHours"
	CacheClonesBytes        = "cache.clonesBytes"
	CacheMinFreeBytes       = "cache.minFreeBytes"
	CacheAssetsBytes        = "cache.assetsBytes"

	// DailyLanguages are the languages the daily text's settings pages have a
	// tab for, besides all languages.
	DailyLanguages = "daily.languages"

	QuotaSpacesPerPerson = "quota.spacesPerPerson"
	QuotaBytesPerPerson  = "quota.bytesPerPerson"
	QuotaPagesPerPerson  = "quota.pagesPerPerson"
)

// baseDefinitions are most of the settings that exist, in the order the pages
// show them (Definitions adds the audit log's, see registry_audit.go).
//
// The storage policy is server-wide for now: the requirement of a remote is
// worked out by queries over every space (store.RemotePolicy), so a tenant's
// own tightening of it is later work. The GitHub App's credentials and the
// captcha's keys are not settings (records and secrets). The person layer of
// the quota.* limits is an administrator's override on the user (users.quota),
// read by internal/quota, not something a person sets for themselves.
func baseDefinitions() []*Def {
	return []*Def{
		{Key: RegistrationOpen, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: false, Lower: Lower{Mode: Lock}},
		{Key: RegistrationDomains, Kind: List, Layers: []Layer{Site, Tenant}, Default: []string{}, Lower: Lower{Mode: Allow}, Check: checkDomains},
		{Key: RegistrationPerHour, Kind: Limit, Layers: []Layer{Site, Tenant}, Default: int64(50), Min: 1, Max: 10000,
			Lower: Lower{Mode: Restrict, Max: i64(50)}},
		{Key: LegalTermsURL, Kind: Text, Layers: []Layer{Site, Tenant}, Default: "", Lower: Lower{Mode: Allow}, Check: checkHTTPS},
		{Key: LegalPrivacyURL, Kind: Text, Layers: []Layer{Site, Tenant}, Default: "", Lower: Lower{Mode: Allow}, Check: checkHTTPS},
		// What a space may do with the daily text (2.3's allow switches).
		{Key: DailyOwn, Kind: Bool, Layers: []Layer{Site, Tenant, Space}, Default: true, Lower: Lower{Mode: Allow}},
		{Key: DailyAPI, Kind: Bool, Layers: []Layer{Site, Tenant, Space}, Default: true, Lower: Lower{Mode: Allow}},
		// The default of the daily text: the shape of /api/admin/daily's default;
		// the tenant's comes between the space's and the site's. Validated by
		// the daily text's code (see Service.Bind, Check).
		{Key: DailyDefault, Kind: Doc, Layers: []Layer{Site, Tenant}, Default: nil, Lower: Lower{Mode: Allow}},
		// Offline copies (4.1): allowed unless the site or the tenant turns them
		// off. No space or person layer: a person who is not allowed has none, and
		// one who is allowed chooses by which spaces they keep on the device.
		{Key: OfflineCopies, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: true, Lower: Lower{Mode: Allow}},
		// The size of one file (3.5). The default and the site's lower rule are the
		// site's --max-upload, which the server sets (SetFileCeiling).
		{Key: FilesMaxBytes, Kind: Limit, Layers: []Layer{Site, Tenant}, Default: int64(10 << 20), Min: 1, Max: 256 << 20,
			Lower: Lower{Mode: Restrict, Max: i64(10 << 20)}},
		// Attachments are separate by default. The space layer is the owner's
		// request when the space is made, read by AssetsRule, not a stored
		// setting. A site administrator may also lock the value for one tenant.
		{Key: StorageAssets, Kind: Choice, Layers: []Layer{Site, Tenant}, Default: "repository", Choices: []string{"space", "repository"}, Lower: Lower{Mode: Allow}},
		{Key: StorageAssetsOwnExempt, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		// The same for every tenant: one purge job keeps every tenant's items on
		// the server's disk for the same number of days.
		{Key: RecycleDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(30), Min: 1, Max: 365, Lower: Lower{Mode: Allow},
			ServerWide: "one purge job keeps every tenant's recycled items on the server's disk"},
		// How long one export is kept is set when it is made, for the tenant that
		// asked: a site administrator may force it for one tenant. The room of all
		// exports together is the server's disk.
		{Key: ExportsDays, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: int64(7), Min: 1, Max: 365, Lower: Lower{Mode: Allow}},
		{Key: ExportsMaxBytes, Kind: Limit, Layers: []Layer{Site}, Default: int64(20 << 30), Min: 1 << 20, Max: 1 << 50, Lower: Lower{Mode: Allow},
			ServerWide: "the room all exports take together on the server's disk"},
		{Key: JobsMaxRunning, Kind: Limit, Layers: []Layer{Site}, Default: int64(2), Min: 1, Max: 64, Lower: Lower{Mode: Allow},
			ServerWide: "the jobs run on the server's own cores and disk"},
		{Key: JobsPerTenant, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: int64(1), Min: 1, Max: 64, Lower: Lower{Mode: Allow}},
		{Key: UsersEmailNonASCII, Kind: Bool, Layers: []Layer{Site}, Default: false, Lower: Lower{Mode: Allow},
			ServerWide: "addresses are unique on the whole server"},
		{Key: AccountsMultipleEmails, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: false, Lower: Lower{Mode: Allow}},
		{Key: UsersReuseUsernames, Kind: Choice, Layers: []Layer{Site}, Default: "after_purge", Choices: []string{"after_purge", "never"}, Lower: Lower{Mode: Allow},
			ServerWide: "usernames are unique on the whole server"},
		// What one person may use. Only the number of spaces is refused (0 is no
		// limit, as --max-spaces 0 was); bytes and pages warn. null is no limit.
		{Key: QuotaSpacesPerPerson, Kind: Limit, Layers: []Layer{Site, Tenant, Person}, Default: int64(3), Min: 0, Max: 10000, Lower: Lower{Mode: Allow}},
		{Key: QuotaBytesPerPerson, Kind: Limit, Layers: []Layer{Site, Tenant, Person}, Default: nil, Min: 1, Max: 1 << 50, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: QuotaPagesPerPerson, Kind: Limit, Layers: []Layer{Site, Tenant, Person}, Default: nil, Min: 1, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},

		// Machine access, and whether tenants exist at all.
		{Key: FeatureTokens, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: true, Lower: Lower{Mode: Allow}, Outward: true},
		{Key: FeatureMCP, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: true, Lower: Lower{Mode: Allow}, Outward: true},
		{Key: FeatureTenancy, Kind: Bool, Layers: []Layer{Site}, Default: false, Lower: Lower{Mode: Allow},
			ServerWide: "whether tenants besides the default one exist is the server's"},
		{Key: FeaturePasskeys, Kind: Bool, Layers: []Layer{Site}, Default: true, Lower: Lower{Mode: Allow},
			ServerWide: signinWhy},

		// What a new tenant starts with.
		{Key: TenantsNewAuditDefault, Kind: Bool, Layers: []Layer{Site}, Default: false, Lower: Lower{Mode: Allow},
			ServerWide: "it is the value a tenant is made with; the tenant does not exist yet"},

		// Organisations people create themselves: off until the site allows it.
		{Key: FeatureOrgCreation, Kind: Bool, Layers: []Layer{Site}, Default: false, Lower: Lower{Mode: Allow}, ServerWide: orgCreationWhy},
		{Key: OrgCreationPerPerson, Kind: Limit, Layers: []Layer{Site}, Default: int64(3), Min: 0, Max: 100, Lower: Lower{Mode: Allow}, ServerWide: orgCreationWhy},
		{Key: OrgCreationPerHour, Kind: Limit, Layers: []Layer{Site}, Default: nil, Min: 1, Max: 100000, Nullable: true, Lower: Lower{Mode: Allow}, ServerWide: orgCreationWhy},
		{Key: OrgCreationPlan, Kind: Text, Layers: []Layer{Site}, Default: "", Lower: Lower{Mode: Allow}, ServerWide: orgCreationWhy},

		// The checks of claimed email domains people ask for.
		{Key: DomainsChecksPerMinute, Kind: Limit, Layers: []Layer{Site}, Default: int64(60), Min: 1, Max: 100000, Lower: Lower{Mode: Allow},
			ServerWide: "the server's own resolver asks DNS for every tenant"},

		// The social providers a tenant's people may use.
		{Key: SigninSocial, Kind: List, Layers: []Layer{Site, Tenant}, Default: []string{"*"}, Lower: Lower{Mode: Allow}, Check: checkSocial},

		// The sign-in policy.
		{Key: SigninMode, Kind: Choice, Layers: []Layer{Site}, Default: "dual", Choices: []string{"dual", "sso_first", "sso_redirect", "local_only"}, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		// An organisation may require its own single sign-on of its people
		// (3.6 d4); it never lifts the site's.
		{Key: SigninSSOOnly, Kind: Bool, Layers: []Layer{Site, Tenant}, Default: false, Lower: Lower{Mode: Allow}, TightenOnly: true},
		{Key: SigninPasskeyRoles, Kind: List, Layers: []Layer{Site}, Default: []string{}, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		{Key: SigninLockoutEnabled, Kind: Bool, Layers: []Layer{Site}, Default: false, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		{Key: SigninLockoutMinutes, Kind: Limit, Layers: []Layer{Site}, Default: int64(15), Min: 1, Max: 10080, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		{Key: SigninLockoutScope, Kind: Choice, Layers: []Layer{Site}, Default: "ip_account", Choices: []string{"ip_account", "ip", "account"}, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		{Key: SigninPerMinute, Kind: Limit, Layers: []Layer{Site}, Default: int64(10), Min: 1, Max: 100000, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		{Key: SigninMaxFailures, Kind: Limit, Layers: []Layer{Site}, Default: int64(10), Min: 1, Max: 100000, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		{Key: SigninWindowMinutes, Kind: Limit, Layers: []Layer{Site}, Default: int64(15), Min: 1, Max: 10080, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		// The server's flags are the defaults (SetDefault); per-organisation
		// lifetimes come later.
		{Key: SessionsIdleDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(30), Min: 1, Max: 365, Lower: Lower{Mode: Allow}, ServerWide: sessionsWhy},
		{Key: SessionsMaxDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(90), Min: 1, Max: 3650, Lower: Lower{Mode: Allow}, ServerWide: sessionsWhy},
		{Key: CaptchaProvider, Kind: Choice, Layers: []Layer{Site}, Default: "image", Choices: []string{"image", "turnstile", "recaptcha", "hcaptcha"}, Lower: Lower{Mode: Allow}, ServerWide: signinWhy},
		// Validated and made whole by internal/auth (see its BindSettings).
		{Key: CaptchaContexts, Kind: Doc, Layers: []Layer{Site}, Default: captchaContextsDefault(), Lower: Lower{Mode: Allow}, ServerWide: signinWhy},

		// The storage policy.
		{Key: StorageRemotesRequire, Kind: Text, Layers: []Layer{Site}, Default: "", Lower: Lower{Mode: Allow}, ServerWide: storageWhy},
		{Key: StorageRemotesScope, Kind: Choice, Layers: []Layer{Site}, Default: "new", Choices: []string{"new", "all"}, Lower: Lower{Mode: Allow}, ServerWide: storageWhy},
		{Key: StorageRemotesGraceDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(30), Min: 0, Max: 365, Lower: Lower{Mode: Allow}, ServerWide: storageWhy},
		{Key: StorageLocalMaxBytes, Kind: Limit, Layers: []Layer{Site}, Default: nil, Min: 1 << 20, Max: 1 << 50, Nullable: true, Lower: Lower{Mode: Allow}, ServerWide: storageWhy},
		{Key: StorageHosts, Kind: List, Layers: []Layer{Site}, Default: []string{}, Lower: Lower{Mode: Allow}, ServerWide: "the hosts a remote may be on are the server's network policy"},
		{Key: CachePartial, Kind: Bool, Layers: []Layer{Site}, Default: true, Lower: Lower{Mode: Allow}, ServerWide: cacheWhy},
		{Key: CacheEvictAfterDays, Kind: Limit, Layers: []Layer{Site}, Default: int64(14), Min: 1, Max: 365, Nullable: true, Lower: Lower{Mode: Allow}, ServerWide: cacheWhy},
		{Key: CacheMinIdleHours, Kind: Limit, Layers: []Layer{Site}, Default: int64(24), Min: 1, Max: 720, Lower: Lower{Mode: Allow}, ServerWide: cacheWhy},
		{Key: CacheClonesBytes, Kind: Limit, Layers: []Layer{Site}, Default: nil, Min: 1 << 30, Max: 1 << 50, Nullable: true, Lower: Lower{Mode: Allow}, ServerWide: cacheWhy},
		{Key: CacheMinFreeBytes, Kind: Limit, Layers: []Layer{Site}, Default: int64(10 << 30), Min: 0, Max: 1 << 50, Nullable: true, Lower: Lower{Mode: Allow}, ServerWide: cacheWhy},
		{Key: CacheAssetsBytes, Kind: Limit, Layers: []Layer{Site}, Default: int64(5 << 30), Min: 64 << 20, Max: 1 << 50, Lower: Lower{Mode: Allow}, ServerWide: cacheWhy},

		// The languages of the daily text's settings pages (canonical BCP 47 tags,
		// checked by the daily text's code).
		{Key: DailyLanguages, Kind: List, Layers: []Layer{Site}, Default: []string{"zh-Hans", "en"}, Lower: Lower{Mode: Allow},
			ServerWide: "the pages of the daily text's settings are the server's"},

		// The features of 3.1: site-wide on, off for one tenant by a rule.
		{Key: FeatureOwnSSO, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureDomains, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureManagedAccounts, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureIdPAdmins, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: false, Lower: Lower{Mode: Allow}},
		{Key: FeatureAdminView, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureAuditDownload, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureTenantAudit, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: MailOwnSender, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: false, Lower: Lower{Mode: Allow}},
		{Key: FeatureOwnCaptcha, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureOwnConnections, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureOwnDaily, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},
		{Key: FeatureGitHubConnect, Kind: Bool, Layers: []Layer{Site}, ForTenant: true, Default: true, Lower: Lower{Mode: Allow}},

		// Where new spaces may sync; restrict is the modes allowed.
		{Key: StorageMode, Kind: Choice, Layers: []Layer{Site, Tenant}, Default: "server", Choices: []string{"server", "own", "tenant"}, Lower: Lower{Mode: Allow}},
		{Key: StoragePinnedBytes, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: int64(0), Min: 0, Max: 1 << 50, Lower: Lower{Mode: Allow}},

		// The tenant's totals: the site's, for each tenant by a rule (manual,
		// billing or the plan's). No tenant layer: a tenant's own value would rank
		// above the rule's, and its administrators could raise their own totals.
		{Key: QuotaPeople, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: QuotaSpaces, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: QuotaBytes, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 50, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: QuotaPages, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},

		// What a tenant may ask of the server.
		{Key: RateAPIPerMinute, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: RateMCPPerMinute, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: RateMailPerDay, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
		{Key: RateOutboundPerMinute, Kind: Limit, Layers: []Layer{Site}, ForTenant: true, Default: nil, Min: 0, Max: 1 << 40, Nullable: true, Lower: Lower{Mode: Allow}},
	}
}

func i64(n int64) *int64 { return &n }

// captchaContextsDefault is every context off, asking after 3 failures.
func captchaContextsDefault() map[string]any {
	out := map[string]any{}
	for _, c := range []string{"login", "invite", "setup", "forgot", "register"} {
		out[c] = map[string]any{"on": false, "when": "after_failures", "failures": float64(3)}
	}
	return out
}

// Why the groups of server-wide settings are server-wide.
const (
	signinWhy   = "the sign-in page comes before any tenant is known"
	sessionsWhy = "a session is the server's: per-organisation lifetimes come later"
	storageWhy  = "the server's storage policy is worked out over every space at once"
	cacheWhy    = "the cache is the server's own disk"
)

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

// checkSocial is a list of provider slugs, or ["*"] (every provider): "*"
// with anything else is "*" alone.
func checkSocial(v any) (any, error) {
	in := v.([]string)
	if len(in) > 50 {
		return nil, bad("at most 50 providers")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "*" {
			return []string{"*"}, nil
		}
		if !socialSlugRE.MatchString(p) {
			return nil, bad("%q is not the slug of a provider", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

var socialSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

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
