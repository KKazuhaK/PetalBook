package api_test

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// The isolation fixture of milestone 3.3: two tenants holding the same shapes
// of data, and every route of the API called by tenant A's administrator, member
// and API token with tenant B's ids. Each answer must be the one for an id that
// does not exist, and no list may hold anything of B.

// tenFx is the fixture.
type tenFx struct {
	a    *acc
	root *client // a site administrator (tenant A, the default one)

	// tenant A's people
	admA, memA, tokA *client
	// tenant B's
	admB, memB *client

	tenB                                           string
	userB, spaceB, groupB, inviteB, tokenB, grantB string
	userA, spaceA, groupA, inviteA                 string
	secretB                                        string

	// syncedB is a synced group of B that a provider of B maps (addMappedB).
	syncedB string
	// cursorB is a change cursor of spaceB, as its member's device holds it.
	cursorB string
	// itemB is an item of B's recycle bin and exportB an export of B's.
	itemB, exportB, jobB string
}

// fakeID is an id of the right form that nothing has.
const fakeID = "0000000ZZZ"

func newTenFx(t *testing.T, a *acc) *tenFx {
	t.Helper()
	ctx := context.Background()
	f := &tenFx{a: a}
	f.root = a.setup("root", pw)
	f.root.putSetting("features.tenancy", true).want(t, 200, "")

	// Tenant B, with its first administrator through the invite the creation gives.
	r := f.root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme", "admin": map[string]any{"username": "adm-b"}}).want(t, 201, "")
	f.tenB = r.body["id"].(string)
	inv := r.body["invite"].(map[string]any)
	if inv["tenant"] != f.tenB || inv["siteAdmin"] != false || inv["role"] != "admin" {
		t.Fatalf("%v", inv)
	}
	f.admB = a.newClient("")
	f.admB.do("POST", "/api/invites/accept", map[string]any{"token": inv["token"], "username": "adm-b", "name": "Admin B", "password": pw}).want(t, 201, "")
	ri := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
	f.memB = a.newClient("")
	f.memB.do("POST", "/api/invites/accept", map[string]any{"token": ri.body["token"], "username": "mem-b", "name": "Member B", "password": pw}).want(t, 201, "")
	f.inviteB = f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["id"].(string)

	// Tenant A: the site administrator is in it too; a tenant administrator and a member.
	if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "adm-a", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
		t.Fatal(err)
	}
	f.admA = a.newClient("")
	f.admA.do("POST", "/api/login", map[string]any{"username": "adm-a", "password": pw}).want(t, 200, "")
	f.memA = a.addUser("mem-a", pw, "member")
	f.inviteA = f.admA.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["id"].(string)

	f.userA, f.userB = mustUserID(t, a, "mem-a"), mustUserID(t, a, "mem-b")
	f.spaceA = f.admA.do("POST", "/api/spaces", map[string]any{"name": "Space A"}).want(t, 201, "").body["id"].(string)
	f.spaceB = f.admB.do("POST", "/api/spaces", map[string]any{"name": "Space B"}).want(t, 201, "").body["id"].(string)
	f.admA.do("POST", "/api/spaces/"+f.spaceA+"/members", map[string]any{"username": "mem-a", "role": "editor"}).want(t, 201, "")
	f.admB.do("POST", "/api/spaces/"+f.spaceB+"/members", map[string]any{"username": "mem-b", "role": "editor"}).want(t, 201, "")
	f.groupA = f.admA.mkGroup(t, "Group A", "local")
	f.groupB = f.admB.mkGroup(t, "Group B", "local")
	f.groupAdd(t, f.admA, f.groupA, f.userA)
	f.groupAdd(t, f.admB, f.groupB, f.userB)
	f.admA.do("POST", "/api/spaces/"+f.spaceA+"/groups", map[string]any{"groupId": f.groupA, "role": "viewer"}).want(t, 201, "")
	f.admB.do("POST", "/api/spaces/"+f.spaceB+"/groups", map[string]any{"groupId": f.groupB, "role": "viewer"}).want(t, 201, "")

	// Tokens: A's member has one; B's has one (with its secret, for the tokens' own tests).
	ra := f.memA.do("POST", "/api/account/tokens", map[string]any{"name": "a", "access": "read", "spaces": []string{f.spaceA}}).want(t, 201, "")
	f.tokA = a.newClient("")
	f.tokA.headers["Authorization"] = "Bearer " + ra.body["secret"].(string)
	rb := f.memB.do("POST", "/api/account/tokens", map[string]any{"name": "b", "access": "read", "spaces": []string{f.spaceB}}).want(t, 201, "")
	f.tokenB = rb.body["token"].(map[string]any)["id"].(string)
	f.secretB = rb.body["secret"].(string)

	// An OAuth grant of B's member.
	now := a.clock.Now()
	f.grantB = "GRANTB0001"
	if err := store.InsertOAuthClient(ctx, a.st.Q(), store.OAuthClient{ID: "CLIENTB001", Name: "client", RedirectURIs: []string{"https://c.example/cb"}, Fingerprint: "fp-b", Created: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertOAuthGrant(ctx, a.st.Q(), store.OAuthGrant{ID: f.grantB, UserID: f.userB, ClientID: "CLIENTB001", ClientName: "client B", Access: "read",
		Resource: "https://notes.example.test/mcp", Created: now, LastUsed: now, RefreshHash: "rh", Spaces: []store.TokenSpace{{ID: f.spaceB}}}); err != nil {
		t.Fatal(err)
	}
	// A change cursor of B's space, as B's member's device would hold one.
	f.cursorB = f.memB.do("GET", "/api/spaces/"+f.spaceB+"/sync/catalog", nil).want(t, 200, "").body["changes"].(string)
	// B's recycle bin and B's exports (3.5).
	spaceB2 := f.admB.do("POST", "/api/spaces", map[string]any{"name": "Space B2"}).want(t, 201, "").body["id"].(string)
	f.itemB = f.admB.do("DELETE", "/api/admin/spaces/"+spaceB2, nil).want(t, 200, "").body["id"].(string)
	f.exportB = f.admB.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{f.spaceB}}).want(t, 202, "").body["job"].(map[string]any)["id"].(string)
	tok := f.admB.do("POST", "/api/admin/confirm", map[string]any{"action": "export", "spaces": []string{f.spaceB}}).want(t, 200, "").body["confirm"].(string)
	f.jobB = f.admB.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 202, "").body["job"].(map[string]any)["id"].(string)
	a.auth.FlushAudit(ctx)
	return f
}

