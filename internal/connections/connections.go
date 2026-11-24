// Package connections is the one interface the server has to the platforms
// that hold its spaces' repositories (docs/api.md, "Connections"). A provider
// describes itself, connects a person (start, callback, renewal, disconnect),
// lists the places it may create repositories in and the repositories it
// reaches, creates a repository, gives Git a credential, and checks its own
// configuration. GitHub (internal/github) is the first implementation; plain
// Git by address is the provider "git" here, which has no connection.
//
// The package holds the shapes that cross between the providers and what uses
// them (the API and the spaces): the interface, the registry, the data a
// provider hands on, and the errors, with the generic codes.
package connections

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Codes of the errors of a connection (docs/api.md, "Connections", "Error
// codes"). They do not carry the platform's name; the error's Provider does.
const (
	CodeNotConfigured   = "provider_not_configured"
	CodeNotConnected    = "not_connected"
	CodeOwnerNotAllowed = "owner_not_allowed"
	CodeNameTaken       = "name_taken"
	// CodeRefused: the platform refused a person (the message says what it said).
	CodeRefused = "provider_refused"
	// CodeMissingPermission: the App, or its installation, lacks a permission
	// (the answer names them); a refusal of the platform to a person is CodeRefused.
	CodeMissingPermission = "app_missing_permission"
	// CodeAppNotInstalled: the App is not installed where the person wants a
	// repository (their own account, or an organisation the App was removed from).
	CodeAppNotInstalled  = "app_not_installed"
	CodePublicURLNeeded  = "public_url_needed"
	CodeRepoNotInInstall = "repo_not_in_installation"
	CodePushFailed       = "push_failed"
	CodeKeyInvalid       = "key_invalid"
	CodeAppRejected      = "app_rejected"
	CodeClientRejected   = "client_rejected"
	CodeInUse            = "provider_in_use"
	CodeProviderError    = "provider_error"
	CodeInvalid          = "invalid"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeRateLimited      = "rate_limited"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"

	// The codes a space's remote shows (errorCode) when its credential stops working.
	CodeConnectionExpired = "connection_expired"
	CodeConnectionMissing = "connection_missing"
	CodeInstallationGone  = "installation_gone"
)

// Via says whose token acts for a space or creates a repository.
const (
	ViaApp  = "app"
	ViaUser = "user"
)

// The provider that is a plain Git address.
const GitID = "git"

// RepoRef names a repository in an error answer.
type RepoRef struct {
	ID       int64
	FullName string
	HTMLURL  string
}

// Error is a refusal with the code, status and message the API answers with.
type Error struct {
	// Provider is the id of the provider the error is about.
	Provider string
	Code     string
	Status   int
	Msg      string
	Repo     *RepoRef
	// AssetsRepo is the attachment repository that was made with the space's
	// (milestone 3.1) when a later step failed.
	AssetsRepo *RepoRef
	// Missing names the permissions the App or its installation lacks.
	Missing []string
	// InstallURL is where the App is installed (CodeAppNotInstalled).
	InstallURL string
	// Counts says how much uses a provider that cannot be removed
	// (CodeInUse): "connections" and "spaces".
	Counts map[string]int
	// Reason says what is wrong with a configuration (CodeInvalid of an
	// administrator's request): a short name a client can show its own words for.
	Reason     string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// Errorf makes an Error of a provider.
func Errorf(provider string, status int, code, format string, a ...any) *Error {
	return &Error{Provider: provider, Code: code, Status: status, Msg: fmt.Sprintf(format, a...)}
}

// Capabilities are what a client may offer for a provider; it shows options by
// capability, never by the platform's name.
type Capabilities struct {
	CreateRepo     bool
	Installations  bool
	ConnectAccount bool
	StorageTarget  bool
	// PartialSync is "yes", "no" or "unknown" (not yet detected).
	PartialSync string
}

// Descriptor is how a provider describes itself.
type Descriptor struct {
	ID, Type, Host, Name string
	// Owner is "site", or "tenant" for a provider a tenant owns, whose id is
	// TenantID (a provider of the site has none). Only people of that tenant
	// see or use a tenant's provider.
	Owner        string
	TenantID     string
	Capabilities Capabilities
}

// App is the App a provider has, when it has one and it is set up.
type App struct {
	Name, Slug, HTMLURL, InstallURL string
}

// Connection is what a person sees of their connection.
type Connection struct {
	Login          string
	AccountID      string
	State          string // "ok" or "expired"
	Connected      time.Time
	RefreshExpires time.Time
	Spaces         int
	Admin          bool
}

// Leftover is a reminder of an empty repository a failed attempt to make a
// space left on the platform.
type Leftover struct {
	RepoID    int64
	FullName  string
	HTMLURL   string
	SpaceName string
	Created   time.Time
}

// Info is what any signed-in user may know about a provider on this server.
type Info struct {
	Configured        bool
	App               *App
	UserAuthorization bool
	Connection        *Connection
	Leftovers         []Leftover // the caller's reminders, newest first
}

// StartRequest begins connecting a person.
type StartRequest struct {
	Return string
	// Install sends the person to the App's installation page, which authorises
	// them as well; false goes to the authorisation page alone.
	Install bool
}

// Start is where to send the browser, and the value of the cookie that ties
// the attempt to that browser.
type Start struct {
	URL, Binding string
}

// Owner is an account a repository can be created in.
type Owner struct {
	Login          string
	Type           string // "User" or "Organization"
	Via            string // "user": the caller's own token creates the repository; "app": the installation's
	InstallationID int64  // 0 when not known (the caller's own account)
	// NeedsInstall: the caller's own account, where the App is not installed;
	// the platform lets no repository be made there until it is.
	NeedsInstall bool
}

// RepoItem is a repository a space can be connected to.
type RepoItem struct {
	ID            int64
	FullName      string
	Private       bool
	DefaultBranch string
	HTMLURL       string
	CanPush       bool
}

// RepoQuery says which repositories are listed: those of an installation, or
// the caller's own (Mine).
type RepoQuery struct {
	Installation int64
	Mine         bool
	Cursor       string
	// Q narrows the list to repositories whose name contains it; a provider
	// that cannot search ignores it.
	Q string
}

// Repo is a repository as a provider hands it on.
type Repo struct {
	ID       int64
	Owner    string
	Name     string
	FullName string
	HTMLURL  string
	Private  bool
}

// Ref is the repository as an error answer names it.
func (r *Repo) Ref() *RepoRef { return &RepoRef{ID: r.ID, FullName: r.FullName, HTMLURL: r.HTMLURL} }

// NewRepoSpec is what a repository is created with.
type NewRepoSpec struct {
	Name        string
	Private     bool
	Description string
}

// Plan says where a repository will be created and with whose token.
type Plan struct {
	Owner          string // as the platform spells it
	Org            string // "" for the caller's own account
	Via            string // the token that creates it: "user" or "app"
	UserID         string
	InstallationID int64 // where the App is installed on the account, if known
	// Ref is what the provider needs to find the place again when it creates the
	// repository (GitLab's id of a group's namespace).
	Ref string
}

// Cred is how a space syncs with its repository.
type Cred struct {
	Provider       string
	Via            string
	InstallationID int64 // via "app"
	UserID         string
	RepoID         int64
}

// Provider is the platform's side of a connection. Every method but
// Descriptor, Host-like reads and the parsers may reach the platform.
type Provider interface {
	Descriptor() Descriptor
	// Info is the provider's state and the caller's connection and reminders.
	Info(ctx context.Context, userID string) (*Info, error)

	// StartConnect begins connecting a person; CompleteConnect finishes it from
	// the platform's redirect and never fails: every outcome is a path to
	// redirect to (userID is the user of the browser's session, "" without
	// one; clearBinding says the attempt is over).
	StartConnect(ctx context.Context, userID string, req StartRequest) (*Start, error)
	CompleteConnect(ctx context.Context, binding, userID string, q url.Values) (redirect string, clearBinding bool)
	Disconnect(ctx context.Context, userID string) error
	DismissLeftover(ctx context.Context, userID string, repoID int64) error

	// Owners lists the accounts the caller can create a repository in, and
	// Repos the repositories they can connect a space to, a page at a time.
	Owners(ctx context.Context, userID, cursor string) ([]Owner, string, error)
	Repos(ctx context.Context, userID string, q RepoQuery) ([]RepoItem, string, error)

	// For spaces: reading an address of this platform, the address of a
	// repository, and what a repository may be called.
	ParseRepoURL(raw string) (owner, name string, ok bool)
	RepoURL(owner, name string) string
	ValidRepoName(name string) bool
	// PlanCreate decides, without creating anything, whose token creates a
	// repository for owner; CreateRepo creates it; ChooseCredential picks and
	// checks how a space syncs with it; ResolveExistingIn does so for a
	// repository that exists (via "" lets the provider choose), for a space of
	// the given tenant.
	PlanCreate(ctx context.Context, userID, owner string) (*Plan, error)
	CreateRepo(ctx context.Context, p *Plan, n NewRepoSpec) (*Repo, error)
	ChooseCredential(ctx context.Context, p *Plan, repo *Repo) (*Cred, error)
	ResolveExistingIn(ctx context.Context, tenantID, userID, owner, name, via string) (*Cred, *Repo, error)
	RecordLeftover(ctx context.Context, userID string, repo *Repo, spaceName string) error
	// GitToken is the token Git is given for a space's remote. Every error is a
	// *gitstore.CredentialError. staging is a space still being made: it has no
	// member yet, so that is not asked.
	GitToken(ctx context.Context, r *store.Remote, staging bool) (string, error)

	// Check reads the provider's configuration from the platform again.
	Check(ctx context.Context) error
	// OnChange registers what is told when something that spaces wait on is
	// fixed: a user connected (userID), or the provider was saved (userID "").
	OnChange(f func(userID string))
}

// Registry holds the server's providers in memory, by id. Providers that an
// administrator sets up at run time (GitLab, Gitea) come and go, so it is safe
// for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	byID    map[string]Provider
	list    []Provider
	creator Creator
	changed func(provider, userID string)
}

// NewRegistry returns a registry of the providers. Ids must be distinct.
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{byID: map[string]Provider{}}
	for _, p := range ps {
		id := p.Descriptor().ID
		if _, dup := r.byID[id]; dup {
			panic("connections: two providers with the id " + id)
		}
		r.byID[id] = p
		r.list = append(r.list, p)
	}
	r.sort()
	return r
}

