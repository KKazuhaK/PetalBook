package api

import (
	"errors"
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/oauth"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// access is who may call a route. There is no zero value on purpose: a route
// added without saying so does not register.
type access struct {
	kind kind
	// need is what a space route asks of the caller's permissions in the space.
	need perm.Perm
	// orSelf waives need for a caller acting on themselves: when the {user} of
	// the path is the caller, being a member is enough (anyone may leave).
	orSelf bool
	// memberOnly: a space route that an administrator who is not a member of
	// the space never reaches, whatever keys they hold (its remote: pointing
	// it elsewhere is taking the space over, which is its own audited
	// endpoint, POST /api/admin/spaces/{space}/takeover).
	memberOnly bool
	// key is what a route outside spaces asks of the caller (kindKey).
	key authz.Key
	// feature is the tenant feature the route belongs to (a boolean setting,
	// features.*): handle answers 403 feature_off before the handler when the
	// caller's tenant does not have it. alwaysOn says the route belongs to no
	// feature; one of the two is stated for every route (see on and always),
	// so a new route cannot forget to choose.
	feature  string
	alwaysOn bool
}

// on is the access of a route that belongs to a feature: while the caller's
// tenant does not have it, the route answers 403 feature_off. It is stated
// once: a route that asks a key whose own feature is checked with the key.
func (a access) on(feature string) access {
	if a.feature != "" || a.alwaysOn || a.kind == kindPublic || feature == "" {
		panic("api: a route belongs to one feature, and a public one to none (" + feature + ")")
	}
	a.feature = feature
	return a
}

// always is the access of a route that belongs to no feature: switching
// nothing off closes it (signing in, one's own account, reading what one has,
// the administration of the settings themselves).
func (a access) always() access {
	if a.feature != "" || a.alwaysOn {
		panic("api: a route belongs to one feature, or to none")
	}
	a.alwaysOn = true
	return a
}

type kind int

const (
	kindPublic kind = iota + 1
	kindSignedIn
	kindKey
	kindSpace
)

var (
	// public needs no session.
	public = access{kind: kindPublic}
	// signedIn needs a session.
	signedIn = access{kind: kindSignedIn}
)

// needs is a route outside spaces that asks for a permission key: a site key
// of the server's own things, or a tenant key, which is checked in the
// caller's own tenant (what the route shows and changes is that tenant's, see
// adminScope; a site administrator holds every tenant key in every tenant).
func needs(k authz.Key) access {
	if k.Scope() == authz.ScopeSpace {
		panic("api: a space key is asked by inSpace, not needs")
	}
	return access{kind: kindKey, key: k, feature: k.Feature()}
}

// inSpaceMembers is inSpace for a member of the space only (see memberOnly).
func inSpaceMembers(need perm.Perm) access {
	a := inSpace(need)
	a.memberOnly = true
	return a
}

// inSpaceOrSelf is inSpace, except that the caller may do it to themselves.
func inSpaceOrSelf(need perm.Perm) access {
	a := inSpace(need)
	a.orSelf = true
	return a
}

// inSpace is a route on the {space} of the path: the caller must hold need in it.
// Someone who may not even see the space (not a member, and not an
// administrator) is told it does not exist; a member without the permission
// is refused with 403, the space being theirs to see.
func inSpace(need perm.Perm) access {
	if need == 0 {
		panic("api: a space route must ask for a permission")
	}
	return access{kind: kindSpace, need: need}
}

// call is a request after the middleware has decided it may proceed. Handlers
// read who is asking and which space from here, and never decide either.
type call struct {
	w http.ResponseWriter
	r *http.Request
	// p is the signed-in user; nil only on public routes, where it is also nil
	// for a visitor who is not signed in.
	p *auth.Principal
	// sp is the space of {space} on member routes.
	sp *spaces.Handle
	// ended is why the session of the request's cookie ended, on a public
	// route reached with such a cookie ("" otherwise).
	ended string
	// scope is the tenant the request acts in and the capacity it acts with
	// (scope.go); p is narrowed to it when the scope header named a tenant.
	scope actingScope
}

func (c *call) user() store.User { return c.p.User }

// author is who a change by the caller is credited to in the space's history
// (see spaces.Manager.AuthorOf); a change made with an API token says so.
func (s *server) author(c *call) space.Author {
	a := s.spaces.AuthorOf(c.user())
	if c.p.Grant != nil {
		a.Via = space.ViaToken
	}
	a.MaxFile = s.fileLimit(c)
	return a
}

type route struct {
	method, pattern string
	access          access
	// accountsOnly routes answer 404 in single-user mode.
	accountsOnly bool
	// optionalUser makes a public route look for a session without requiring one.
	optionalUser bool
	// upload marks the one kind of route whose body is a file, not JSON: it
	// must be sent as application/octet-stream (see wrap).
	upload bool
	h      func(*server, *call)
}

// routeTable is every endpoint. The access of each is stated here and enforced
// in handle, in one place.
var routeTable = []route{
	{"GET", "/api/meta", public.always(), false, true, false, (*server).getMeta},
	{"GET", "/api/session", public.always(), false, true, false, (*server).getSession},

	{"POST", "/api/setup", public.always(), true, false, false, (*server).setup},
	{"POST", "/api/login", public.always(), true, false, false, (*server).login},
	{"POST", "/api/logout", public.always(), true, false, false, (*server).logout},
	{"POST", "/api/auth/oidc/start", public.always(), true, true, false, (*server).oidcStart},
	{"GET", oidcCallbackPath, public.always(), true, false, false, (*server).oidcCallback},
	{"POST", "/api/invites/check", public.always(), true, false, false, (*server).checkInvite},
	{"POST", "/api/invites/accept", public.always(), true, false, false, (*server).acceptInvite},

	{"GET", "/api/connections", signedIn.always(), true, false, false, (*server).listConnections},
	{"POST", "/api/connections/{provider}/authorization/start", signedIn.always(), true, false, false, (*server).connectStart},
	{"GET", connCallbackPrefix + "{provider}" + connCallbackSuffix, public.always(), true, true, false, (*server).connectCallback},
	{"GET", connManifestPath, public.always(), true, true, false, (*server).connectManifestCallback},
	{"DELETE", "/api/connections/{provider}/authorization", signedIn.always(), true, false, false, (*server).connectDisconnect},
	{"DELETE", "/api/connections/{provider}/leftovers/{repoId}", signedIn.always(), true, false, false, (*server).connectDismissLeftover},
	{"GET", "/api/connections/{provider}/owners", signedIn.always(), true, false, false, (*server).connectOwners},
	{"GET", "/api/connections/{provider}/repos", signedIn.always(), true, false, false, (*server).connectRepos},

	{"GET", "/api/account", signedIn.always(), true, false, false, (*server).getAccount},
	{"PATCH", "/api/account", signedIn.always(), true, false, false, (*server).patchAccount},
	{"POST", "/api/account/password", signedIn.always(), true, false, false, (*server).changePassword},
	{"DELETE", "/api/account/identities/{id}", signedIn.always(), true, false, false, (*server).unlinkIdentity},
	{"GET", "/api/account/usage", signedIn.always(), true, false, false, (*server).accountUsage},
	{"GET", "/api/account/tokens", signedIn.always(), true, false, false, (*server).listMyTokens},
	{"POST", "/api/account/tokens", signedIn.on(settings.FeatureTokens), true, false, false, (*server).createToken},
	{"DELETE", "/api/account/tokens/{id}", signedIn.always(), true, false, false, (*server).revokeMyToken},
	{"GET", "/api/account/grants", signedIn.always(), true, false, false, (*server).listMyGrants},
	{"DELETE", "/api/account/grants/{id}", signedIn.always(), true, false, false, (*server).revokeMyGrant},
	{"GET", "/api/oauth/consent", signedIn.on(settings.FeatureMCP), true, false, false, (*server).oauthConsentInfo},
	{"POST", "/api/oauth/consent", signedIn.on(settings.FeatureMCP), true, false, false, (*server).oauthConsentDecide},

	{"GET", "/api/groups", signedIn.always(), true, false, false, (*server).findGroups},
	{"GET", "/api/people", signedIn.always(), true, false, false, (*server).findPeople},
	{"GET", "/api/settings", signedIn.always(), true, false, false, (*server).mySettings},

	{"GET", "/api/users", needs(authz.TenantUsersRead).always(), true, false, false, (*server).listUsers},
	{"GET", "/api/users/{id}", needs(authz.TenantUsersRead).always(), true, false, false, (*server).getUser},
	{"PATCH", "/api/users/{id}", needs(authz.TenantUsersManage).always(), true, false, false, (*server).patchUser},
	{"POST", "/api/users/{id}/password", needs(authz.TenantUsersManage).always(), true, false, false, (*server).setTemporaryPassword},
	{"GET", "/api/users/{id}/memberships", needs(authz.TenantUsersRead).always(), true, false, false, (*server).userMemberships},
	// The sessions and connected accounts of a person are for administrators of the whole server (a tenant's administrator is answered 404).
	{"GET", "/api/users/{id}/sessions", needs(authz.TenantUsersRead).always(), true, false, false, (*server).userSessions},
	{"DELETE", "/api/users/{id}/sessions", needs(authz.TenantUsersManage).always(), true, false, false, (*server).endUserSessions},
	{"DELETE", "/api/users/{id}/sessions/{session}", needs(authz.TenantUsersManage).always(), true, false, false, (*server).endUserSession},
	{"GET", "/api/users/{id}/identities", needs(authz.TenantUsersRead).always(), true, false, false, (*server).userIdentities},
	{"DELETE", "/api/users/{id}/identities/{identity}", needs(authz.TenantUsersManage).always(), true, false, false, (*server).disconnectUserIdentity},
	{"DELETE", "/api/users/{id}", needs(authz.TenantUsersDelete).always(), true, false, false, (*server).adminDeleteUser},
	{"POST", "/api/invites", needs(authz.TenantInvitesManage).always(), true, false, false, (*server).createInvite},
	{"POST", "/api/admin/members/batch", needs(authz.TenantInvitesManage).always(), true, false, false, (*server).adminAddMembersBatch},
	{"GET", "/api/invites", needs(authz.TenantInvitesManage).always(), true, false, false, (*server).listInvites},
	{"DELETE", "/api/invites/{id}", needs(authz.TenantInvitesManage).always(), true, false, false, (*server).deleteInvite},

	{"GET", "/api/admin/usage", needs(authz.TenantUsageRead).always(), true, false, false, (*server).adminUsage},
	{"GET", "/api/admin/spaces", needs(authz.TenantSpacesRead).always(), true, false, false, (*server).adminListSpaces},
	{"GET", "/api/admin/spaces/{space}", needs(authz.TenantSpacesRead).always(), true, false, false, (*server).adminGetSpace},
	{"POST", "/api/admin/spaces/{space}/size", needs(authz.TenantSpacesManage).always(), true, false, false, (*server).adminResizeSpace},
	{"PUT", "/api/admin/spaces/{space}/storage", needs(authz.SiteStorageManage).always(), true, false, false, (*server).adminPutSpaceStorage},
	{"POST", "/api/admin/spaces/{space}/evict", needs(authz.SiteStorageManage).always(), true, false, false, (*server).adminEvictSpace},
	{"GET", "/api/admin/storage", needs(authz.TenantStorageRead).always(), true, false, false, (*server).adminGetStorage},
	{"GET", "/api/admin/storage/people", needs(authz.TenantStorageRead).always(), true, false, false, (*server).adminListPeople},
	{"GET", "/api/admin/storage/people/{user}", needs(authz.TenantStorageRead).always(), true, false, false, (*server).adminGetPerson},
	{"POST", "/api/admin/spaces/{space}/transfer", needs(authz.TenantSpacesTakeover).always(), true, false, false, (*server).adminTransferSpace},
	{"POST", "/api/admin/spaces/{space}/takeover", needs(authz.TenantSpacesTakeover).always(), true, false, false, (*server).adminTakeOverSpace},
	{"POST", "/api/admin/spaces/{space}/disable", needs(authz.TenantSpacesManage).always(), true, false, false, (*server).adminDisableSpace},
	{"POST", "/api/admin/spaces/{space}/enable", needs(authz.TenantSpacesManage).always(), true, false, false, (*server).adminEnableSpace},
	{"DELETE", "/api/admin/spaces/{space}", needs(authz.TenantSpacesDelete).always(), true, false, false, (*server).adminDeleteSpace},
	{"GET", "/api/admin/spaces/{space}/deletion", needs(authz.TenantSpacesDelete).always(), true, false, false, (*server).adminSpaceDeletion},
	{"POST", "/api/admin/spaces/{space}/view", needs(authz.TenantDataView), true, false, false, (*server).adminOpenView},
	{"DELETE", "/api/admin/spaces/{space}/view", needs(authz.TenantDataView), true, false, false, (*server).adminEndView},
	{"GET", "/api/admin/recycle", needs(authz.TenantRecycleRead).always(), true, false, false, (*server).adminListRecycle},
	{"POST", "/api/admin/recycle/{item}/restore", needs(authz.TenantRecycleRestore).always(), true, false, false, (*server).adminRestoreItem},
	{"DELETE", "/api/admin/recycle/{item}", needs(authz.SiteRecyclePurge).always(), true, false, false, (*server).adminPurgeItem},
	{"POST", "/api/admin/confirm", needs(authz.TenantBulkRun).always(), true, false, false, (*server).adminConfirm},
	{"POST", "/api/admin/bulk", needs(authz.TenantBulkRun).always(), true, false, false, (*server).adminBulk},
	{"GET", "/api/admin/jobs/{id}", needs(authz.TenantBulkRun).always(), true, false, false, (*server).adminGetJob},
	{"GET", "/api/admin/users/{user}/deletion", needs(authz.TenantUsersDelete).always(), true, false, false, (*server).adminUserDeletion},
	{"POST", "/api/admin/exports", needs(authz.TenantDataExport).always(), true, false, false, (*server).adminCreateExport},
	{"GET", "/api/admin/exports", needs(authz.TenantDataExport).always(), true, false, false, (*server).adminListExports},
	{"GET", "/api/admin/exports/{id}", needs(authz.TenantDataExport).always(), true, false, false, (*server).adminGetExport},
	{"DELETE", "/api/admin/exports/{id}", needs(authz.TenantDataExport).always(), true, false, false, (*server).adminDeleteExport},
	{"GET", "/api/admin/exports/{id}/download", needs(authz.TenantDataExport).always(), true, false, false, (*server).adminDownloadExport},
	{"GET", "/api/admin/daily", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminGetDaily},
	{"GET", "/api/admin/daily/lines", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminGetDailyLines},
	{"POST", "/api/admin/daily/lines/preview", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminPreviewDailyLines},
	{"PUT", "/api/admin/daily/lines", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminPutDailyLines},
	{"DELETE", "/api/admin/daily/lines", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminDeleteDailyLines},
	{"PUT", "/api/admin/daily/apis/{id}", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminPutDailyAPI},
	{"DELETE", "/api/admin/daily/apis/{id}", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminDeleteDailyAPI},
	{"PUT", "/api/admin/daily/providers/{id}", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminPutDailyProvider},
	{"POST", "/api/admin/daily/try", needs(authz.SiteDailyManage).always(), true, false, false, (*server).adminTryDailyAPI},
	{"GET", "/api/admin/connections", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminListConnections},
	{"POST", "/api/admin/connections", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminCreateConnection},
	{"GET", "/api/admin/connections/{provider}", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminGetConnection},
	{"PUT", "/api/admin/connections/{provider}", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminPutConnection},
	{"PATCH", "/api/admin/connections/{provider}", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminPutConnection},
	{"POST", "/api/admin/connections/{provider}/manifest", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminConnectionManifest},
	{"POST", "/api/admin/connections/{provider}/manifest/retry", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminConnectionManifestRetry},
	{"POST", "/api/admin/connections/{provider}/check", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminCheckConnection},
	{"DELETE", "/api/admin/connections/{provider}", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminDeleteConnection},
	{"GET", "/api/admin/connections/{provider}/installations", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminConnectionInstallations},
	{"PUT", "/api/admin/connections/{provider}/installations/{id}", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminPutConnectionInstallation},
	{"GET", "/api/admin/status", needs(authz.SiteStatusRead).always(), true, false, false, (*server).adminStatus},
	{"GET", "/api/admin/groups", needs(authz.TenantGroupsRead).always(), true, false, false, (*server).adminListGroups},
	{"POST", "/api/admin/groups", needs(authz.TenantGroupsManage).always(), true, false, false, (*server).adminCreateGroup},
	{"GET", "/api/admin/groups/{group}", needs(authz.TenantGroupsRead).always(), true, false, false, (*server).adminGetGroup},
	{"PATCH", "/api/admin/groups/{group}", needs(authz.TenantGroupsManage).always(), true, false, false, (*server).adminPatchGroup},
	{"DELETE", "/api/admin/groups/{group}", needs(authz.TenantGroupsManage).always(), true, false, false, (*server).adminDeleteGroup},
	{"GET", "/api/admin/groups/{group}/members", needs(authz.TenantGroupsRead).always(), true, false, false, (*server).adminGroupMembers},
	{"POST", "/api/admin/groups/{group}/members", needs(authz.TenantGroupsManage).always(), true, false, false, (*server).adminAddGroupMembers},
	{"POST", "/api/admin/groups/{group}/members/batch", needs(authz.TenantGroupsManage).always(), true, false, false, (*server).adminAddGroupMembersBatch},
	{"DELETE", "/api/admin/groups/{group}/members/{user}", needs(authz.TenantGroupsManage).always(), true, false, false, (*server).adminRemoveGroupMember},
	{"GET", "/api/admin/groups/{group}/spaces", needs(authz.TenantGroupsRead).always(), true, false, false, (*server).adminGroupSpaces},
	{"GET", "/api/admin/audit", needs(authz.TenantAuditRead).always(), true, false, false, (*server).adminAudit},
	{"GET", "/api/admin/audit/kinds", needs(authz.TenantAuditRead).always(), true, false, false, (*server).adminAuditKinds},
	{"GET", "/api/admin/audit/download", needs(authz.TenantAuditRead).on(settings.FeatureAuditDownload), true, false, false, (*server).adminAuditDownload},
	{"GET", "/api/admin/tenants", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminListTenants},
	{"POST", "/api/admin/tenants", needs(authz.SiteTenantsManage).on(settings.FeatureTenancy), true, false, false, (*server).adminCreateTenant},
	{"GET", "/api/admin/tenants/{tenant}", needs(authz.TenantSettingsRead).always(), true, false, false, (*server).adminGetTenant},
	{"PATCH", "/api/admin/tenants/{tenant}", needs(authz.TenantSettingsWrite).always(), true, false, false, (*server).adminPatchTenant},
	{"DELETE", "/api/admin/tenants/{tenant}", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminDeleteTenant},
	{"PUT", "/api/admin/tenants/{tenant}/plan", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminPutTenantPlan},
	{"GET", "/api/admin/plans", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminListPlans},
	{"POST", "/api/admin/plans", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminCreatePlan},
	{"GET", "/api/admin/plans/{slug}", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminGetPlan},
	{"PATCH", "/api/admin/plans/{slug}", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminPatchPlan},
	{"DELETE", "/api/admin/plans/{slug}", needs(authz.SiteTenantsManage).always(), true, false, false, (*server).adminDeletePlan},
	{"GET", "/api/admin/tenants/{tenant}/settings", needs(authz.TenantSettingsRead).always(), true, false, false, (*server).adminGetTenantSettings},
	{"PUT", "/api/admin/tenants/{tenant}/settings/{key}", needs(authz.TenantSettingsWrite).always(), true, false, false, (*server).adminPutTenantSetting},
	{"GET", "/api/admin/settings", needs(authz.SiteSettingsManage).always(), true, false, false, (*server).adminGetSettings},
	{"PUT", "/api/admin/settings/{key}", needs(authz.SiteSettingsManage).always(), true, false, false, (*server).adminPutSetting},
	{"GET", "/api/admin/secrets", needs(authz.SiteSettingsManage).always(), true, false, false, (*server).adminListSecrets},
	{"PUT", "/api/admin/secrets/{name}", needs(authz.SiteSettingsManage).always(), true, false, false, (*server).adminPutSecret},
	{"DELETE", "/api/admin/secrets/{name}", needs(authz.SiteSettingsManage).always(), true, false, false, (*server).adminDeleteSecret},
	{"GET", "/api/admin/tenants/{tenant}/secrets", needs(authz.TenantSettingsRead).always(), true, false, false, (*server).adminListTenantSecrets},
	{"PUT", "/api/admin/tenants/{tenant}/secrets/{name}", needs(authz.TenantSettingsWrite).always(), true, false, false, (*server).adminPutTenantSecret},
	{"DELETE", "/api/admin/tenants/{tenant}/secrets/{name}", needs(authz.TenantSettingsWrite).always(), true, false, false, (*server).adminDeleteTenantSecret},
	{"GET", "/api/admin/tokens", needs(authz.TenantTokensRead).always(), true, false, false, (*server).adminListTokens},
	{"DELETE", "/api/admin/tokens/{id}", needs(authz.TenantTokensRevoke).always(), true, false, false, (*server).adminRevokeToken},
	{"GET", "/api/admin/grants", needs(authz.TenantTokensRead).always(), true, false, false, (*server).adminListGrants},
	{"GET", "/api/admin/permissions", needs(authz.TenantPermissionsRead).always(), true, false, false, (*server).adminPermissions},
	{"DELETE", "/api/admin/grants/{id}", needs(authz.TenantTokensRevoke).always(), true, false, false, (*server).adminRevokeGrant},

	{"GET", "/api/spaces", signedIn.always(), false, false, false, (*server).listSpaces},
	{"GET", "/api/sync/heads", signedIn.on(settings.OfflineCopies), false, false, false, (*server).syncHeads},
	{"POST", "/api/spaces", needs(authz.TenantSpacesCreate).always(), true, false, false, (*server).createSpace},
	{"PATCH", "/api/spaces/{space}", inSpace(perm.Manage).always(), true, false, false, (*server).renameSpace},

	{"GET", "/api/spaces/{space}/pages", inSpace(perm.Read).always(), false, false, false, (*server).listPages},
	{"GET", "/api/spaces/{space}/pages/groups", inSpace(perm.Read).always(), false, false, false, (*server).getGroups},
	{"GET", "/api/spaces/{space}/views", inSpace(perm.Read).always(), false, false, false, (*server).getViews},
	{"PUT", "/api/spaces/{space}/views", inSpace(perm.Write).always(), false, false, false, (*server).putViews},
	{"POST", "/api/spaces/{space}/pages", inSpace(perm.Write).always(), false, false, false, (*server).createPage},
	{"GET", "/api/spaces/{space}/pages/{id}", inSpace(perm.Read).always(), false, false, false, (*server).getPage},
	{"PUT", "/api/spaces/{space}/pages/{id}", inSpace(perm.Write).always(), false, false, false, (*server).savePage},
	{"DELETE", "/api/spaces/{space}/pages/{id}", inSpace(perm.Write).always(), false, false, false, (*server).deletePage},
	{"PUT", "/api/spaces/{space}/pages/{id}/draft", inSpace(perm.Write).always(), false, false, false, (*server).putDraft},
	{"DELETE", "/api/spaces/{space}/pages/{id}/draft", inSpace(perm.Read).always(), false, false, false, (*server).deleteDraft},
	{"POST", "/api/spaces/{space}/pages/{id}/commit", inSpace(perm.Write).always(), false, false, false, (*server).commitDraft},
	{"PATCH", "/api/spaces/{space}/pages/{id}/props", inSpace(perm.Write).always(), false, false, false, (*server).patchProps},
	{"GET", "/api/spaces/{space}/pages/{id}/backlinks", inSpace(perm.Read).always(), false, false, false, (*server).getBacklinks},
	{"GET", "/api/spaces/{space}/pages/{id}/links", inSpace(perm.Read).always(), false, false, false, (*server).getLinks},
	{"POST", "/api/spaces/{space}/pages/{id}/move", inSpace(perm.Write).always(), false, false, false, (*server).movePage},
	{"GET", "/api/spaces/{space}/tree", inSpace(perm.Read).always(), false, false, false, (*server).getTree},
	{"POST", "/api/spaces/{space}/folders", inSpace(perm.Write).always(), false, false, false, (*server).createFolder},
	{"POST", "/api/spaces/{space}/folders/move", inSpace(perm.Write).always(), false, false, false, (*server).moveFolder},
	{"DELETE", "/api/spaces/{space}/folders", inSpace(perm.Write).always(), false, false, false, (*server).deleteFolder},
	{"POST", "/api/spaces/{space}/pages/batch", inSpace(perm.Write).always(), false, false, false, (*server).batchPages},
	{"GET", "/api/spaces/{space}/tags", inSpace(perm.Read).always(), false, false, false, (*server).getTags},
	{"PUT", "/api/spaces/{space}/tags/{tag}", inSpace(perm.Write).always(), false, false, false, (*server).putTag},
	{"GET", "/api/spaces/{space}/types", inSpace(perm.Read).always(), false, false, false, (*server).getTypes},
	{"PUT", "/api/spaces/{space}/types/{name}", inSpace(perm.Manage).always(), false, false, false, (*server).putType},
	{"DELETE", "/api/spaces/{space}/types/{name}", inSpace(perm.Manage).always(), false, false, false, (*server).deleteType},
	{"GET", "/api/spaces/{space}/appearance", inSpace(perm.Read).always(), false, false, false, (*server).getAppearance},
	{"PUT", "/api/spaces/{space}/folders/style", inSpace(perm.Write).always(), false, false, false, (*server).putFolderStyle},
	{"GET", "/api/spaces/{space}/sources", inSpace(perm.Read).always(), false, false, false, (*server).getSources},
	{"PUT", "/api/spaces/{space}/sources/presets", inSpace(perm.Manage).always(), false, false, false, (*server).putSourcePresets},
	{"POST", "/api/spaces/{space}/types/{name}/rename-field", inSpace(perm.Manage).always(), false, false, false, (*server).renameTypeField},
	{"PUT", "/api/spaces/{space}/folders/type", inSpace(perm.Manage).always(), false, false, false, (*server).putFolderType},
	{"GET", "/api/spaces/{space}/templates", inSpace(perm.Read).always(), false, false, false, (*server).getTemplates},
	{"PUT", "/api/spaces/{space}/templates/defaults", inSpace(perm.Manage).always(), false, false, false, (*server).putTemplateDefaults},
	{"GET", "/api/spaces/{space}/templates/{name}", inSpace(perm.Read).always(), false, false, false, (*server).getTemplate},
	{"PUT", "/api/spaces/{space}/templates/{name}", inSpace(perm.Manage).always(), false, false, false, (*server).putTemplate},
	{"DELETE", "/api/spaces/{space}/templates/{name}", inSpace(perm.Manage).always(), false, false, false, (*server).deleteTemplate},
	{"GET", "/api/notifications", signedIn.always(), false, false, false, (*server).listNotifications},
	{"GET", "/api/notifications/unread", signedIn.always(), false, false, false, (*server).getUnreadNotifications},
	{"POST", "/api/notifications/read", signedIn.always(), false, false, false, (*server).markNotificationsRead},
	{"POST", "/api/notifications/read-all", signedIn.always(), false, false, false, (*server).markAllNotificationsRead},
	{"GET", "/api/prefs", signedIn.always(), false, false, false, (*server).getPrefs},
	{"PATCH", "/api/prefs", signedIn.always(), false, false, false, (*server).patchPrefs},

	{"GET", "/api/spaces/{space}/checks", inSpace(perm.Read).always(), false, false, false, (*server).getChecks},
	{"POST", "/api/spaces/{space}/checks/fix", inSpace(perm.Write).always(), false, false, false, (*server).fixCheck},
	{"GET", "/api/spaces/{space}/daily", inSpace(perm.Read).always(), false, false, false, (*server).getDaily},
	{"GET", "/api/spaces/{space}/daily/settings", inSpace(perm.Read).always(), false, false, false, (*server).getDailySettings},
	{"PUT", "/api/spaces/{space}/daily/settings", inSpace(perm.Manage).always(), false, false, false, (*server).putDailySettings},
	{"GET", "/api/spaces/{space}/daily/prefs", inSpace(perm.Read).always(), true, false, false, (*server).getDailyPrefs},
	{"PUT", "/api/spaces/{space}/daily/prefs", inSpace(perm.Manage).always(), true, false, false, (*server).putDailyPrefs},
	{"GET", "/api/spaces/{space}/favorites", inSpace(perm.Read).always(), false, false, false, (*server).getFavorites},
	{"POST", "/api/spaces/{space}/favorites", inSpace(perm.Read).always(), false, false, false, (*server).addFavorite},
	{"DELETE", "/api/spaces/{space}/favorites", inSpace(perm.Read).always(), false, false, false, (*server).deleteFavorite},
	{"PUT", "/api/spaces/{space}/favorites/order", inSpace(perm.Read).always(), false, false, false, (*server).putFavoritesOrder},
	{"GET", "/api/spaces/{space}/sync/catalog", inSpace(perm.Read).on(settings.OfflineCopies), false, false, false, (*server).syncCatalog},
	{"GET", "/api/spaces/{space}/changes", inSpace(perm.Read).on(settings.OfflineCopies), false, false, false, (*server).syncChanges},
	{"GET", "/api/spaces/{space}/sync/pages", inSpace(perm.Read).on(settings.OfflineCopies), false, false, false, (*server).syncPages},
	{"POST", "/api/spaces/{space}/reindex", inSpace(perm.Manage).always(), false, false, false, (*server).reindex},

	{"POST", "/api/spaces/{space}/pages/{id}/assets", inSpace(perm.Write).always(), false, false, true, (*server).putAsset},
	{"GET", "/api/spaces/{space}/files/{path...}", inSpace(perm.Read).always(), false, false, false, (*server).getFile},
	{"POST", "/api/spaces/{space}/import", inSpace(perm.Write).always(), false, false, true, (*server).importMarkdown},

	{"GET", "/api/spaces/{space}/pages/{id}/history", inSpace(perm.Read).always(), false, false, false, (*server).getHistory},
	{"GET", "/api/spaces/{space}/pages/{id}/versions/{commit}", inSpace(perm.Read).always(), false, false, false, (*server).getVersion},

	{"GET", "/api/spaces/{space}/members", inSpace(perm.Read).always(), true, false, false, (*server).listMembers},
	{"GET", "/api/spaces/{space}/members/effective", inSpace(perm.Read).always(), true, false, false, (*server).effectiveMembers},
	{"POST", "/api/spaces/{space}/groups", inSpace(perm.Members).always(), true, false, false, (*server).addSpaceGroup},
	{"PATCH", "/api/spaces/{space}/groups/{group}", inSpace(perm.Members).always(), true, false, false, (*server).patchSpaceGroup},
	{"DELETE", "/api/spaces/{space}/groups/{group}", inSpace(perm.Members).always(), true, false, false, (*server).deleteSpaceGroup},
	{"POST", "/api/spaces/{space}/members", inSpace(perm.Members).always(), true, false, false, (*server).addMember},
	{"POST", "/api/spaces/{space}/members/batch", inSpace(perm.Members).always(), true, false, false, (*server).addMembersBatch},
	{"GET", "/api/spaces/{space}/people", inSpace(perm.Members).always(), true, false, false, (*server).findSpacePeople},
	{"PATCH", "/api/spaces/{space}/members/{user}", inSpace(perm.Members).always(), true, false, false, (*server).patchMember},
	{"DELETE", "/api/spaces/{space}/members/{user}", inSpaceOrSelf(perm.Members).always(), true, false, false, (*server).removeMember},

	{"GET", "/api/spaces/{space}/remote", inSpace(perm.Manage).always(), true, false, false, (*server).getRemote},
	{"POST", "/api/spaces/{space}/remote/key", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).remoteKey},
	{"PUT", "/api/spaces/{space}/remote", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).putRemote},
	{"DELETE", "/api/spaces/{space}/remote", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).deleteRemote},
	{"POST", "/api/spaces/{space}/remote/sync", inSpace(perm.Manage).always(), true, false, false, (*server).syncRemote},
	{"POST", "/api/spaces/{space}/remote/accept", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).acceptRemote},
	{"GET", "/api/spaces/{space}/remote/assets", inSpace(perm.Manage).always(), true, false, false, (*server).getAssetsRemote},
	{"PUT", "/api/spaces/{space}/remote/assets", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).putAssetsRemote},
	{"DELETE", "/api/spaces/{space}/remote/assets", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).deleteAssetsRemote},
	{"POST", "/api/spaces/{space}/remote/assets/move", inSpaceMembers(perm.Manage).always(), true, false, false, (*server).moveAssets},
}

