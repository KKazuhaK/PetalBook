package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A function of this package that reads or writes one of the five tables a
// tenant owns (users, spaces, groups, invites, audit_log) takes a Scope, or is on the allowlist below with the reason it may look
// across tenants (or needs no scope because the row it works on has already
// been found inside one). The tables of 2.7 that hang off a user or a space
// (github_authorizations, github_installations, github_installation_tenants, github_leftovers, remotes) follow the same rule.
// scopeAllowlist is every exported function that touches the five tables
// without a Scope parameter. The comment of each is the reason. A function
// that gains a Scope, or is removed, must leave the list (the test says so), so
// the list cannot grow stale.
var scopeAllowlist = map[string]string{
	// --- the account decides the tenant: a sign-in, a session, a token, a CLI ---
	"GetUserByUsername":          "sign-in by username: usernames are unique on the server, and the account found decides the tenant",
	"FindUsableInvite":           "an invite by the hash of its secret token: the invite decides the tenant",
	"UseInvite":                  "marks the invite found by its secret token as used",
	"HasUsers":                   "whether the server has any account (setup)",
	"CountEnabledAdminsOutside":  "the rule 'keep one administrator' counts site administrators, the server's own",
	"CountEnabledPasswordAdmins": "the same rule for password sign-in: the server's own",
	"CountPasskeyRequired":       "a count for the security page, which is the server's own",
	"DeleteOldInvites":           "background job: expired invites of every tenant",
	"TrimAudit":                  "background job: the audit log of every tenant",
	"NextSpaceToMeasure":         "background job: the space measured longest ago, of any tenant",
	"DisabledSpaces":             "the server's set of disabled space ids, which is checked by id",
	// --- rows reached through a row that was already found in scope --------------
	"SpaceOwner":              "a space already opened by the caller",
	"ListMembers":             "members of a space already opened by the caller",
	"GetMember":               "a member of a space already opened by the caller",
	"CountOtherEnabledOwners": "owners of a space already opened by the caller",
	"LockSpace":               "a space already found in scope",
	"RenameSpace":             "a space already opened by the caller",
	"SetSpaceInbox":           "a space already opened by the caller",
	"ListUserSpaces":          "the caller's own spaces: memberships are same-tenant by construction",
	"EnsureLocal":             "single-user mode: the built-in user and space, in the default tenant",
	"SetSpaceSize":            "background measurement of a space by id",
	"SkipSpaceSize":           "background measurement of a space by id",
	"UpdateProfile":           "the caller's own account, or a user already found in scope",
	"SetEmail":                "a user already found in scope",
	"SetEmailVerified":        "a user already found in scope",
	"SetTermsAccepted":        "the account just made from a registration",
	"GetTermsAccepted":        "a user already found in scope",
	"EmailVerifiedBy":         "a user already found in scope (by the address, in the provider's tenant)",
	"SetPasskeyRequired":      "a user already found in scope",
	"SetRole":                 "a user already found in scope",
	"SetSiteAdmin":            "a user already found in scope",
	"SetDisabled":             "a user already found in scope",
	"UpdateGroup":             "a group already found in scope",
	"DeleteGroup":             "a group already found in scope",
	"GroupSourceOf":           "a group already found in scope",
	"ListGroupSpaces":         "a group already found in scope",
	"ListGroupPeople":         "a group already found in scope",
	"UserGroups":              "the groups of one person: memberships are same-tenant by construction",
	"GroupSources":            "groups by ids already read in scope",
	"LockGroup":               "a group already found in scope",
	"ListSpaceGroups":         "a space already opened by the caller",
	"UserSpaceGroups":         "a space already opened by the caller",
	"ListEffective":           "a space already opened by the caller",
	"ListUserAccess":          "the spaces one person reaches: same-tenant by construction",
	"ListHeldDrafts":          "background job: held drafts of every tenant",
	"SpaceDrafts":             "a space already found in scope, for its deletion",
	"PageDrafts":              "a space already opened",
	"LockFavorites":           "locks one user's row to serialise changes of their own favourites",
	// --- linking statements: the statement itself joins both rows on one tenant ---
	"AddMember":         "INSERT ... SELECT joining the space and the user on tenant_id",
	"AddSpaceGroup":     "INSERT ... SELECT joining the space and the group on tenant_id",
	"AddLocalMembers":   "INSERT ... SELECT joining the group and each person on tenant_id",
	"ApplySynced":       "INSERT ... SELECT joining the group and the person on tenant_id",
	"TransferOwnership": "INSERT ... SELECT joining the space and the new owner on tenant_id",
	"RestoreSpaceRows":  "INSERT ... SELECT joining the space and each person and group on tenant_id; the item was found in scope by the caller",
	"RecycledView":      "reads the records of an item already found in scope, and a person's name by id",
	// --- inserts: the tenant is a column of the new row, chosen by the caller ----
	"InsertUser":   "the new row carries its tenant",
	"InsertSpace":  "the new row carries its tenant",
	"InsertGroup":  "the new row carries its tenant",
	"InsertInvite": "the new row carries its tenant",
	"InsertAudit":  "each row carries its tenant, or takes its user's",
	// --- the tenants table and the layers of settings are not a tenant's data -----
	"CountTenantRows": "counts the rows of one tenant to refuse deleting it",
	// --- 2.7: GitHub. A connection is its user's, a remote its space's; the App and
	// its installations are the server's own (each is shared with chosen tenants, design.md 2.7) ---
	"GetGitHubAuthorization":       "one person's connection, by the id of a user already found in scope",
	"PutGitHubAuthorization":       "the caller's own connection; its GitHub account check is site-wide, as GitHubAccountUser's",
	"GitHubAccountUser":            "a GitHub account links to one PetalBook user on the whole server (github_id is unique); callers learn only that it is taken (account_in_use), never by whom or in which tenant",
	"ReplaceGitHubTokens":          "one person's connection, by the id of a user already found",
	"ExpireGitHubAuthorization":    "one person's connection, by the id of a user already found",
	"DeleteGitHubAuthorization":    "the caller's own connection, or that of a user being deleted",
	"DeleteGitHubAuthorizations":   "the App was removed (site administrators only): every connection on the server goes with it",
	"ListGitHubAuthorizations":     "the App was removed (site administrators only): every connection on the server is revoked",
	"DueGitHubRenewals":            "background job: the daily renewal of the connections of every tenant",
	"CountGitHubAuthorizations":    "the App's own count, shown to site administrators only",
	"CountGitHubSpaces":            "the App's own count, shown to site administrators only",
	"CountGitSpaces":               "the count of plain-address remotes, shown to site administrators only",
	"GitHubSpaceCounts":            "the App's own count per installation, shown to site administrators only",
	"CountUserGitHubSpaces":        "the spaces syncing through one person's connection: same-tenant, as connecting checks",
	"GetGitHubInstallation":        "an installation of the server's App: the server's own",
	"PutGitHubInstallation":        "an installation of the server's App, marked by a site administrator",
	"UserTenant":                   "the tenant of a user already found, to scope what they may use of the App",
	"PutGitHubLeftover":            "a reminder of the caller's own, by the id of a user already found",
	"ListGitHubLeftovers":          "the caller's own reminders, by the id of a user already found",
	"DeleteGitHubLeftover":         "the caller's own reminder, by the id of a user already found",
	"SetGitHubInstallationTenants": "a site administrator chooses the tenants an installation is shared with; the tenants are checked by the caller",
	"GitHubInstallationTenants":    "which tenants installations are shared with, shown to site administrators only",
	"DeleteGitHubInstallations":    "the App was removed (site administrators only): the shares go with the installations",
	"MemberStanding":               "one person's roles on a space already found: memberships are same-tenant by construction",
	"GetRemote":                    "the remote of a space already opened by the caller",
	"PutRemote":                    "the remote of a space already opened, or one being made in the caller's tenant",
	"UpdateRemoteStatus":           "the remote of an open space, by its id",
	"DeleteRemote":                 "the remote of a space already opened by the caller",
	"RemoteStates":                 "the server's map of sync states, read by space id",
	// --- 3.1: server storage. The disk, its policy and the cache of clones are the
	// server's own (site administrators only, design.md 3.3's table); a space's
	// records are reached by the id of a space already found ---
	"GetSpaceStorage":          "the storage record of a space already found, by its id",
	"TouchSpaceUsed":           "records the use of a space already opened, by its id",
	"SetSpaceCache":            "the cache state of a space being evicted or restored, by its id (the eviction is the server's)",
	"SetSpaceCacheKept":        "the cache state of an open space, by its id",
	"SetSpacePinned":           "a site administrator keeps a space on the server's disk",
	"SetSpaceExempt":           "a site administrator exempts a space from the site's requirement of a remote",
	"SetSpaceIndexBytes":       "the size of the index of a space being evicted, by its id",
	"SetRemoteExempt":          "a site administrator exempts a person already found in scope from the site's requirement of a remote",
	"SpacesInCacheState":       "the server's map of cache states, read by space id",
	"GetPolicyFacts":           "what the site's requirement of a remote looks at in a space already found, by its id",
	"PolicyCounts":             "the site's requirement of a remote across the server, shown to site administrators only",
	"SumStorage":               "the server's disk, shown to site administrators only",
	"EvictionCandidates":       "background job: the eviction of idle spaces of every tenant from the server's disk",
	"CloneBytesOfRemoteSpaces": "background job: the server's disk budget for clones",
	"PinnedSpaceIDs":           "background job: the spaces kept on the server's disk, of every tenant",
	"PutAssetRemote":           "the attachment repository of a space already opened, or one being made in the caller's tenant",
}

