package spaces

// Spaces that follow a repository on a provider (GitHub's App, ...): connecting
// an existing space, and making a space together with a new repository.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// provider is the provider with the id, or the "not configured" answer.
func (m *Manager) provider(id string) (connections.Provider, error) {
	p, ok := m.opt.Connections.Get(id)
	if ok && id != connections.GitID {
		return p, nil
	}
	name := id
	if ok {
		name = p.Descriptor().Name
	}
	return nil, connections.Errorf(id, http.StatusConflict, connections.CodeNotConfigured, "this server has no %s connection set up", name)
}

// hostAllowed holds a provider's host to --remote-host, as every other remote
// is: a connection is no way around the administrator's list. Only the host is
// checked here (the address itself is the provider's own).
func (m *Manager) hostAllowed(p connections.Provider) error {
	_, err := gitstore.ParseURL("https://"+p.Descriptor().Host+"/owner/name.git", gitstore.Policy{Hosts: m.opt.RemotePolicy.Hosts})
	var he *gitstore.HostError
	if errors.As(err, &he) {
		return he
	}
	if err != nil {
		return invalidAt(p.Descriptor().ID, "the address of this provider is not usable")
	}
	return nil
}

// resolveConnection reads the address of a space's repository and decides how
// it syncs: the address is stored in https form on the provider's host.
func (m *Manager) resolveConnection(ctx context.Context, tenantID string, req ConnectRequest) (gitstore.Remote, *connections.Cred, error) {
	if req.Provider == "" {
		return gitstore.Remote{}, nil, invalidAt("", "credential.provider is required for a connection")
	}
	p, err := m.provider(req.Provider)
	if err != nil {
		return gitstore.Remote{}, nil, err
	}
	d := p.Descriptor()
	owner, name, ok := p.ParseRepoURL(req.URL)
	if !ok {
		return gitstore.Remote{}, nil, invalidAt(d.ID, "a connection goes with a repository on %s (https://%s/owner/name.git or git@%s:owner/name.git)", d.Name, d.Host, d.Host)
	}
	if req.Via != connections.ViaApp && req.Via != connections.ViaUser {
		return gitstore.Remote{}, nil, invalidAt(d.ID, "credential.via must be app or user")
	}
	if err := m.hostAllowed(p); err != nil {
		return gitstore.Remote{}, nil, err
	}
	cred, repo, err := p.ResolveExistingIn(ctx, tenantID, req.UserID, owner, name, req.Via)
	if err != nil {
		return gitstore.Remote{}, nil, err
	}
	u := p.RepoURL(repo.Owner, repo.Name)
	scheme := "https"
	if strings.HasPrefix(u, "http://") {
		scheme = "http"
	}
	return gitstore.Remote{URL: u, Scheme: scheme, Host: strings.ToLower(d.Host)}, cred, nil
}

// SyncCreate is a new space's repository on a provider.
type SyncCreate struct {
	Provider    string
	Owner, Name string
	Private     bool
	Description string
	// Create makes the repository; false connects the space to the one that is
	// there already.
	Create bool
}