func (f *tenFx) groupAdd(t *testing.T, c *client, g string, ids ...string) {
	t.Helper()
	c.groupAdd(t, g, ids...).want(t, 200, "")
}

// bIDs are everything of tenant B that must never show in an answer to A.
func (f *tenFx) bIDs() []string {
	l := []string{f.tenB, f.userB, f.spaceB, f.groupB, f.inviteB, f.tokenB, f.grantB, "adm-b", "mem-b", "Space B", "Group B", "Acme", f.secretB, f.itemB, f.exportB, f.jobB, "Space B2"}
	if f.syncedB != "" {
		l = append(l, f.syncedB, "Synced B")
	}
	return l
}

// addMappedB gives B a synced group that a provider of B (acme-sso) maps.
func (f *tenFx) addMappedB(t *testing.T, idp *fakeIDP) {
	t.Helper()
	f.syncedB = f.admB.mkGroup(t, "Synced B", "synced")
	f.root.do("POST", "/api/admin/providers", idp.providerBody("acme-sso", map[string]any{"tenant": f.tenB})).want(t, 201, "")
	f.root.do("PATCH", "/api/admin/providers/acme-sso", map[string]any{"groups": map[string]any{"enabled": true, "map": gmap("staff", f.syncedB)}}).want(t, 200, "")
}

// mappingsOfB is how many entries of acme-sso's map there are.
func (f *tenFx) mappingsOfB(t *testing.T) int {
	t.Helper()
	g := f.root.do("GET", "/api/admin/providers/acme-sso", nil).want(t, 200, "").body["groups"].(map[string]any)
	return len(g["map"].([]any))
}

// --- the table of routes -----------------------------------------------------------

type isoKind int

const (
	isoParam  isoKind = iota + 1 // an id of the path or body: B's id must answer as a made-up one
	isoList                      // a list: nothing of B in it, and ?tenant=B as for a made-up tenant
	isoSite                      // the server's own: a tenant administrator is refused
	isoExempt                    // nothing of another tenant can be named, with the reason
	isoEcho                      // ids of the query that are answered back: B's id must answer exactly as a made-up one
)

type isoEntry struct {
	kind   isoKind
	reason string
	// path and body build the request for an id (B's, or a made-up one); nil
	// uses the pattern's parameters by name.
	body func(f *tenFx, id string) any
	ids  func(f *tenFx) map[string]string // parameters of the path (name -> B's id)
	// query is added to the path, the same for B's id and a made-up one.
	query string
	// queryOf, for isoEcho, is the query for an id (B's, or a made-up one).
	queryOf func(f *tenFx, id string) string
}

