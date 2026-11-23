package spaces

// Spaces that follow a repository on GitHub through the server's GitHub App:
// connecting an existing space, and making a space together with a new
// repository.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func (m *Manager) needGitHub() (*github.Service, error) {
	if m.opt.GitHub == nil {
		return nil, &github.Error{Code: github.CodeNotConfigured, Status: http.StatusConflict, Msg: "this server has no GitHub App set up"}
	}
	return m.opt.GitHub, nil
}

// resolveGitHub reads the address of a space's repository and decides how it
// syncs: the address is stored in https form on the server's GitHub.
func (m *Manager) resolveGitHub(ctx context.Context, req ConnectRequest) (gitstore.Remote, *github.Cred, error) {
	gh, err := m.needGitHub()
	if err != nil {
		return gitstore.Remote{}, nil, err
	}
	owner, name, ok := gh.ParseRepoURL(req.URL)
	if !ok {
		return gitstore.Remote{}, nil, fmt.Errorf("%w: a github credential goes with a repository on GitHub (https://github.com/owner/name.git or git@github.com:owner/name.git)", ErrInvalid)
	}
	if req.Via != github.ViaApp && req.Via != github.ViaUser {
		return gitstore.Remote{}, nil, fmt.Errorf("%w: credential.via must be app or user", ErrInvalid)
	}
	cred, repo, err := gh.ResolveExisting(ctx, req.UserID, owner, name, req.Via)
	if err != nil {
		return gitstore.Remote{}, nil, err
	}
	u := gh.Client().RepoURL(repo.Owner, repo.Name)
	scheme := "https"
	if strings.HasPrefix(u, "http://") {
		scheme = "http"
	}
	return gitstore.Remote{URL: u, Scheme: scheme, Host: strings.ToLower(gh.Client().Host())}, cred, nil
}

// GitHubCreate is a new space's repository on GitHub.
type GitHubCreate struct {
	Owner, Name string
	Private     bool
	Description string
	// Create makes the repository; false connects the space to the one that is
	// there already.
	Create bool
}