// CreateWithSync makes a space together with its repository on a provider, in
// the order of docs/api.md ("Creating a space with a repository"): nothing is
// visible, and nothing stays on the server, unless every step works. An empty
// repository left on the platform by a failure after it was made is named in
// the error, so the same request with Create false can use it.
func (m *Manager) CreateWithSync(ctx context.Context, userID, name, inbox string, g SyncCreate) (Info, error) {
	if m.single != nil {
		return Info{}, ErrNotFound
	}
	gh, err := m.provider(g.Provider)
	if err != nil {
		return Info{}, err
	}
	if inbox == "" {
		inbox = store.DefaultInbox
	}
	if inbox, err = ValidInbox(inbox); err != nil {
		return Info{}, err
	}
	if name, err = ValidName(name); err != nil {
		return Info{}, err
	}
	if err := m.hostAllowed(gh); err != nil {
		return Info{}, err
	}
	if !gh.ValidRepoName(g.Name) {
		return Info{}, invalidAt(gh.Descriptor().ID, "storage.sync.name is 1 to 100 characters of letters, digits, '.', '_' and '-', not '.' or '..' and not ending in .git")
	}
	if utf8.RuneCountInString(g.Description) > 350 || !utf8.ValidString(g.Description) ||
		strings.IndexFunc(g.Description, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return Info{}, invalidAt(gh.Descriptor().ID, "storage.sync.description is at most 350 characters, without control characters")
	}
	// The limit counts the spaces being made as well as the ones that exist.
	// The space is the creator's tenant's, as Create's is.
	m.createMu.Lock()
	u, err := store.GetUser(ctx, m.opt.Store.Q(), store.AllTenants(), userID) // the creator, by id
	if err != nil {
		m.createMu.Unlock()
		return Info{}, err
	}
	if err := m.checkSpaceQuota(ctx, *u); err != nil {
		m.createMu.Unlock()
		return Info{}, err
	}
	tenantID := u.TenantID
	m.reserved[userID]++
	m.reservedT[tenantID]++
	m.createMu.Unlock()
	held := true
	unreserve := func() { // under createMu
		m.reserved[userID]--
		if m.reserved[userID] == 0 {
			delete(m.reserved, userID)
		}
		m.reservedT[tenantID]--
		if m.reservedT[tenantID] == 0 {
			delete(m.reservedT, tenantID)
		}
		held = false
	}
	release := func() { // gives the place back: under createMu, once
		if held {
			m.createMu.Lock()
			unreserve()
			m.createMu.Unlock()
		}
	}
	defer release()

	// The work goes on if the client goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()

	var plan *connections.Plan
	var cred *connections.Cred
	var repo *connections.Repo
	if g.Create {
		if plan, err = gh.PlanCreate(ctx, userID, g.Owner); err != nil {
			return Info{}, err
		}
	} else {
		if cred, repo, err = gh.ResolveExistingIn(ctx, tenantID, userID, g.Owner, g.Name, ""); err != nil {
			return Info{}, err
		}
	}

	stg, err := m.Stage(name, IdentityOf(*u))
	if err != nil {
		return Info{}, err
	}
	committed := false
	defer func() {
		if !committed {
			stg.Abort()
		}
	}()

	created := false
	if g.Create {
		if repo, err = gh.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: g.Name, Private: g.Private, Description: g.Description}); err != nil {
			return Info{}, err
		}
		created = true
		m.hook("repo-created")
	}
	// From here on the repository exists: a failure leaves it empty on GitHub
	// and says so.
	failed := func(code string, status int, err error) error {
		if !created {
			return err
		}
		// A reminder the person sees later, so the repository is not forgotten.
		if rerr := gh.RecordLeftover(ctx, userID, repo, name); rerr != nil {
			m.log.Warn("could not remember a repository left empty by a failed space", "repo", repo.FullName, "error", rerr)
		}
		var he *connections.Error
		if errors.As(err, &he) && (he.Code == connections.CodeRepoNotInInstall || he.Code == connections.CodeMissingPermission) {
			he.Repo = repo.Ref()
			return he
		}
		return &connections.Error{Provider: g.Provider, Code: code, Status: status, Msg: err.Error(), Repo: repo.Ref(), Err: err}
	}
	if created {
		if cred, err = gh.ChooseCredential(ctx, plan, repo); err != nil {
			return Info{}, failed(connections.CodePushFailed, http.StatusBadGateway, err)
		}
	}
	id := stg.ID()
	m.mu.Lock()
	m.staging[id] = true
	m.mu.Unlock()
	endStaging := func() { m.mu.Lock(); delete(m.staging, id); m.mu.Unlock() }
	defer endStaging()
	row := store.Remote{SpaceID: id, URL: gh.RepoURL(repo.Owner, repo.Name), Branch: "main", CredType: CredConnection, Provider: g.Provider, State: store.RemoteOK,
		GHVia: cred.Via, GHInstallationID: cred.InstallationID, GHRepoID: cred.RepoID, GHUserID: cred.UserID, Updated: m.opt.Now()}
	if err := m.connectRetry(ctx, stg.sp, *m.remoteOptions(id, &row)); err != nil {
		return Info{}, failed(connections.CodePushFailed, http.StatusBadGateway, err)
	}
	m.hook("connected")
	st, _ := stg.sp.Remote()
	row.State, row.LastGood, row.LastSync = st.State, st.LastGood, st.LastSync
	now := m.opt.Now()
	m.createMu.Lock()
	unreserve() // the row below takes the place
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.InsertSpace(ctx, q, tenantID, id, stg.Name(), "", inbox, now); err != nil {
			return err
		}
		if err := store.AddMember(ctx, q, id, userID, perm.Owner, now); err != nil {
			return err
		}
		return store.PutRemote(ctx, q, row)
	})
	if err != nil {
		m.createMu.Unlock()
		m.log.Error("could not record a space made with a repository on a provider", "error", err)
		return Info{}, failed(connections.CodeInternal, http.StatusInternalServerError, errors.New("the space could not be recorded; nothing was kept"))
	}
	m.createMu.Unlock()
	endStaging() // the space has its member now: the checks apply from here on
	committed = true
	stg.Commit(userID)
	m.usageAdded(ctx, store.Touched{UserID: userID, TenantID: tenantID})
	m.setSync(id, row.State)
	m.schedulePull(id)
	return Info{ID: id, Name: stg.Name(), Role: perm.Owner, Perms: perm.All, Inbox: inbox, Sync: row.State}, nil
}