func TestEveryStoreMethodThatTouchesTenantTablesTakesAScope(t *testing.T) {
	fset := token.NewFileSet()
	files, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := files["store"]
	if pkg == nil {
		t.Fatal("package store not found")
	}
	var missing, stale []string
	seen := map[string]bool{}
	for name, f := range pkg.Files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Body == nil {
				continue
			}
			if fn.Recv != nil {
				continue // methods of Store: tenants.go (the tenants table) and plumbing
			}
			if !touches(fn.Body) {
				continue
			}
			seen[fn.Name.Name] = true
			hasScope := false
			for _, p := range fn.Type.Params.List {
				if id, ok := p.Type.(*ast.Ident); ok && id.Name == "Scope" {
					hasScope = true
				}
			}
			_, allowed := scopeAllowlist[fn.Name.Name]
			switch {
			case hasScope && allowed:
				stale = append(stale, fn.Name.Name+" takes a Scope and is still on the allowlist")
			case !hasScope && !allowed:
				missing = append(missing, fn.Name.Name)
			}
		}
	}
	for name := range scopeAllowlist {
		if !seen[name] && scopeAllowlist[name] != "" {
			stale = append(stale, name+" is on the allowlist but touches none of the tenant tables, or is gone")
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, m := range missing {
		t.Errorf("%s touches users, spaces, groups, invites, audit_log or a GitHub table (github_authorizations, github_installations, github_installation_tenants, github_leftovers, remotes) and takes no Scope: give it one, or put it on the allowlist with the reason", m)
	}
	for _, s := range stale {
		t.Error(s)
	}
}

// touches reports whether a function body has a string literal (a query) that
// names one of the tenant tables as a table (after FROM, JOIN, INTO, UPDATE).
func touches(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if sqlTable.MatchString(s) {
			found = true
		}
		return true
	})
	return found
}

// The tables of 2.7 count as well: a GitHub connection is a user's and a remote
// a space's, so a function that reaches them across users needs a reason.
var sqlTable = regexp.MustCompile(`(?is)\b(FROM|JOIN|INTO|UPDATE)\s+(users|spaces|groups|invites|audit_log|github_authorizations|github_installations|github_installation_tenants|github_leftovers|remotes)\b`)