func (r *Registry) sort() {
	sort.SliceStable(r.list, func(i, j int) bool { return less(r.list[i].Descriptor(), r.list[j].Descriptor()) })
}

// Add puts a provider in the registry; false when its id is taken.
func (r *Registry) Add(p Provider) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := p.Descriptor().ID
	if _, dup := r.byID[id]; dup {
		return false
	}
	r.byID[id] = p
	r.list = append(r.list, p)
	r.sort()
	if f := r.changed; f != nil {
		p.OnChange(func(userID string) { f(id, userID) })
	}
	return true
}

// Remove takes the provider with the id out of the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return
	}
	delete(r.byID, id)
	out := r.list[:0:0]
	for _, p := range r.list {
		if p.Descriptor().ID != id {
			out = append(out, p)
		}
	}
	r.list = out
}

// SetCreator says what makes the providers an administrator sets up.
func (r *Registry) SetCreator(c Creator) {
	r.mu.Lock()
	r.creator = c
	r.mu.Unlock()
}

// Creator is what the registry has to make a provider, or nil.
func (r *Registry) Creator() Creator {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.creator
}

// typeRank orders the types: GitHub, GitLab, Gitea, the rest by name, git last.
func typeRank(t string) int {
	switch t {
	case "github":
		return 0
	case "gitlab":
		return 1
	case "gitea":
		return 2
	case GitID:
		return 4
	}
	return 3
}