func (m *Manager) hook(step string) {
	if m.opt.SyncHook != nil {
		m.opt.SyncHook(step)
	}
}

// connectRetry connects a new space to its repository. A repository GitHub has
// just made is sometimes not found for a few seconds, so "not found" and a
// failure of GitHub's own are tried again after the delays of
// Options.PushRetry, all within Options.PushDeadline: the deadline covers each
// push as well as the waits, and a try that would start after it is not made.
// GitHub also answers "not found" for a credential it does not accept on a
// private repository, which cannot be told apart here; the deadline is what
// bounds that case. Every other refusal of the credential, and a credential
// that could not be had at all, is not tried again.
func (m *Manager) connectRetry(ctx context.Context, sp *space.Space, ro space.RemoteOptions) error {
	ctx, cancel := context.WithTimeout(ctx, m.opt.PushDeadline)
	defer cancel()
	delays := m.opt.PushRetry
	for i := 0; ; i++ {
		err := sp.Connect(ctx, ro)
		if err == nil || i >= len(delays) || !retryable(err) {
			return err
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= delays[i] {
			return err // the next try would start after the deadline
		}
		t := time.NewTimer(delays[i])
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
	}
}

// retryable: the repository is "not found" (perhaps not yet there), GitHub
// failed, or it could not be reached. Never a credential that could not be had.
func retryable(err error) bool {
	var ce *gitstore.CredentialError
	if errors.As(err, &ce) {
		return false
	}
	var ue *space.UnavailableError
	if !errors.As(err, &ue) {
		return false
	}
	var ge *gitstore.GitError
	text := ue.Msg
	if errors.As(err, &ge) {
		text = ge.Stderr
	}
	low := strings.ToLower(text)
	for _, s := range []string{"not found", "returned error: 5", "could not reach"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

// ConnectionFixed tells the manager that what spaces with a connection
// credential were waiting on is fixed (userID connected again; "" the
// provider was saved): the open spaces that were in one of those states are
// fetched now, without waiting for their turn.
func (m *Manager) ConnectionFixed(userID string) {
	if m.single != nil {
		return
	}
	var due []*opening
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	for _, e := range m.open {
		select {
		case <-e.done:
		default:
			continue
		}
		if e.closing != nil || e.sp == nil {
			continue
		}
		if st, ok := e.sp.Remote(); ok && st.State == space.RemoteError && space.IsConnectionKind(st.Kind) {
			e.refs++
			due = append(due, e)
		}
	}
	m.mu.Unlock()
	for _, e := range due {
		go func() {
			defer func() { m.mu.Lock(); e.refs--; m.mu.Unlock() }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var ue *space.UnavailableError
			if err := e.sp.Pull(ctx, true); err != nil && !errors.As(err, &ue) && !errors.Is(err, space.ErrClosed) {
				m.log.Warn("fetching a space again after its connection was fixed failed", "error", err)
			}
		}()
	}
}

// invalidAt is a 400 of a connection route: it carries the provider's id like
// every error of a connection, and still is an ErrInvalid.
func invalidAt(provider, format string, a ...any) error {
	e := connections.Errorf(provider, http.StatusBadRequest, connections.CodeInvalid, format, a...)
	e.Err = ErrInvalid
	return e
}