// RouteInfo describes a route for tests and tooling.
type RouteInfo struct {
	Method, Pattern string
	Public          bool // no session needed
	// Key is the permission key a route outside spaces asks for ("" for none).
	Key string
	// Feature is the feature the route belongs to ("" for none); AlwaysOn says
	// it was stated that it belongs to none.
	Feature   string
	AlwaysOn  bool
	Admin     bool      // it asks for a tenant key that only administrators hold
	SiteAdmin bool      // it asks for a site key
	Space     bool      // a route on {space}
	Perm      perm.Perm // what a space route asks of the caller in the space
	OrSelf    bool      // the permission is waived for the caller's own {user}
	// MemberOnly: an administrator who is not a member never reaches it.
	MemberOnly   bool
	AccountsOnly bool
	Upload       bool // the body is a file, sent as application/octet-stream
}

// Routes lists every endpoint with its access.
func Routes() []RouteInfo {
	out := make([]RouteInfo, len(routeTable))
	for i, r := range routeTable {
		a := r.access
		out[i] = RouteInfo{Method: r.method, Pattern: r.pattern, Feature: a.feature, AlwaysOn: a.alwaysOn, Public: a.kind == kindPublic, Space: a.kind == kindSpace, Perm: a.need, OrSelf: a.orSelf,
			MemberOnly: a.memberOnly, AccountsOnly: r.accountsOnly, Upload: r.upload}
		if a.kind == kindKey {
			out[i].Key = a.key.Name()
			out[i].SiteAdmin = a.key.Scope() == authz.ScopeSite
			member, _ := authz.RoleSet(authz.RoleMember)
			out[i].Admin = a.key.Scope() == authz.ScopeTenant && !member.Has(a.key)
		}
	}
	return out
}

