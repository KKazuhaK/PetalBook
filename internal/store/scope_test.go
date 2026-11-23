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
// been found inside one).
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
	// --- inserts: the tenant is a column of the new row, chosen by the caller ----
	"InsertUser":   "the new row carries its tenant",
	"InsertSpace":  "the new row carries its tenant",
	"InsertGroup":  "the new row carries its tenant",
	"InsertInvite": "the new row carries its tenant",
	"InsertAudit":  "each row carries its tenant, or takes its user's",
	// --- the tenants table and the layers of settings are not a tenant's data -----
	"CountTenantRows": "counts the rows of one tenant to refuse deleting it",
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
		t.Errorf("%s touches users, spaces, groups, invites or audit_log and takes no Scope: give it one, or put it on the allowlist with the reason", m)
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

var sqlTable = regexp.MustCompile(`(?is)\b(FROM|JOIN|INTO|UPDATE)\s+(users|spaces|groups|invites|audit_log)\b`)