// isolationRoutes has an entry for every route that is not a space route (those
// are all isoParam by their {space}; an entry here for one only adds its query).
// A route added to the API without an entry here fails
// TestEveryRouteIsInTheIsolationTable.
var isolationRoutes = map[string]isoEntry{
	"GET /api/meta":                        {kind: isoList},
	"GET /api/session":                     {kind: isoExempt, reason: "the caller's own user"},
	"GET /api/admin/permissions":           {kind: isoExempt, reason: "the registry of permission keys: the same for every tenant"},
	"POST /api/setup":                      {kind: isoExempt, reason: "public: nothing of a tenant"},
	"POST /api/login":                      {kind: isoExempt, reason: "public: the account that signs in decides the tenant"},
	"POST /api/logout":                     {kind: isoExempt, reason: "the caller's own session"},
	"POST /api/auth/oidc/start":            {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/oidc/callback":          {kind: isoExempt, reason: "public sign-in"},
	"POST /api/invites/check":              {kind: isoExempt, reason: "the secret token of an invite is the capability, in any tenant"},
	"POST /api/invites/accept":             {kind: isoExempt, reason: "the secret token of an invite is the capability, in any tenant"},
	"GET /api/auth/providers":              {kind: isoExempt, reason: "public; ?tenant= is tested apart"},
	"GET /api/captcha":                     {kind: isoExempt, reason: "public"},
	"GET /api/register":                    {kind: isoExempt, reason: "public: names a tenant by slug and shows only its public rules"},
	"POST /api/register":                   {kind: isoExempt, reason: "public: makes a pending registration in the tenant named by slug; tested in the registration tests"},
	"POST /api/register/confirm":           {kind: isoExempt, reason: "the secret token of a registration is the capability"},
	"POST /api/email/confirm":              {kind: isoExempt, reason: "the secret token of the link is the capability"},
	"POST /api/password/forgot":            {kind: isoExempt, reason: "public: answers the same whatever exists; mail goes to the account's own address"},
	"POST /api/password/reset/check":       {kind: isoExempt, reason: "the secret token of the link is the capability"},
	"POST /api/password/reset":             {kind: isoExempt, reason: "the secret token of the link is the capability"},
	"GET /api/auth/{kind}/{slug}/start":    {kind: isoExempt, reason: "public sign-in"},
	"POST /api/auth/{kind}/{slug}/start":   {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/oidc/{slug}/callback":   {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/github/{slug}/callback": {kind: isoExempt, reason: "public sign-in"},
	"POST /api/auth/saml/{slug}/acs":       {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/saml/{slug}/metadata":   {kind: isoExempt, reason: "public"},
	"POST /api/auth/passkey/begin":         {kind: isoExempt, reason: "public sign-in"},
	"POST /api/auth/passkey/finish":        {kind: isoExempt, reason: "public sign-in"},

	"GET /api/account":                                  {kind: isoExempt, reason: "the caller's own account"},
	"PATCH /api/account":                                {kind: isoExempt, reason: "the caller's own account"},
	"POST /api/account/password":                        {kind: isoExempt, reason: "the caller's own account"},
	"POST /api/account/email/verify":                    {kind: isoExempt, reason: "the caller's own account"},
	"POST /api/account/emails":                          {kind: isoExempt, reason: "the caller's own account (an address is unique on the whole server, whatever the tenant)"},
	"POST /api/account/emails/verify":                   {kind: isoExempt, reason: "the caller's own account"},
	"POST /api/account/emails/primary":                  {kind: isoExempt, reason: "the caller's own account"},
	"DELETE /api/account/emails/{email}":                {kind: isoExempt, reason: "the caller's own account"},
	"GET /api/account/links":                            {kind: isoList},
	"POST /api/account/links/{provider}/start":          {kind: isoExempt, reason: "the caller's own account; the provider is the server's"},
	"POST /api/account/github/install":                  {kind: isoExempt, reason: "the caller's own installations: their owner is the person, and another owner's id answers 404 (github_installs_test.go)"},
	"GET /api/account/github/installations":             {kind: isoExempt, reason: "the caller's own installations: their owner is the person, and another owner's id answers 404 (github_installs_test.go)"},
	"GET /api/account/github/installations/{id}/spaces": {kind: isoExempt, reason: "the caller's own installations: their owner is the person, and another owner's id answers 404 (github_installs_test.go)"},
	"GET /api/account/github/installations/{id}/repos":  {kind: isoExempt, reason: "the caller's own installations: their owner is the person, and another owner's id answers 404 (github_installs_test.go)"},
	"DELETE /api/account/github/installations/{id}":     {kind: isoExempt, reason: "the caller's own installations: their owner is the person, and another owner's id answers 404 (github_installs_test.go)"},
	"POST /api/admin/github/install":                    {kind: isoExempt, reason: "the acting tenant's own installations (c.tenant()): another tenant's id answers 404 (github_installs_test.go)"},
	"GET /api/admin/github/installations":               {kind: isoExempt, reason: "the acting tenant's own installations (c.tenant()): another tenant's id answers 404 (github_installs_test.go)"},
	"GET /api/admin/github/installations/{id}/spaces":   {kind: isoExempt, reason: "the acting tenant's own installations (c.tenant()): another tenant's id answers 404 (github_installs_test.go)"},
	"GET /api/admin/github/installations/{id}/repos":    {kind: isoExempt, reason: "the acting tenant's own installations (c.tenant()): another tenant's id answers 404 (github_installs_test.go)"},
	"DELETE /api/admin/github/installations/{id}":       {kind: isoExempt, reason: "the acting tenant's own installations (c.tenant()): another tenant's id answers 404 (github_installs_test.go)"},
	"DELETE /api/account/links/{id}":                    {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"GET /api/account/usage":                            {kind: isoExempt, reason: "the caller's own usage"},
	"GET /api/account/tokens":                           {kind: isoList},
	"POST /api/account/tokens":                          {kind: isoExempt, reason: "a token for the caller's own spaces (a space of another tenant is not one of them: tested in the tenant tests)"},
	"DELETE /api/account/tokens/{id}":                   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.tokenB} }},
	"GET /api/account/grants":                           {kind: isoList},
	"DELETE /api/account/grants/{id}":                   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.grantB} }},
	"GET /api/oauth/consent":                            {kind: isoExempt, reason: "the caller's own consent screen"},
	"POST /api/oauth/consent":                           {kind: isoExempt, reason: "the caller's own consent"},
	"GET /api/account/passkeys":                         {kind: isoExempt, reason: "the caller's own passkeys"},
	"POST /api/account/passkeys/begin":                  {kind: isoExempt, reason: "the caller's own passkeys"},
	"POST /api/account/passkeys/finish":                 {kind: isoExempt, reason: "the caller's own passkeys"},
	"PATCH /api/account/passkeys/{id}":                  {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/account/passkeys/{id}":                 {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},

	// 2.7: a GitHub connection is the caller's own; the App is the server's; an installation
	// is shared by a site administrator with chosen tenants, and only their people use it.
	"GET /api/account/links/{provider}/callback":        {kind: isoExempt, reason: "needs the session of the user who started it; account_in_use names nobody, in no tenant"},
	"DELETE /api/account/links/{id}/repos":              {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"GET /api/account/links/{provider}/owners":          {kind: isoList},
	"GET /api/providers/github/manifest/callback":       {kind: isoExempt, reason: "needs the session of the site administrator who started it; the App is the server's, not a tenant's"},
	"POST /api/github/webhook":                          {kind: isoExempt, reason: "GitHub's signed events, no session; it answers nothing of any tenant, and completes only claims a person made in their own scope"},
	"DELETE /api/account/leftovers/{provider}/{repoId}": {kind: isoExempt, reason: "the caller's own reminder, by a GitHub repository id; names no PetalBook id and touches only the caller's rows"},
	"GET /api/account/links/{provider}/repos":           {kind: isoExempt, reason: "what the caller's own connection, or an installation shared with the caller's tenant, reaches on GitHub; names no PetalBook id (tested in github_tenants_test.go)"},

	// 3.10 part 4: the notification centre and the preferences are the caller's own. Rows are read by
	// the caller's id and tenant, never by an id of the request (TestNotificationsAreTheCallersOwnAcrossTenants).
	"GET /api/notifications":           {kind: isoList},
	"GET /api/notifications/unread":    {kind: isoList},
	"POST /api/notifications/read":     {kind: isoExempt, reason: "marks the caller's own rows: an id of another person's, in any tenant, matches nothing (tested in notifications_test.go)"},
	"POST /api/notifications/read-all": {kind: isoExempt, reason: "marks the caller's own rows"},
	"GET /api/prefs":                   {kind: isoExempt, reason: "the caller's own personal space; names no space or person"},
	"PATCH /api/prefs":                 {kind: isoExempt, reason: "the caller's own personal space; names no space or person"},

	"GET /api/groups":   {kind: isoList},
	"GET /api/people":   {kind: isoList},
	"GET /api/settings": {kind: isoList},
	"GET /api/spaces":   {kind: isoList},
	"POST /api/spaces":  {kind: isoExempt, reason: "makes a space of the caller's own tenant"},

	"GET /api/users":                       {kind: isoList},
	"POST /api/admin/users/{id}/view-as":   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }, body: func(f *tenFx, id string) any { return map[string]any{"reason": "looking"} }},
	"DELETE /api/admin/users/{id}/view-as": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"POST /api/users/{id}/password":        {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }, body: func(f *tenFx, id string) any { return map[string]any{"password": "a-new-password"} }},
	"POST /api/users/{id}/password-reset":  {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }, body: func(f *tenFx, id string) any { return map[string]any{} }},
	"PATCH /api/users/{id}":                {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }, body: func(f *tenFx, id string) any { return map[string]any{"disabled": true} }},
	"POST /api/invites":                    {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"role": "member", "tenant": id} }, ids: func(f *tenFx) map[string]string { return map[string]string{} }},
	"POST /api/admin/members/batch": {kind: isoParam, body: func(f *tenFx, id string) any {
		return map[string]any{"tenant": id, "rows": []map[string]any{{"user": "new@example.test", "role": "member"}}}
	}, ids: func(f *tenFx) map[string]string { return map[string]string{} }},
	"GET /api/invites":                          {kind: isoList},
	"DELETE /api/invites/{id}":                  {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.inviteB} }},
	"GET /api/users/{id}":                       {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"GET /api/users/{id}/memberships":           {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"GET /api/users/{id}/sessions":              {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/users/{id}/sessions":           {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/users/{id}/sessions/{session}": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB, "session": fakeID} }},
	"GET /api/users/{id}/links":                 {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/users/{id}/links/{link}":       {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB, "link": fakeID} }},
	"GET /api/users/{id}/passkeys":              {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/users/{id}/passkeys/{passkey}": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB, "passkey": fakeID} }},

	"GET /api/admin/spaces":         {kind: isoList},
	"GET /api/admin/spaces/{space}": {kind: isoParam},
	// 3.5 part 4: the storage summary and the lists of people. They read the
	// usage rows through the caller's scope; B's people are B's ids (the item) or
	// are not in the list (the others), and ?tenant=B is answered as a tenant
	// that does not exist.
	"GET /api/admin/storage":               {kind: isoList},
	"GET /api/admin/storage/people":        {kind: isoList},
	"GET /api/admin/storage/people/{user}": {kind: isoParam},
	// 3.5: the recycle bin, exports, views and confirmations. Every id is B's item,
	// export or space; a tenant administrator of A is answered as for one that is
	// not there. DELETE /api/admin/recycle/{item} is the server's (isoSite).
	"GET /api/admin/spaces/{space}/deletion": {kind: isoParam},
	"POST /api/admin/spaces/{space}/view":    {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"reason": "looking"} }},
	"DELETE /api/admin/spaces/{space}/view":  {kind: isoParam},
	"GET /api/admin/recycle":                 {kind: isoList},
	"POST /api/admin/recycle/{item}/restore": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"item": f.itemB} }, body: func(f *tenFx, id string) any { return map[string]any{} }},
	"POST /api/admin/confirm": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{} }, body: func(f *tenFx, id string) any {
		return map[string]any{"action": "delete_spaces", "spaces": []string{ofB(f, id, f.spaceB)}}
	}},
	"POST /api/admin/exports": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{} }, body: func(f *tenFx, id string) any {
		return map[string]any{"spaces": []string{ofB(f, id, f.spaceB)}}
	}},
	"GET /api/admin/users/{user}/deletion":    {kind: isoParam},
	"DELETE /api/users/{id}":                  {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }, body: func(f *tenFx, id string) any { return map[string]any{} }},
	"POST /api/admin/bulk":                    {kind: isoExempt, reason: "names nothing: the token holds the ids, bound to the caller who asked for it, and a confirmation cannot be asked for an id outside the caller's tenant (404; see the confirm entry and TestABulkJobRunsOneItemAtATimeAndListsWhatFailed)"},
	"GET /api/admin/jobs/{id}":                {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.jobB} }},
	"GET /api/admin/exports":                  {kind: isoList},
	"GET /api/admin/jobs":                     {kind: isoList},
	"DELETE /api/admin/exports/{id}":          {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.exportB} }},
	"GET /api/admin/exports/{id}/download":    {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.exportB} }},
	"POST /api/admin/spaces/{space}/size":     {kind: isoParam},
	"POST /api/admin/spaces/{space}/transfer": {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"userId": f.userB} }},
	"POST /api/admin/spaces/{space}/takeover": {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"reason": "taking over"} }},
	"POST /api/admin/spaces/{space}/disable":  {kind: isoParam},
	"POST /api/admin/spaces/{space}/enable":   {kind: isoParam},
	"DELETE /api/admin/spaces/{space}":        {kind: isoParam},
	"GET /api/admin/groups":                   {kind: isoList},
	"POST /api/admin/groups":                  {kind: isoList},
	"GET /api/admin/groups/{group}":           {kind: isoParam},
	"PATCH /api/admin/groups/{group}":         {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"name": "renamed"} }},
	"DELETE /api/admin/groups/{group}":        {kind: isoParam, query: "?force=1", ids: func(f *tenFx) map[string]string { return map[string]string{"group": f.syncedB} }},
	"GET /api/admin/groups/{group}/members":   {kind: isoParam},
	"POST /api/admin/groups/{group}/members":  {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"userIds": []string{f.userB}} }},
	"POST /api/admin/groups/{group}/members/batch": {kind: isoParam, body: func(f *tenFx, id string) any {
		return map[string]any{"rows": []map[string]any{{"user": "mem-b"}}}
	}},
	"DELETE /api/admin/groups/{group}/members/{user}":   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"group": f.groupB, "user": f.userB} }},
	"GET /api/admin/groups/{group}/spaces":              {kind: isoParam},
	"GET /api/admin/tokens":                             {kind: isoList},
	"DELETE /api/admin/tokens/{id}":                     {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.tokenB} }},
	"GET /api/admin/grants":                             {kind: isoList},
	"DELETE /api/admin/grants/{id}":                     {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.grantB} }},
	"GET /api/admin/audit":                              {kind: isoList},
	"GET /api/admin/audit/kinds":                        {kind: isoList},
	"GET /api/admin/audit/download":                     {kind: isoList},
	"GET /api/admin/usage":                              {kind: isoList},
	"GET /api/admin/tenants/{tenant}":                   {kind: isoParam},
	"PATCH /api/admin/tenants/{tenant}":                 {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"name": "taken over"} }},
	"GET /api/admin/tenants/{tenant}/settings":          {kind: isoParam},
	"GET /api/admin/tenants/{tenant}/overview":          {kind: isoParam},
	"GET /api/admin/tenants/{tenant}/secrets":           {kind: isoParam},
	"PUT /api/admin/tenants/{tenant}/secrets/{name}":    {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"tenant": f.tenB, "name": "mail.password"} }, body: func(f *tenFx, id string) any { return map[string]any{"value": "evil"} }},
	"DELETE /api/admin/tenants/{tenant}/secrets/{name}": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"tenant": f.tenB, "name": "mail.password"} }},
	"PUT /api/admin/tenants/{tenant}/settings/{key}":    {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"tenant": f.tenB, "key": "legal.termsUrl"} }, body: func(f *tenFx, id string) any { return map[string]any{"value": "https://evil.example"} }},

	// Offline sync (4.1). heads names spaces in its query and answers each back,
	// in heads or in missing: B's space must be in missing as a made-up one is.
	// The other three are space routes; their queries make the request one that
	// would be answered (a cursor of B's space, page ids of the right form), so
	// it is the {space} that refuses it.
	"GET /api/sync/heads":                  {kind: isoEcho, queryOf: func(f *tenFx, id string) string { return "?space=" + id + "&space=" + f.spaceA }},
	"GET /api/spaces/{space}/sync/catalog": {kind: isoParam, query: "?limit=10"},
	"GET /api/spaces/{space}/changes":      {kind: isoParam, queryOf: func(f *tenFx, _ string) string { return "?since=" + url.QueryEscape(f.cursorB) }},
	"GET /api/spaces/{space}/sync/pages":   {kind: isoParam, query: "?ids=0000000AAA,0000000AAB"},
}