// handle wraps a route with the access decision: who is asking, whether they
// may, and which space. This is the only place any of that is decided.
func (s *server) handle(rt route) http.HandlerFunc {
	if rt.access.kind == 0 {
		panic("api: route " + rt.method + " " + rt.pattern + " has no access level")
	}
	if rt.access.feature == "" && !rt.access.alwaysOn {
		panic("api: route " + rt.method + " " + rt.pattern + " does not say which feature it belongs to (on) or that it belongs to none (always)")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if rt.accountsOnly && s.single {
			writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
			return
		}
		if rt.access.kind == kindPublic {
			// A public route is where a session starts: it keeps the browser's name for the administrator's list of sessions.
			r = r.WithContext(auth.WithUserAgent(r.Context(), r.UserAgent()))
		}
		c := &call{w: w, r: r}
		if secret, ok := bearerToken(r); ok {
			// A request with a token is judged by the token alone: never by a
			// cookie sent with it, and never falling back to one.
			viaOAuth := s.oauth != nil && oauth.LooksLikeAccessToken(secret)
			if viaOAuth {
				// An access token of the authorization server is for /mcp only.
				writeError(w, http.StatusForbidden, "token_scope", "An OAuth access token can only be used on /mcp.")
				return
			}
			if !tokenAllowed(rt) {
				writeError(w, http.StatusForbidden, "token_scope", "An API token cannot use this endpoint.")
				return
			}
			p, err := s.tokenPrincipal(r, secret)
			if err != nil {
				s.fail(w, err)
				return
			}
			if p.Grant != nil && !p.Grant.Write && tokenChanges(r) {
				writeError(w, http.StatusForbidden, "token_scope", "This token is read only.")
				return
			}
			c.p = p
			c.r = r.WithContext(space.ContextWithVia(r.Context(), space.ViaToken))
			// The api category: the call is recorded when it is over, if its
			// person's tenant has it on.
			var done func()
			if w, done = s.apiCalls(w, c, rt.pattern, p.User.TenantID); done != nil {
				c.w = w
				defer done()
			}
		} else if rt.access.kind != kindPublic || rt.optionalUser {
			p, err := s.principal(r)
			switch {
			case err == nil:
				c.p = p
			case rt.access.kind == kindPublic:
				// A visitor without a session is fine here; a broken database is not.
				var ae *auth.Error
				if !errors.As(err, &ae) {
					s.fail(w, err)
					return
				}
				c.ended = ae.Reason
			default:
				s.fail(w, err)
				return
			}
		}
		if c.p != nil && c.p.Held && !heldAllowed(c.p.HeldBy, rt.method+" "+rt.pattern) {
			if c.p.HeldBy == auth.HoldPassword {
				writeError(w, http.StatusForbidden, "password_change_required", "Choose a new password to continue.")
				return
			}
			writeError(w, http.StatusForbidden, "passkey_required", "Register a passkey to continue.")
			return
		}
		if !s.enterScope(c, rt) {
			return
		}
		if rt.access.kind == kindKey {
			// A route outside spaces acts in the acting tenant: the caller's
			// own, or the one the scope header names.
			if err := s.allow(c, rt.access.key, c.tenant()); err != nil {
				s.refuse(w, err)
				return
			}
		}
		// The route's feature comes before the space is opened: an evicted
		// space is not restored for a request that is refused anyway, and the
		// answer is the same for every space.
		if !s.featureGate(c, rt) {
			return
		}
		s.consoleOpened(c)
		if rt.access.kind == kindSpace {
			if !s.openSpace(c, rt) {
				return
			}
			defer c.sp.Release()
			s.withFileLimit(c)
		}
		rt.h(s, c)
	}
}