// CreateWithGitHub makes a space together with its repository on GitHub, in the
// order of docs/api.md ("Creating a space with a GitHub repository"): nothing
// is visible, and nothing stays on the server, unless every step works. An
// empty repository left on GitHub by a failure after it was made is named in
// the error, so the same request with Create false can use it.
func (m *Manager) CreateWithGitHub(ctx context.Context, userID, name, inbox string, g GitHubCreate) (Info, error) {
	if m.single != nil {
		return Info{}, ErrNotFound
	}
	gh, err := m.needGitHub()
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
	if !github.ValidRepoName(g.Name) {
		return Info{}, fmt.Errorf("%w: github.name is 1 to 100 characters of letters, digits, '.', '_' and '-', not '.' or '..' and not ending in .git", ErrInvalid)
	}
	if utf8.RuneCountInString(g.Description) > 350 || !utf8.ValidString(g.Description) ||
		strings.IndexFunc(g.Description, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return Info{}, fmt.Errorf("%w: github.description is at most 350 characters, without control characters", ErrInvalid)
	}
	// The limit counts the spaces being made as well as the ones that exist.
	m.createMu.Lock()
	u, err := store.GetUser(ctx, m.opt.Store.Q(), userID)
	if err != nil {
		m.createMu.Unlock()
		return Info{}, err
	}
	if m.opt.MaxSpaces > 0 {
		n, err := store.CountOwnedSpaces(ctx, m.opt.Store.Q(), userID)
		if err != nil {
			m.createMu.Unlock()
			return Info{}, err
		}
		if n+m.reserved[userID] >= m.opt.MaxSpaces {
			m.createMu.Unlock()
			return Info{}, fmt.Errorf("%w: you already own %d spaces, the most this server allows", ErrLimit, n)
		}
	}
	m.reserved[userID]++
	m.createMu.Unlock()
	held := true
	release := func() { // gives the place back: under createMu, once
		if held {
			m.createMu.Lock()
			m.reserved[userID]--
			m.createMu.Unlock()
			held = false
		}
	}
	defer release()

	// The work goes on if the client goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()

	var plan *github.Plan
	var cred *github.Cred
	var repo *github.Repo
	if g.Create {
		if plan, err = gh.PlanCreate(ctx, userID, g.Owner); err != nil {
			return Info{}, err
		}
	} else {
		if cred, repo, err = gh.ResolveExisting(ctx, userID, g.Owner, g.Name, ""); err != nil {
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
		if repo, err = gh.CreateRepo(ctx, plan, github.NewRepoSpec{Name: g.Name, Private: g.Private, Description: g.Description}); err != nil {
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
		var he *github.Error
		if errors.As(err, &he) && he.Code == github.CodeRepoNotInInstall {
			he.Repo = repo.Ref()
			return he
		}
		return &github.Error{Code: code, Status: status, Msg: err.Error(), Repo: repo.Ref(), Err: err}
	}
	if created {
		if cred, err = gh.ChooseCredential(ctx, plan, repo); err != nil {
			return Info{}, failed(github.CodePushFailed, http.StatusBadGateway, err)
		}
	}
	id := stg.ID()
	m.mu.Lock()
	m.staging[id] = true
	m.mu.Unlock()
	endStaging := func() { m.mu.Lock(); delete(m.staging, id); m.mu.Unlock() }
	defer endStaging()
	row := store.Remote{SpaceID: id, URL: gh.Client().RepoURL(repo.Owner, repo.Name), Branch: "main", CredType: CredGitHub, State: store.RemoteOK,
		GHVia: cred.Via, GHInstallationID: cred.InstallationID, GHRepoID: cred.RepoID, GHUserID: cred.UserID, Updated: m.opt.Now()}
	if err := m.connectRetry(ctx, stg.sp, *m.remoteOptions(id, &row)); err != nil {
		return Info{}, failed(github.CodePushFailed, http.StatusBadGateway, err)
	}
	m.hook("connected")
	st, _ := stg.sp.Remote()
	row.State, row.LastGood, row.LastSync = st.State, st.LastGood, st.LastSync
	now := m.opt.Now()
	m.createMu.Lock()
	m.reserved[userID]-- // the row below takes the place
	held = false
	err = m.opt.Store.Tx(ctx, func(q store.Q) error {
		if err := store.InsertSpace(ctx, q, id, stg.Name(), "", inbox, now); err != nil {
			return err
		}
		if err := store.AddMember(ctx, q, id, userID, perm.Owner, now); err != nil {
			return err
		}
		return store.PutRemote(ctx, q, row)
	})
	if err != nil {
		m.createMu.Unlock()
		m.log.Error("could not record a space made with a GitHub repository", "error", err)
		return Info{}, failed(github.CodeInternal, http.StatusInternalServerError, errors.New("the space could not be recorded; nothing was kept"))
	}
	m.createMu.Unlock()
	endStaging() // the space has its member now: the checks apply from here on
	committed = true
	stg.Commit(userID)
	m.setSync(id, row.State)
	m.schedulePull(id)
	return Info{ID: id, Name: stg.Name(), Role: perm.Owner, Perms: perm.All, Inbox: inbox, Sync: row.State}, nil
}

func (m *Manager) hook(step string) {
	if m.opt.GitHubHook != nil {
		m.opt.GitHubHook(step)
	}
}

// connectRetry connects a new space to its repository. A repository GitHub has
// just made is sometimes not found for a few seconds, so "not found" and a
// failure of GitHub's own are tried again, four tries in all within the delays
// of Options.PushRetry; a credential GitHub refuses is not.
func (m *Manager) connectRetry(ctx context.Context, sp *space.Space, ro space.RemoteOptions) error {
	delays := m.opt.PushRetry
	for i := 0; ; i++ {
		err := sp.Connect(ctx, ro)
		if err == nil || i >= len(delays) || !retryable(err) {
			return err
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

// retryable: the repository is "not found" (not yet there), or GitHub failed.
func retryable(err error) bool {
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

// GitHubFixed tells the manager that what spaces with a GitHub credential were
// waiting on is fixed (userID connected again; "" the App was saved): the open
// spaces that were in one of those states are fetched now, without waiting for
// their turn.
func (m *Manager) GitHubFixed(userID string) {
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
		if st, ok := e.sp.Remote(); ok && st.State == space.RemoteError && space.IsGitHubKind(st.Kind) {
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
				m.log.Warn("fetching a space again after its GitHub credential was fixed failed", "error", err)
			}
		}()
	}
}