func init() {
	// The server's own: every route marked site-admin-only is one entry.
	for _, r := range api.Routes() {
		if r.SiteAdmin {
			isolationRoutes[r.Method+" "+r.Pattern] = isoEntry{kind: isoSite}
		}
	}
}

func TestEveryRouteIsInTheIsolationTable(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, r := range api.Routes() {
		key := r.Method + " " + r.Pattern
		seen[key] = true
		if r.Space {
			continue // by its {space}
		}
		if _, ok := isolationRoutes[key]; !ok {
			t.Errorf("%s is not in isolationRoutes: say how it keeps tenants apart (or why it needs no check)", key)
		}
	}
	for key, e := range isolationRoutes {
		if !seen[key] {
			t.Errorf("isolationRoutes has %s, which is no route any more", key)
		}
		if e.kind == isoExempt && e.reason == "" {
			t.Errorf("%s is exempt without a reason", key)
		}
	}
}

// build makes the request of a route: other picks B's ids, else made-up ones.
func (f *tenFx) build(rt api.RouteInfo, e isoEntry, other bool) (path string, body any) {
	val := func(name, bID string) string {
		if other {
			return bID
		}
		return fakeID
	}
	ids := map[string]string{}
	if e.ids != nil {
		ids = e.ids(f)
	}
	params := map[string]string{"space": f.spaceB, "group": f.groupB, "user": f.userB, "tenant": f.tenB, "id": f.userB, "key": "legal.termsUrl",
		"tag": "x", "name": "x", "commit": "abc", "path...": "x.md", "passkey": fakeID, "item": f.itemB}
	path = rt.Pattern
	for name, bID := range params {
		if v, ok := ids[name]; ok {
			bID = v
		}
		keep := name == "key" || name == "tag" || name == "name" || name == "commit" || name == "path..." || name == "passkey"
		v := bID
		if !keep {
			v = val(name, bID)
		}
		if name == "path..." {
			path = strings.ReplaceAll(path, "{path...}", v)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", v)
	}
	// A page id of the path is not another tenant's secret: the same either way.
	if strings.Contains(rt.Pattern, "/pages/{id}") {
		path = strings.ReplaceAll(rt.Pattern, "{id}", "0000000AAA")
		path = strings.ReplaceAll(path, "{space}", val("space", f.spaceB))
		for _, p := range []string{"{tag}", "{name}", "{commit}", "{group}", "{user}"} {
			path = strings.ReplaceAll(path, p, "x")
		}
		path = strings.ReplaceAll(path, "{path...}", "x.md")
	}
	path += e.query
	if e.queryOf != nil {
		path += e.queryOf(f, val("space", f.spaceB))
	}
	body = map[string]any{}
	if e.body != nil {
		body = e.body(f, val("id", f.tenB))
	}
	if rt.Space && strings.Contains(rt.Pattern, "/groups") && rt.Method == "POST" {
		body = map[string]any{"groupId": val("group", f.groupB), "role": "viewer"}
	}
	if rt.Space && strings.HasSuffix(rt.Pattern, "/members") && rt.Method == "POST" {
		body = map[string]any{"username": "mem-b", "role": "viewer"}
	}
	if rt.Method == "GET" || rt.Method == "DELETE" && body != nil && len(body.(map[string]any)) == 0 {
		body = nil
	}
	return path, body
}

// ofB is B's id for a request meant for B's things, and a made-up id for one
// meant to be answered as for something that does not exist (the body of such a
// request is given fakeID where it would name a tenant).
func ofB(f *tenFx, id, b string) string {
	if id == fakeID {
		return fakeID
	}
	return b
}

// shape is what an answer says that must not depend on whose id was asked.
func shape(r result) string {
	code, _ := r.body["error"].(string)
	return fmt.Sprintf("%d %s", r.code, code)
}

func TestTenantsAreIsolatedOnEveryRoute(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "acme-sso")
	eachAccounts(t, accOpts{oauth: true, httpc: anyTLSClient()}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.addMappedB(t, idp)
		callers := []struct {
			name string
			c    *client
		}{{"tenant administrator", f.admA}, {"member", f.memA}, {"token", f.tokA}}
		b := f.bIDs()
		checked := 0
		routes := api.Routes()
		sort.Slice(routes, func(i, j int) bool { return routes[i].Pattern+routes[i].Method < routes[j].Pattern+routes[j].Method })
		for _, rt := range routes {
			key := rt.Method + " " + rt.Pattern
			e, ok := isolationRoutes[key]
			if rt.Space {
				// By its {space}; an entry of the table only adds a query.
				e = isoEntry{kind: isoParam, query: e.query, queryOf: e.queryOf}
				ok = true
			}
			if !ok {
				continue
			}
			switch e.kind {
			case isoExempt:
				continue
			case isoEcho:
				for _, cl := range callers {
					rb := cl.c.do(rt.Method, rt.Pattern+e.queryOf(f, f.spaceB), nil)
					rf := cl.c.do(rt.Method, rt.Pattern+e.queryOf(f, fakeID), nil)
					if shape(rb) != shape(rf) || strings.ReplaceAll(rb.raw, f.spaceB, fakeID) != rf.raw {
						t.Errorf("%s as %s: B's id answered %s, a made-up id %s", key, cl.name, rb.raw, rf.raw)
					}
					for _, id := range b {
						if id != f.spaceB && strings.Contains(rb.raw, id) {
							t.Errorf("%s as %s: the answer names %q of tenant B: %s", key, cl.name, id, rb.raw)
						}
					}
					checked++
				}
			case isoSite:
				for _, cl := range callers[:2] {
					path := strings.NewReplacer("{tenant}", f.tenB, "{key}", "legal.termsUrl", "{id}", "x", "{slug}", "x").Replace(rt.Pattern)
					cl.c.do(rt.Method, path, "{}").want(t, 403, "forbidden")
				}
				continue
			case isoList:
				for _, cl := range callers {
					path := rt.Pattern
					if strings.Contains(path, "{") {
						continue
					}
					body := any(nil)
					if rt.Method != "GET" {
						body = map[string]any{"name": "x-" + cl.name, "source": "local"}
					}
					r := cl.c.do(rt.Method, path, body)
					for _, id := range b {
						if strings.Contains(r.raw, id) {
							t.Errorf("%s as %s: the answer names %q of tenant B: %s", key, cl.name, id, r.raw)
						}
					}
					if rt.Method == "GET" && rt.Admin && cl.name == "tenant administrator" {
						// ?tenant=B is as ?tenant=made-up.
						rb := cl.c.do("GET", path+"?tenant="+f.tenB, nil)
						rf := cl.c.do("GET", path+"?tenant="+fakeID, nil)
						if shape(rb) != shape(rf) || shape(rb) != "404 not_found" {
							t.Errorf("%s ?tenant=: B %s, made-up %s", key, shape(rb), shape(rf))
						}
					}
					checked++
				}
				if rt.Method == "POST" && rt.Pattern == "/api/admin/groups" {
					rb := f.admA.do("POST", "/api/admin/groups?tenant="+f.tenB, map[string]any{"name": "evil", "source": "local"})
					rf := f.admA.do("POST", "/api/admin/groups?tenant="+fakeID, map[string]any{"name": "evil", "source": "local"})
					if shape(rb) != "404 not_found" || shape(rb) != shape(rf) {
						t.Errorf("creating a group in another tenant: %s / %s", shape(rb), shape(rf))
					}
				}
			case isoParam:
				for _, cl := range callers {
					pb, bb := f.build(rt, e, true)
					pf, bf := f.build(rt, e, false)
					rb := cl.c.do(rt.Method, pb, bb)
					rf := cl.c.do(rt.Method, pf, bf)
					if shape(rb) != shape(rf) {
						t.Errorf("%s as %s: B's id answered %s, a made-up id %s\n  %s\n  %s", key, cl.name, shape(rb), shape(rf), rb.raw, rf.raw)
					}
					if rb.code < 400 {
						t.Errorf("%s as %s: B's id was accepted: %d %s", key, cl.name, rb.code, rb.raw)
					}
					for _, id := range b {
						if strings.Contains(rb.raw, id) {
							t.Errorf("%s as %s: the refusal names %q of tenant B: %s", key, cl.name, id, rb.raw)
						}
					}
					checked++
				}
			}
		}
		if checked < 150 {
			t.Fatalf("only %d calls were made: the table is not being read", checked)
		}
		// Nothing of B changed, and no link joins two tenants.
		a.auth.FlushAudit(context.Background())
		for q, want := range map[string]int{
			`SELECT COUNT(*) FROM spaces WHERE id = '` + f.spaceB + `' AND disabled = 0`:      1,
			`SELECT COUNT(*) FROM groups WHERE id = '` + f.groupB + `' AND name = 'Group B'`:  1,
			`SELECT COUNT(*) FROM groups WHERE id = '` + f.syncedB + `'`:                      1,
			`SELECT COUNT(*) FROM users WHERE id = '` + f.userB + `' AND disabled = 0`:        1,
			`SELECT COUNT(*) FROM tenants WHERE id = '` + f.tenB + `' AND name = 'Acme'`:      1,
			`SELECT COUNT(*) FROM invites WHERE id = '` + f.inviteB + `'`:                     1,
			`SELECT COUNT(*) FROM recycle_items WHERE id = '` + f.itemB + `' AND job_id = ''`: 1,
			`SELECT COUNT(*) FROM jobs WHERE id = '` + f.exportB + `'`:                        1,
		} {
			var n int
			if err := a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil || n != want {
				t.Errorf("%s: %d %v", q, n, err)
			}
		}
		if n := f.mappingsOfB(t); n != 1 {
			t.Errorf("acme-sso maps %d values: B's mapping was removed by another tenant", n)
		}
		assertNoCrossTenantMemberships(t, a)
		if n := f.countOf(`SELECT COUNT(*) FROM api_tokens WHERE id = '` + f.tokenB + `' AND revoked = 0`); n != 1 {
			t.Errorf("B's token was revoked")
		}
		if n := f.countOf(`SELECT COUNT(*) FROM oauth_grants WHERE id = '` + f.grantB + `'`); n != 1 {
			t.Errorf("B's grant was revoked")
		}
	})
}