func less(a, b Descriptor) bool {
	if ra, rb := typeRank(a.Type), typeRank(b.Type); ra != rb {
		return ra < rb
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

// Get returns the provider with the id.
func (r *Registry) Get(id string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	return p, ok
}

// GetFor returns the provider with the id when the tenant may see it: a
// provider of the site is everyone's, a tenant's own is its people's only (a
// provider of another tenant is as good as not there).
func (r *Registry) GetFor(id, tenantID string) (Provider, bool) {
	p, ok := r.Get(id)
	if !ok || !VisibleTo(p.Descriptor(), tenantID) {
		return nil, false
	}
	return p, true
}

// VisibleTo says whether people of the tenant may see and use the provider.
func VisibleTo(d Descriptor, tenantID string) bool {
	return d.TenantID == "" || d.TenantID == tenantID
}

// List returns the providers in their stable order.
func (r *Registry) List() []Provider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Provider(nil), r.list...)
}

// ListFor returns the providers the tenant's people may see, in their stable order.
func (r *Registry) ListFor(tenantID string) []Provider {
	var out []Provider
	for _, p := range r.List() {
		if VisibleTo(p.Descriptor(), tenantID) {
			out = append(out, p)
		}
	}
	return out
}

// OnChange registers f on every provider, those that are there now and those
// added later; f is told which provider changed.
func (r *Registry) OnChange(f func(provider, userID string)) {
	r.mu.Lock()
	r.changed = f
	r.mu.Unlock()
	for _, p := range r.List() {
		id := p.Descriptor().ID
		p.OnChange(func(userID string) { f(id, userID) })
	}
}

