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
// tenant owns (users, spaces, groups, invites, audit_log; and space_measure, the queue of
// the background measurement, whose rows carry the space's tenant) takes a Scope, or is on the allowlist below with the reason it may look
// across tenants (or needs no scope because the row it works on has already
// been found inside one). The tables of 2.7 that hang off a user or a space
// (github_authorizations, github_installations, github_installation_tenants, github_leftovers, providers, provider_authorizations, provider_leftovers, remotes) follow the same rule.
// scopeAllowlist is every exported function that touches the five tables
// without a Scope parameter. The comment of each is the reason. A function
// that gains a Scope, or is removed, must leave the list (the test says so), so
// the list cannot grow stale.
var scopeAllowlist = map[string]string{
	// --- the account decides the tenant: a sign-in, a session, a token, a CLI ---
	"GetUserByUsername":          "sign-in by username: usernames are unique on the server, and the account found decides the tenant",
	"FindUsableInvite":           "an invite by the hash of its secret token: the invite decides the tenant",
	"UseInvite":                  "marks the invite found by its secret token as used",
	"SetUsername":                "the caller has found the person by the administrator's scope (or is the person) before it renames them",
	"HasUsers":                   "whether the server has any account (setup)",
	"CountEnabledAdminsOutside":  "the rule 'keep one administrator' counts site administrators, the server's own",
	"CountEnabledPasswordAdmins": "the same rule for password sign-in: the server's own",
	"CountPasskeyRequired":       "a count for the security page, which is the server's own",
	"DeleteOldInvites":           "background job: expired invites of every tenant",
	"NotifyUser":                 "a notification for one person by id: the row carries the person's own tenant, read here",
	"TrimAudit":                  "background job: the audit log of every tenant",
	"DisabledSpaces":             "the server's set of disabled space ids, which is checked by id",
	"EndTenantSessions":          "a site administrator disabling a tenant: the tenant is the one named, its people's sessions end",
	"DeleteTenantSessionEnds":    "a site administrator enabling a tenant again: the tenant is the one named",
	// --- rows reached through a row that was already found in scope --------------
	"LockSpace":               "locks the row of a space already found in scope, by id",
	"LockGroup":               "locks the row of a group already found in scope, by id",
	"LockFavorites":           "locks the row of a person already found in scope, by id",
	"NextSpaceToMeasure":      "background job: the queue of the measurement, of every tenant",
	"SpaceOwner":              "a space already opened by the caller",
	"ListMembers":             "members of a space already opened by the caller",
	"GetMember":               "a member of a space already opened by the caller",
	"CountOtherEnabledOwners": "owners of a space already opened by the caller",
	"RenameSpace":             "a space already opened by the caller",
	"SetSpaceInbox":           "a space already opened by the caller",
	"ListUserSpaces":          "the caller's own spaces: memberships are same-tenant by construction",
	"EnsureLocal":             "single-user mode: the built-in user and space, in the default tenant",
	"SetSpaceSize":            "background measurement of a space by id",
	"UpdateProfile":           "the caller's own account, or a user already found in scope",
	"SetPrimaryEmail":         "a user already found in scope; the address is unique on the whole server, so its holder is looked for everywhere",
	"SetEmailRowVerified":     "a user already found in scope",
	"UserEmails":              "a user already found in scope",
	"EmailOwner":              "an address belongs to one account on the whole server (3.6): the lookup is site-wide by design",
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
	"ListSpaceGroups":         "a space already opened by the caller",
	"UserSpaceGroups":         "a space already opened by the caller",
	"ListEffective":           "a space already opened by the caller",
	"ListUserAccess":          "the spaces one person reaches: same-tenant by construction",
	"ListHeldDrafts":          "background job: held drafts of every tenant",
	"SpaceDrafts":             "a space already found in scope, for its deletion",
	"PageDrafts":              "a space already opened",
	// --- linking statements: the statement itself joins both rows on one tenant ---
	"AddMember":          "INSERT ... SELECT joining the space and the user on tenant_id",
	"AddSpaceGroup":      "INSERT ... SELECT joining the space and the group on tenant_id",
	"AddLocalMembers":    "INSERT ... SELECT joining the group and each person on tenant_id",
	"ApplySynced":        "INSERT ... SELECT joining the group and the person on tenant_id",
	"TransferOwnership":  "INSERT ... SELECT joining the space and the new owner on tenant_id",
	"RestoreSpaceRows":   "INSERT ... SELECT joining the space and each person and group on tenant_id; the item was found in scope by the caller",
	"RecycledView":       "reads the records of an item already found in scope, and a person's name by id",
	"GetUserAny":         "the recycle bin and the audit log name a person who is deleted; the caller found them through an item in scope",
	"GetUserState":       "whether a person is deleted, for the recycle bin's own steps, by the id of an item already found in scope",
	"PurgeUserData":      "the purge of a person's item, which the caller found in scope; or the server's own job",
	"RestoreUserRows":    "the restore of a person's item, which the caller found in scope",
	"TenantsOf":          "the tenant of each id a bulk job works on, for its queue; the ids were checked in the caller's scope when the job was confirmed",
	"LeaveSpace":         "one person's membership of a space already found, by ids; reads the other owners of that space",
	"BeginUserDeletion":  "the record of a deletion of a person the caller found in scope (GetUser), by id",
	"NoteUserDeletion":   "the record of a deletion of a person the caller found in scope, by id",
	"ForgetUserDeletion": "the record of a deletion of a person the caller found in scope, by id",
	"SweepTombstones":    "background job: the rows of purged people that nothing refers to, of every tenant",
	// --- inserts: the tenant is a column of the new row, chosen by the caller ----
	"InsertUser":   "the new row carries its tenant",
	"InsertSpace":  "the new row carries its tenant",
	"InsertGroup":  "the new row carries its tenant",
	"InsertInvite": "the new row carries its tenant",
	"InsertAudit":  "each row carries its tenant, or takes its user's",
	// --- the tenants table and the layers of settings are not a tenant's data -----
	"CountTenantRows": "counts the rows of one tenant to refuse deleting it",
	// --- 2.7: GitHub. A connection is its user's, a remote its space's; the App and
	// its installations are the server's own (each is shared with chosen tenants, docs/design/milestones/phase-2-connections.md, 2.7) ---
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
	// --- 3.8: GitLab and Gitea providers. A provider is the server's own (or its
	// tenant's: owner), set up by site administrators; a connection and a reminder are
	// their user's; the account link is unique per provider on the whole server ---
	"ListConnProviderRows":             "the server's providers, loaded at start and shown to site administrators; visibility by owner is the registry's rule",
	"GetConnProviderRow":               "a provider by its id, for administrators and the provider's own reads",
	"CountConnProviderRows":            "how many providers an owner has, for the limit",
	"InsertConnProviderRow":            "a provider set up by a site administrator",
	"UpdateConnProviderRow":            "a provider changed by a site administrator",
	"SetConnProviderState":             "what a check of a provider's host found, written only to the application it was made with",
	"ExpireProviderAuthorizations":     "the provider's application was replaced by a site administrator: every connection to it ends",
	"SetConnProviderPartial":           "what a check of a provider's host found",
	"GetProviderAuthorization":         "one person's connection, by the id of a user already found in scope",
	"ProviderAccountUser":              "an account on a host links to one PetalBook user on the whole server; callers learn only that it is taken (account_in_use)",
	"ReplaceProviderTokens":            "one person's connection, by the id of a user already found",
	"ExpireProviderAuthorization":      "one person's connection, by the id of a user already found",
	"DeleteUserProviderAuthorizations": "a person being deleted: their connections go with them",
	"DeleteProviderAuthorization":      "the caller's own connection, or that of a user being deleted",
	"DueProviderRenewals":              "background job: the daily renewal of the connections of every tenant",
	"ListProviderAuthorizations":       "the provider was removed (site administrators only): every connection to it is revoked",
	"CountProviderAuthorizations":      "the provider's own count, shown to site administrators only",
	"CountProviderSpaces":              "the provider's own count, shown to site administrators only",
	"CountUserProviderSpaces":          "the spaces syncing through one person's connection: same-tenant, as connecting checks",
	"PutProviderLeftover":              "a reminder of the caller's own, by the id of a user already found",
	"ListProviderLeftovers":            "the caller's own reminders, by the id of a user already found",
	"DeleteProviderLeftover":           "the caller's own reminder, by the id of a user already found",
	"MemberStanding":                   "one person's roles on a space already found: memberships are same-tenant by construction",
	"GetRemote":                        "the remote of a space already opened by the caller",
	"PutRemote":                        "the remote of a space already opened, or one being made in the caller's tenant",
	"UpdateRemoteStatus":               "the remote of an open space, by its id",
	"DeleteRemote":                     "the remote of a space already opened by the caller",
	"RemoteStates":                     "the server's map of sync states, read by space id",
	// --- 3.1: server storage. The disk, its policy and the cache of clones are the
	// server's own (site administrators only, docs/design/milestones/phase-3-tenants.md, the 3.3 table); a space's
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
	"SpaceTenants":             "the tenant of each space an in-memory cache names, by id",
	"PinnedSpaceIDs":           "background job: the spaces kept on the server's disk, of every tenant",
	"PutAssetRemote":           "the attachment repository of a space already opened, or one being made in the caller's tenant",
}