// indexOnlyRoutes are the routes whose answer comes from the index alone, so
// an evicted space (see "Eviction") answers them without being restored: lists,
// the tree, tags, links and backlinks, the problems of the pages. A listing
// that asks for bodies (body=1) reads files and is not one of them.
var indexOnlyRoutes = map[string]bool{
	"GET /api/spaces/{space}/pages":                true,
	"GET /api/spaces/{space}/pages/groups":         true,
	"GET /api/spaces/{space}/tree":                 true,
	"GET /api/spaces/{space}/tags":                 true,
	"GET /api/spaces/{space}/sources":              true,
	"GET /api/spaces/{space}/pages/{id}/backlinks": true,
	"GET /api/spaces/{space}/pages/{id}/links":     true,
	"GET /api/spaces/{space}/checks":               true,
	// The people search of a space's dialog of members reads no content.
	"GET /api/spaces/{space}/people": true,
	// The offline sync's catalogue and changes (4.1); its page bodies
	// (sync/pages) read files and restore the space as other content reads do.
	"GET /api/spaces/{space}/sync/catalog": true,
	"GET /api/spaces/{space}/changes":      true,
}

// indexOnly says whether this request can be answered by the index of an
// evicted space.
func indexOnly(rt route, r *http.Request) bool {
	if !indexOnlyRoutes[rt.method+" "+rt.pattern] {
		return false
	}
	if r.URL.Query().Get("body") != "" && r.URL.Query().Get("body") != "0" {
		return false
	}
	return true
}