// Optional parts of a provider, found by type assertion: what only some types
// of provider have.

// Rechecker is a provider that remembers an answer about a person ("the App is
// not installed on your account") for a while; RecheckPerson drops what it remembers
// about the person, so the next read asks the platform again.
type Rechecker interface {
	RecheckPerson(ctx context.Context, userID string)
}

// Administered is a provider an administrator sets up. The values are what the
// API shows as they are (JSON).
type Administered interface {
	AdminView(ctx context.Context) (any, error)
	// AdminSave takes the body of the PUT; it answers the new view.
	AdminSave(ctx context.Context, body []byte) (any, error)
	// AdminCheck reads the configuration from the platform again.
	AdminCheck(ctx context.Context) (any, error)
	AdminDelete(ctx context.Context) error
	// AdminCounts is how many people are connected and spaces sync through it.
	AdminCounts(ctx context.Context) (connections, spaces int, err error)
}

// Installable is a provider whose App is installed on accounts (GitHub).
type Installable interface {
	InstallationsView(ctx context.Context, cursor string) (items any, next string, err error)
	SetInstallationTenants(ctx context.Context, id int64, tenants []string) (any, error)
}

// ManifestRequest asks to make the provider's App on the platform in one click.
type ManifestRequest struct {
	Organization, Name string
}

// ManifestStart is what the browser needs to send the administrator to the
// platform: it posts Manifest (as the form field "manifest") to URL.
type ManifestStart struct {
	URL      string
	Manifest any
	// Binding is the value of the cookie that ties the attempt to this browser.
	Binding string
}

// ManifestMaker is a provider whose App can be made in one click (GitHub).
type ManifestMaker interface {
	StartManifest(ctx context.Context, userID string, req ManifestRequest) (*ManifestStart, error)
	CompleteManifest(ctx context.Context, binding, userID string, q url.Values) (redirect string, clearBinding bool)
	// RetryManifest saves an App the platform made but the server could not
	// save at the time.
	RetryManifest(ctx context.Context, userID string) (any, error)
}

// Creator makes the providers an administrator sets up at run time (GitLab and
// Gitea hosts). The body is the request's JSON; the answer is what the
// administrator sees of the new provider.
type Creator interface {
	CreateProvider(ctx context.Context, actorID string, body []byte) (Provider, any, error)
}

// GitUser is a provider that wants Git to be given a user name other than the
// default one with its token (GitLab's is "oauth2").
type GitUser interface {
	GitUsername() string
}

// GitUsernameForType is the user name Git is given with a token of a kind of
// provider, for when the provider itself is not loaded (a server that has not
// read its providers yet): GitLab and Gitea want "oauth2", GitHub
// "x-access-token". "" for a type this server does not know.
func GitUsernameForType(typ string) string {
	switch typ {
	case "gitlab", "gitea":
		return "oauth2"
	case "github":
		return "x-access-token"
	}
	return ""
}

// Connected is a provider that can say, cheaply, who a user is on the platform.
type Connected interface {
	Login(ctx context.Context, userID string) string
}

// Searcher is a provider whose lists of owners can be narrowed by a search text.
type Searcher interface {
	OwnersQ(ctx context.Context, userID, cursor, q string) ([]Owner, string, error)
}

type actorKey struct{}

// WithActor says who an administrative change is made by.
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

// ActorOf is who an administrative change is made by, "" when not said.
func ActorOf(ctx context.Context) string {
	s, _ := ctx.Value(actorKey{}).(string)
	return s
}