func (f *tenFx) countOf(q string) int {
	var n int
	if err := f.a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil {
		f.a.t.Fatal(q, err)
	}
	return n
}

// assertNoCrossTenantMemberships is the query every test of the fixture ends
// with: no membership, group membership or place of a group in a space joins
// two tenants.
func assertNoCrossTenantMemberships(t *testing.T, a *acc) {
	t.Helper()
	for name, q := range map[string]string{
		// A site administrator who took a space over explicitly is the one exception (3.6).
		"space_members": `SELECT COUNT(*) FROM space_members m JOIN spaces s ON s.id = m.space_id JOIN users u ON u.id = m.user_id WHERE s.tenant_id <> u.tenant_id AND NOT (u.role = 'admin' AND u.site_admin = 1)`,
		"group_members": `SELECT COUNT(*) FROM group_members m JOIN groups g ON g.id = m.group_id JOIN users u ON u.id = m.user_id WHERE g.tenant_id <> u.tenant_id`,
		"space_groups":  `SELECT COUNT(*) FROM space_groups x JOIN spaces s ON s.id = x.space_id JOIN groups g ON g.id = x.group_id WHERE s.tenant_id <> g.tenant_id`,
		"token spaces":  `SELECT COUNT(*) FROM api_token_spaces x JOIN api_tokens k ON k.id = x.token_id JOIN users u ON u.id = k.user_id JOIN spaces s ON s.id = x.space_id WHERE s.tenant_id <> u.tenant_id`,
	} {
		var n int
		if err := a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil {
			t.Fatal(name, err)
		}
		if n != 0 {
			t.Errorf("%d rows of %s join two tenants", n, name)
		}
	}
}