func TestEveryStoreMethodThatTouchesTenantTablesTakesAScope(t *testing.T) {
	t.Parallel()
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
		t.Errorf("%s touches users, spaces, groups, invites, audit_log or a GitHub table (github_authorizations, github_installations, github_installation_tenants, github_leftovers, providers, provider_authorizations, provider_leftovers, remotes) or space_measure and takes no Scope: give it one, or put it on the allowlist with the reason", m)
	}
	for _, s := range stale {
		t.Error(s)
	}
}

// touches reports whether a function body has a string literal (a query) that
// names one of the tenant tables as a table (after FROM, JOIN, INTO, UPDATE), or
// calls lockRow with the name of one: lockRow builds its query from the table
// it is given, so the table is read from the call.
func touches(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "lockRow" && len(call.Args) >= 3 {
				if lit, ok := call.Args[2].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if table, err := strconv.Unquote(lit.Value); err == nil && sqlTable.MatchString("FROM "+table) {
						found = true
					}
				}
			}
			return true
		}
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

// space_measure (the queue of the background measurement) is a tenant's too:
// each row carries the space's tenant_id.
// The tables of 2.7 count as well: a GitHub connection is a user's and a remote
// a space's, so a function that reaches them across users needs a reason.
var sqlTable = regexp.MustCompile(`(?is)\b(FROM|JOIN|INTO|UPDATE)\s+(users|spaces|groups|invites|audit_log|github_authorizations|github_installations|github_installation_tenants|github_leftovers|providers|provider_authorizations|provider_leftovers|remotes|space_measure)\b`)