// openSpace opens the {space} of a space route for the caller and checks that
// they hold the permission the route asks, leaving the handle in c.sp (the
// caller releases it). It answers the refusal and returns false otherwise.
func (s *server) openSpace(c *call, rt route) bool {
	w, r := c.w, c.r
	need := rt.access.need
	spaceID := r.PathValue("space")
	if g := c.p.Grant; g != nil {
		// A token reaches only the spaces it names, and never more than its
		// owner has there now: the owner's own membership decides, not the
		// administrator's way into spaces they are not a member of.
		if !g.Names(spaceID) {
			writeError(w, http.StatusForbidden, "token_scope", "This token does not name this space.")
			return false
		}
		if need == perm.Write && !g.Write {
			writeError(w, http.StatusForbidden, "token_scope", "This token is read only.")
			return false
		}
		var h *spaces.Handle
		var err error
		if indexOnly(rt, r) {
			h, err = s.spaces.OpenMemberIndex(r.Context(), c.p.User, spaceID)
		} else {
			h, err = s.spaces.OpenMember(r.Context(), c.p.User, spaceID)
		}
		if err != nil {
			s.fail(w, err)
			return false
		}
		h.Info.Perms &= g.Perms()
		c.sp = h
		if !h.Info.Perms.Has(need) {
			h.Release()
			c.sp = nil
			writeError(w, http.StatusForbidden, "no_permission", "Your role in this space does not allow that; it needs the "+need.String()+" permission.")
			return false
		}
		return true
	}
	self := rt.access.orSelf && r.PathValue("user") == c.p.User.ID
	var h *spaces.Handle
	var err error
	if self {
		// Acting on one's own membership (leaving) needs no open space.
		h, err = s.spaces.OpenMembership(r.Context(), c.p.User, spaceID)
	}
	if !self || errors.Is(err, spaces.ErrNotFound) {
		if indexOnly(rt, r) {
			h, err = s.spaces.OpenIndexAs(r.Context(), c.p.User, c.p.Access, spaceID)
		} else {
			h, err = s.spaces.OpenAs(r.Context(), c.p.User, c.p.Access, spaceID)
		}
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	c.sp = h
	if h.Info.ViaAdmin && rt.access.memberOnly {
		h.Release()
		c.sp = nil
		writeError(w, http.StatusForbidden, "takeover_required", "Only a member of the space may do that; an administrator takes the space over first.")
		return false
	}
	if h.Info.ViaAdmin {
		h.Info.Perms, h.Info.Viewing = s.withView(c, rt, h.Info.Perms, spaceID)
	}
	if self {
		need = perm.Read
	}
	if !h.Info.Perms.Has(need) {
		h.Release()
		c.sp = nil
		writeError(w, http.StatusForbidden, "no_permission", "Your role in this space does not allow that; it needs the "+need.String()+" permission.")
		return false
	}
	return true
}

// principal is who a request is from: the built-in user in single-user mode,
// else the owner of the session cookie.
func (s *server) principal(r *http.Request) (*auth.Principal, error) {
	if s.single {
		return &auth.Principal{User: s.local, Access: s.localAccess}, nil
	}
	ck, err := r.Cookie(s.auth.SessionCookieName())
	if err != nil {
		return nil, &auth.Error{Kind: auth.Unauthenticated, Msg: "Sign in to continue."}
	}
	return s.auth.Authenticate(r.Context(), ck.Value)
}

// pageID returns the {id} of the request, answering 404 for anything that
// cannot be a page id.
func (s *server) pageID(c *call) (string, bool) {
	id := c.r.PathValue("id")
	if !page.ValidID(id) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such page.")
		return "", false
	}
	return id, true
}