var _ = time.Second

// A personal space is never reachable by a tenant administrator: every route
// that takes a space or a recycle item (in its path or its body) answers for a
// personal one exactly as for an id that does not exist, and no list shows one
// (docs/api.md, "Viewing a space", As built). The routes are read from the table,
// so a new one is covered as it is added.
func TestPersonalSpacesDoNotExistForATenantAdministratorOnEveryRoute(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		own := f.memA.personalSpace()
		// A personal space in the recycle bin: the one of a person of A who was deleted.
		pat := a.addUser("pat", pw, "member")
		patSpace := pat.personalSpace()
		patID := mustUserID(t, a, "pat")
		userItem := f.admA.do("DELETE", "/api/users/"+patID, map[string]any{}).want(t, 200, "").body["id"].(string)
		var personalItem string
		if err := a.st.Q().QueryRow(context.Background(), `SELECT id FROM recycle_items WHERE space_id = ? AND personal = 1`, patSpace).Scan(&personalItem); err != nil {
			t.Fatal(err)
		}
		// g is the fixture with "B's" space and item being A's personal ones.
		g := *f
		g.spaceB, g.itemB, g.userB = own, personalItem, f.userA
		forbidden := []string{own, personalItem, patSpace}

		checked := 0
		check := func(name, method, pb string, bb any, pf string, bf any) {
			t.Helper()
			rb := f.admA.do(method, pb, bb)
			rf := f.admA.do(method, pf, bf)
			if shape(rb) != shape(rf) {
				t.Errorf("%s: a personal space answered %s, a made-up id %s\n  %s\n  %s", name, shape(rb), shape(rf), rb.raw, rf.raw)
			}
			if rb.code < 400 {
				t.Errorf("%s: a personal space was accepted: %d %s", name, rb.code, rb.raw)
			}
			for _, id := range forbidden {
				if strings.Contains(rb.raw, id) {
					t.Errorf("%s: the answer names %s: %s", name, id, rb.raw)
				}
			}
			checked++
		}
		routes := api.Routes()
		sort.Slice(routes, func(i, j int) bool { return routes[i].Pattern+routes[i].Method < routes[j].Pattern+routes[j].Method })
		for _, rt := range routes {
			key := rt.Method + " " + rt.Pattern
			e := isolationRoutes[key]
			switch {
			case rt.Space:
				e = isoEntry{kind: isoParam, query: e.query, queryOf: e.queryOf}
			case rt.Admin && (strings.Contains(rt.Pattern, "{space}") || strings.Contains(rt.Pattern, "{item}")):
			case key == "POST /api/admin/confirm" || key == "POST /api/admin/exports":
			default:
				continue
			}
			pb, bb := g.build(rt, e, true)
			pf, bf := g.build(rt, e, false)
			check(key, rt.Method, pb, bb, pf, bf)
		}
		// Every action of a confirmation, and an export, by the ids of the body.
		for action, kind := range map[string]string{"delete_spaces": "spaces", "disable_spaces": "spaces", "enable_spaces": "spaces", "evict": "spaces",
			"pin": "spaces", "unpin": "spaces", "export": "spaces", "restore": "items"} {
			id := own
			if kind == "items" {
				id = personalItem
			}
			check("confirm "+action, "POST", "/api/admin/confirm", map[string]any{"action": action, kind: []string{id}},
				"/api/admin/confirm", map[string]any{"action": action, kind: []string{fakeID}})
		}
		check("confirm export of an item", "POST", "/api/admin/confirm", map[string]any{"action": "export", "items": []string{personalItem}},
			"/api/admin/confirm", map[string]any{"action": "export", "items": []string{fakeID}})
		check("export of an item", "POST", "/api/admin/exports", map[string]any{"items": []string{personalItem}},
			"/api/admin/exports", map[string]any{"items": []string{fakeID}})
		if checked < 80 {
			t.Fatalf("only %d calls were made: the table is not being read", checked)
		}
		// No list shows one.
		// 3.5 part 4: every sort and filter of the lists of spaces, and the item of
		// the person the personal space belongs to, which lists the spaces counted for them.
		lists := []string{"/api/admin/spaces", "/api/admin/spaces?q=My", "/api/admin/recycle", "/api/admin/recycle?kind=space",
			"/api/admin/spaces?sort=name", "/api/admin/spaces?sort=bytes", "/api/admin/spaces?sort=lastUsed&order=asc", "/api/admin/spaces?owner=" + f.userA,
			"/api/admin/spaces?remote=none", "/api/admin/spaces?cache=local", "/api/admin/spaces?idleDays=0", "/api/admin/spaces?deadline=overdue",
			"/api/admin/storage/people/" + f.userA}
		for _, path := range lists {
			r := f.admA.do("GET", path, nil).want(t, 200, "")
			for _, it := range r.body["items"].([]any) {
				m := it.(map[string]any)
				if m["personal"] == true {
					t.Errorf("%s lists a personal space: %v", path, m)
				}
				if sp, ok := m["space"].(map[string]any); ok && sp["personal"] == true {
					t.Errorf("%s lists a personal space: %v", path, m)
				}
			}
			for _, id := range []string{own, personalItem} {
				if strings.Contains(r.raw, `"id":"`+id+`"`) {
					t.Errorf("%s lists %s: %s", path, id, r.raw)
				}
			}
		}
		// The list of people has no space in it at all; nothing there is a personal space.
		pl := f.admA.do("GET", "/api/admin/storage/people", nil).want(t, 200, "")
		if strings.Contains(pl.raw, own) || strings.Contains(pl.raw, patSpace) {
			t.Errorf("the people's list names a personal space: %s", pl.raw)
		}
		// A person who has only a personal space shows no spaces of theirs to a tenant administrator, and a site administrator sees it.
		if r := f.admA.do("GET", "/api/admin/storage/people/"+f.userA, nil).want(t, 200, ""); len(r.body["items"].([]any)) != 0 {
			t.Errorf("a tenant administrator sees a person's spaces: %s", r.raw)
		}
		if r := f.root.do("GET", "/api/admin/storage/people/"+f.userA, nil).want(t, 200, ""); !strings.Contains(r.raw, `"id":"`+own+`"`) {
			t.Errorf("a site administrator does not see the person's personal space: %s", r.raw)
		}
		if r := f.root.do("GET", "/api/admin/spaces?owner="+f.userA, nil).want(t, 200, ""); !strings.Contains(r.raw, `"id":"`+own+`"`) {
			t.Errorf("a site administrator does not see the personal space in the list: %s", r.raw)
		}
		// The person's own item is the tenant's: restoring it brings their personal space back with them.
		f.admA.do("POST", "/api/admin/recycle/"+userItem+"/restore", map[string]any{}).want(t, 200, "")
		pat.do("POST", "/api/login", map[string]any{"username": "pat", "password": pw}).want(t, 200, "")
		if pat.personalSpace() != patSpace {
			t.Fatal("the personal space did not come back with its person")
		}
		// A site administrator reaches every one of them.
		f.root.do("GET", "/api/admin/spaces/"+own, nil).want(t, 200, "")
		f.root.do("GET", "/api/admin/spaces/"+own+"/deletion", nil).want(t, 200, "")
	})
}