// A function that reads people leaves out the ones in the recycle bin
// (docs/api.md, "Deleting a user": a deleted person is invisible). A query on the
// users table that does not say deleted_at is on this list with the reason.
var deletedAllowlist = map[string]string{
	"EndTenantSessions":       "ends every session of a tenant's people when it is disabled; ending the (already ended) sessions of a deleted person too is harmless",
	"DeleteTenantSessionEnds": "forgets a tenant's `disabled` records when it is enabled again; a deleted person's records go with the rest",
	"GetUserAny":              "names a deleted person on purpose: the recycle bin and the audit log show them",
	"GetUserState":            "reads whether a person is deleted",
	"PlanUserDeletion":        "its first read is GetUser; the other queries are of people who are members of spaces of the person, filtered where it matters",
	"FinishUserDeletion":      "marks the person: it reads and writes a row that is not deleted yet",
	"RestoreUserRows":         "restores a deleted person",
	"PurgeUserData":           "purges a deleted person",
	"userDeletionPending":     "the record of a deletion under way, of a person who is not deleted yet (or who was just marked, in the same transaction)",
	"SweepTombstones":         "the sweep of purged people",
	"RecycledView":            "the name of the author of a draft kept in an item",
	"RecordFolderMove":        "",
	"HasUsers":                "whether the server has any account at all (setup): a deleted person's row counts",
	"UpdateProfile":           "reads the address of the account it was given, by id, to keep who vouched for it",
	"GetPolicyFacts":          "whether an owner of a space is exempt: the owners of a live space are not deleted",
	"PolicyCounts":            "the same, counted for the whole server",
	"policyConds":             "the same, as a condition",
	"ListAllSpaces":           "the owner of each space of a page: the owners of a live space are not deleted",
	"queryAdminSpaces":        "the owner of each space of a page: the owners of a live space are not deleted",
	"deadlineCond":            "whether an owner of a space is exempt: the owners of a live space are not deleted",
	"SettleDeadlines":         "whether an owner of a space is exempt: the owners of a live space are not deleted",
	"InsertAudit":             "the tenant of the person an audit row is about, by id: the log names deleted people too",
	"ofUser":                  "the condition of a person's tenant, for the lists of tokens and grants (a deleted person's are revoked)",
	"UserTenant":              "the tenant of a person by id, for what they may use of the GitHub App; a deleted person has no connection left",
	"ListMembers":             "the members of a space: a deleted person has none left",
	"GetMember":               "a member of a space: a deleted person has none left",
	"SpaceOwner":              "the owner of a space already opened: a deleted person has none left",
	"CountOtherEnabledOwners": "owners of a space already opened: a deleted person has none left",
	"MemberStanding":          "the roles of a person's memberships: a deleted person has none left",
	"ListUserSpaces":          "the spaces of a person by their own memberships: a deleted person has none left",
	"UserHasIdentityOf":       "an identity of a person by id",
	"GetTermsAccepted":        "the person's own record, by id",
	"SetTermsAccepted":        "the person's own record, by id",
	"EmailVerifiedBy":         "the person's own record, by id",
	"SetEmail":                "a person already found",
	"UserQuota":               "a person already found",
	"SetUserQuota":            "a person already found",
	"UserUsageRows":           "the usage rows of people, read to put them right: a deleted person has one until they are purged",
	"RecountUsage":            "puts the usage rows right: it counts the tenant's people without the deleted",
	"CountEnabledAdmins":      "counts enabled administrators: through CountEnabledAdminsOutside, which leaves the deleted out",
	"DisabledSpaces":          "",
	"ListHeldDrafts":          "held drafts of people: a deleted person's are purged with them",
	"PageDrafts":              "the drafts of a page, with the names of their authors",
	"SpaceDrafts":             "the drafts of a space, with the names of their authors",
	"ListAPITokens":           "tokens with their owners' names: a deleted person's are revoked",
	"ListOAuthGrants":         "grants with their owners' names: a deleted person's are revoked",
	"ListUserUsage":           "",
	"GitHubSpaceCounts":       "",
	"RemoteUserState":         "",
}

