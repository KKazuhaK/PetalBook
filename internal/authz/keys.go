package authz

import "petalbook.app/server/internal/perm"

// The keys. Each route outside a space names one of these in the route table
// (internal/api); the table in docs/api.md and the PermissionKey union of the
// web app list the same names (a test of internal/api compares them).
//
// No key of the site or a tenant grants reading or writing a space's content:
// that goes only through the space keys, which a person holds as a member of
// the space (or, for reading, in an administrator's audited view).
var (
	// The site's own.
	SiteSettingsManage     = Register("site.settings.manage", ScopeSite, "", "Read and change the site's settings")
	SiteSecurityManage     = Register("site.security.manage", ScopeSite, "", "Read and change the sign-in security of the site")
	SiteSigninManage       = Register("site.signin.manage", ScopeSite, "", "Add, change and remove sign-in providers")
	SiteMailManage         = Register("site.mail.manage", ScopeSite, "", "Read and change the mail settings, and send a test message")
	SiteTenantsManage      = Register("site.tenants.manage", ScopeSite, "", "List, create and delete tenants; change a tenant's slug, state and quota, and give a person more than their tenant allows")
	SiteConnectionsManage  = Register("site.connections.manage", ScopeSite, "", "Set up the providers people connect to")
	SiteDailyManage        = Register("site.daily.manage", ScopeSite, "", "Set up the daily text: its interfaces, providers and lines")
	SiteStatusRead         = Register("site.status.read", ScopeSite, "", "Read the server's status")
	SiteStorageManage      = Register("site.storage.manage", ScopeSite, "", "Change the server's storage budget and a space's storage, evict and pin spaces, see the disk, and exempt a person from the site's requirement of a remote")
	SiteRecyclePurge       = Register("site.recycle.purge", ScopeSite, "", "Purge an item of the recycle bin for good")
	SiteAuditPurge         = Register("site.audit.purge", ScopeSite, "", "Purge rows of the audit log by hand, whatever the retention settings say")
	SiteAdminsManage       = Register("site.admins.manage", ScopeSite, "", "Change administrators of the whole server (their accounts, passwords, passkeys, sessions and tokens), delete them, and make or end one")
	SiteSpacesPersonalMeta = Register("site.spaces.personal.meta", ScopeSite, "", "See people's personal spaces in the administration (in lists, the recycle bin and a person's page, with their size), restore them with their person, and manage one without being its member; not their content")
	// The content of people's personal spaces is the built-in site
	// administrator's alone: no custom role holds it.
	SiteSpacesPersonalContent = BuiltInOnly(Register("site.spaces.personal.content", ScopeSite, "", "Open an audited view of a person's personal space and export it; held by the built-in site administrator only"))

	// A tenant's.
	TenantSettingsRead    = Register("tenant.settings.read", ScopeTenant, "", "Read the tenant and its settings")
	TenantSettingsWrite   = Register("tenant.settings.write", ScopeTenant, "", "Change the tenant and its settings")
	TenantUsersRead       = Register("tenant.users.read", ScopeTenant, "", "List people and read their accounts, memberships, sessions, identities and passkeys")
	TenantUsersManage     = Register("tenant.users.manage", ScopeTenant, "", "Change people's accounts, end their sessions, remove identities and passkeys, send password resets")
	TenantUsersDelete     = Register("tenant.users.delete", ScopeTenant, "", "Delete people")
	TenantInvitesManage   = Register("tenant.invites.manage", ScopeTenant, "", "Create, list and withdraw invitations")
	TenantGroupsRead      = Register("tenant.groups.read", ScopeTenant, "", "List groups, their members and their spaces")
	TenantGroupsManage    = Register("tenant.groups.manage", ScopeTenant, "", "Create, change and delete groups and their members")
	TenantSpacesCreate    = Register("tenant.spaces.create", ScopeTenant, "", "Create spaces")
	TenantSpacesRead      = Register("tenant.spaces.read", ScopeTenant, "", "List the tenant's spaces")
	TenantSpacesManage    = Register("tenant.spaces.manage", ScopeTenant, "", "Manage a space without being its member: its settings and members, but not its owners or its remote; measure, disable and enable it")
	TenantSpacesTakeover  = Register("tenant.spaces.takeover", ScopeTenant, "", "Take over a space (become its owner) or transfer it; audited, and its owners are told")
	TenantSpacesDelete    = Register("tenant.spaces.delete", ScopeTenant, "", "Delete spaces")
	TenantDataView        = Register("tenant.data.view", ScopeTenant, "features.adminView", "Open an audited, read-only view of a space")
	TenantUsersViewAs     = Register("tenant.users.viewAs", ScopeTenant, "features.adminView", "View the app as a person sees it: read only, for an hour, audited; never personal spaces, drafts or the person's account")
	TenantDataExport      = Register("tenant.data.export", ScopeTenant, "", "Export spaces and download the exports")
	TenantStorageRead     = Register("tenant.storage.read", ScopeTenant, "", "Read the storage used by the tenant and its people")
	TenantRecycleRead     = Register("tenant.recycle.read", ScopeTenant, "", "List the recycle bin")
	TenantRecycleRestore  = Register("tenant.recycle.restore", ScopeTenant, "", "Restore an item of the recycle bin")
	TenantBulkRun         = Register("tenant.bulk.run", ScopeTenant, "", "Confirm and run bulk actions, and follow their jobs")
	TenantUsageRead       = Register("tenant.usage.read", ScopeTenant, "", "Read the usage of the tenant")
	TenantAuditRead       = Register("tenant.audit.read", ScopeTenant, "features.tenantAudit", "Read the audit log")
	TenantTokensRead      = Register("tenant.tokens.read", ScopeTenant, "", "List people's API tokens and app authorizations")
	TenantTokensRevoke    = Register("tenant.tokens.revoke", ScopeTenant, "", "Revoke people's API tokens and app authorizations")
	TenantPermissionsRead = Register("tenant.permissions.read", ScopeTenant, "", "List the permission keys")
	TenantGitHubManage    = Register("tenant.github.manage", ScopeTenant, "", "Install the site's GitHub App on the organisation's GitHub account, and see, change or disconnect the organisation's installations")
	TenantDomainsManage   = Register("tenant.domains.manage", ScopeTenant, "features.domains", "Claim the organisation's email domains, verify them by DNS, keep others from making organisations on them, and remove them")
	TenantSigninManage    = Register("tenant.signin.manage", ScopeTenant, "features.ownSso", "Add, change, test and remove the organisation's own single sign-on providers (OpenID Connect and SAML), and route its verified domains to them")

	TenantMailManage    = Register("tenant.mail.manage", ScopeTenant, "mail.ownSender", "See, change and remove the organisation's own mail sender (SMTP), and send a test message through it")
	TenantCaptchaManage = Register("tenant.captcha.manage", ScopeTenant, "features.ownCaptcha", "See, set, try and remove the organisation's own captcha provider (Turnstile, reCAPTCHA, hCaptcha or the built-in image) and its keys")

	// A space's: the four permissions of internal/perm, under their key names.
	// The API keeps their short names (read, write, members, manage).
	SpaceRead          = Register("space.read", ScopeSpace, "", "See pages, history, attachments and search")
	SpaceWrite         = Register("space.write", ScopeSpace, "", "Create, change, move and delete pages and folders")
	SpaceMembersManage = Register("space.members.manage", ScopeSpace, "", "Add, change and remove the members of the space")
	SpaceManage        = Register("space.manage", ScopeSpace, "", "Rename the space; its inbox, remote, sync, types and templates")
)

// SpaceKey is the key of a space permission.
func SpaceKey(p perm.Perm) Key {
	switch p {
	case perm.Read:
		return SpaceRead
	case perm.Write:
		return SpaceWrite
	case perm.Members:
		return SpaceMembersManage
	case perm.Manage:
		return SpaceManage
	}
	panic("authz: no key for space permission " + p.String())
}