func TestEveryReadOfPeopleLeavesOutTheDeleted(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := files["store"]
	reads := regexp.MustCompile(`(?is)\bSELECT\b.*\b(FROM|JOIN)\s+users\b`)
	var missing []string
	for name, f := range pkg.Files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			reading, filtered := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if reads.MatchString(s) {
					reading = true
				}
				if strings.Contains(s, "deleted_at") {
					filtered = true
				}
				return true
			})
			if reading && !filtered {
				if _, ok := deletedAllowlist[fn.Name.Name]; !ok {
					missing = append(missing, fn.Name.Name)
				}
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s reads the users table and does not leave out deleted_at: add the condition, or put it on deletedAllowlist with the reason", m)
	}
}

// touches must see a table through a query and through lockRow, and must not
// see one that is not a tenant table: the test above stands on it.
func TestScopeTestSeesTenantTables(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		src  string
		want bool
	}{
		{`SELECT 1 FROM spaces WHERE id = ?`, true},
		{`UPDATE space_measure SET size_at = 1`, true},
		{`INSERT INTO space_measure(space_id) VALUES (1)`, true},
		{`SELECT 1 FROM site_settings`, false},
	} {
		if got := touchesSrc(t, "_ = `"+c.src+"`"); got != c.want {
			t.Errorf("%s: touches = %v", c.src, got)
		}
	}
	for _, c := range []struct {
		call string
		want bool
	}{
		{`lockRow(ctx, q, "spaces", id)`, true},
		{`lockRow(ctx, q, "groups", id)`, true},
		{`lockRow(ctx, q, "users", id)`, true},
		{`lockRow(ctx, q, "site_settings", id)`, false},
		{`other(ctx, q, "spaces", id)`, false},
	} {
		if got := touchesSrc(t, c.call); got != c.want {
			t.Errorf("%s: touches = %v", c.call, got)
		}
	}
}

func touchesSrc(t *testing.T, stmt string) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package store\nfunc F() {\n"+stmt+"\n}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	return touches(f.Decls[0].(*ast.FuncDecl).Body)
}
