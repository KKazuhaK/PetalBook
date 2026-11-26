package spaces

// The attachment repository of a space (milestone 3.1, docs/api.md
// "Attachment repository"): connecting it, serving and storing attachments
// through it, moving the attachments of the space into it in the background,
// and what eviction has to ask of it.
//
// The pieces: internal/assetcache (the contents, shared bounded cache),
// internal/assetrepo (the meta clone, the table of files, the pushes), and
// here the rows in the state database, the credential, the move and the
// hooks into opening, pulling and evicting a space.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/assetcache"
	"petalbook.app/server/internal/assetrepo"
	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
)

// ErrNoAssets: the space has no attachment repository (404).
var ErrNoAssets = errors.New("this space has no attachment repository")

// ErrAssetsConflict: the attachment repository cannot be changed now (409 conflict).
var ErrAssetsConflict = errors.New("the attachment repository cannot be changed now")

func assetsConflict(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrAssetsConflict, fmt.Sprintf(format, a...))
}

// assetState is what the manager keeps for attachment repositories.
type assetState struct {
	once  sync.Once
	cache *assetcache.Cache
	err   error

	mu        sync.Mutex
	repos     map[string]*assetrepo.Repo
	moving    map[string]bool   // spaces whose uploads go into assets/: a move is queued, or copying
	phase     map[string]string // the phase of each move queued or running here (MoveCopying, MoveSwitching)
	pinned    map[string]bool
	tenants   map[string]string // the tenant of each space the cache named, remembered (AssetBytesOfTenant)
	connected map[string]bool   // spaces that have an attachment repository; nil until first needed
	// opening holds a lock per space, taken to open its repository from its
	// row and to replace or remove its directory: an open in the middle of a
	// swap would make the directory again, from the row before it.
	opening map[string]*sync.Mutex
}

func (m *Manager) assetsDir(id string) string { return filepath.Join(m.opt.DataDir, "assetrepos", id) }

// AssetCache is the cache of attachment contents.
func (m *Manager) AssetCache() *assetcache.Cache {
	m.as.once.Do(func() {
		m.as.repos = map[string]*assetrepo.Repo{}
		m.as.moving = map[string]bool{}
		m.as.phase = map[string]string{}
		m.as.pinned = map[string]bool{}
		c, err := assetcache.Open(assetcache.Options{
			Dir:    filepath.Join(m.opt.DataDir, "assetcache"),
			Budget: func() int64 { return m.Storage().Cache.AssetsBytes },
			Pinned: func(id string) bool {
				m.as.mu.Lock()
				defer m.as.mu.Unlock()
				return m.as.pinned[id]
			},
			Now: m.opt.Now,
		})
		if err != nil {
			m.as.err = err
			m.log.Error("could not open the attachment cache", "error", err)
			return
		}
		m.as.cache = c
		if ids, err := store.PinnedSpaceIDs(context.Background(), m.opt.Store.Q()); err == nil {
			for _, id := range ids {
				m.as.pinned[id] = true
			}
		}
	})
	return m.as.cache
}

func (m *Manager) notePinned(id string, pinned bool) {
	m.AssetCache()
	m.as.mu.Lock()
	defer m.as.mu.Unlock()
	if pinned {
		m.as.pinned[id] = true
	} else {
		delete(m.as.pinned, id)
	}
}

// AssetBytes is what the space's attachments take in the cache.
func (m *Manager) AssetBytes(id string) int64 {
	if m.single != nil {
		return 0
	}
	if c := m.AssetCache(); c != nil {
		return c.SpaceBytes(id)
	}
	return 0
}

// AssetBytesOfTenant is what the attachments of one tenant's spaces take in the
// cache: the cache's per-space map in memory, and the tenant of each space it
// names, which is remembered (one query for all the spaces not seen before,
// none after).
func (m *Manager) AssetBytesOfTenant(ctx context.Context, tenant string) (int64, error) {
	if m.single != nil {
		return 0, nil
	}
	c := m.AssetCache()
	if c == nil {
		return 0, nil
	}
	cached := c.Spaces()
	// The tenants are copied out for this call: another may clear the memory
	// (at its bound) between the lookup and the sum, which must not undercount.
	tenants := make(map[string]string, len(cached))
	var unknown []string
	m.as.mu.Lock()
	for id := range cached {
		if t, ok := m.as.tenants[id]; ok {
			tenants[id] = t
		} else {
			unknown = append(unknown, id)
		}
	}
	m.as.mu.Unlock()
	if len(unknown) > 0 {
		if testHookTenantLookup != nil {
			testHookTenantLookup()
		}
		found, err := store.SpaceTenants(ctx, m.opt.Store.Q(), unknown) // a space that is gone is not there: its files are dropped with it
		if err != nil {
			return 0, err
		}
		m.as.mu.Lock()
		if m.as.tenants == nil || len(m.as.tenants)+len(unknown) >= 50_000 {
			m.as.tenants = map[string]string{}
		}
		for _, id := range unknown {
			t, ok := found[id]
			if !ok {
				t = goneTenant // remembered too, so it is not asked about on every call
			}
			m.as.tenants[id] = t // a space never changes tenant
			tenants[id] = t
		}
		m.as.mu.Unlock()
	}
	var total int64
	for id, b := range cached {
		if t := tenants[id]; t != goneTenant && t == tenant {
			total += b
		}
	}
	return total, nil
}

// goneTenant is the tenant remembered for a cached space the database no longer
// has: no tenant's id.
const goneTenant = "\x00gone"

// testHookTenantLookup is called before each lookup of the tenants of cached
// spaces in the database (a test counts them).
var testHookTenantLookup func()

// AssetTotals are the bytes of the attachment cache and its budget.
func (m *Manager) AssetTotals() (total, budget int64) {
	if m.single != nil {
		return 0, 0
	}
	if c := m.AssetCache(); c != nil {
		return c.Total(), c.Budget()
	}
	return 0, m.Storage().Cache.AssetsBytes
}

// AssetsMode is what the Space object's storage.assets says: "repository" when
// the space has an attachment repository, else "space".
func (m *Manager) AssetsMode(ctx context.Context, id string) string {
	if m.single != nil {
		return "space"
	}
	m.AssetCache()
	m.as.mu.Lock()
	if m.as.connected == nil {
		m.as.mu.Unlock()
		set, err := store.AssetRepositories(ctx, m.opt.Store.Q())
		if err != nil {
			return "space"
		}
		m.as.mu.Lock()
		if m.as.connected == nil {
			m.as.connected = set
		}
	}
	defer m.as.mu.Unlock()
	if _, ok := m.as.connected[id]; ok {
		return "repository"
	}
	return "space"
}

func (m *Manager) noteConnected(id string, on bool) {
	m.AssetCache()
	m.as.mu.Lock()
	defer m.as.mu.Unlock()
	if m.as.connected == nil {
		return // read from the database when first needed
	}
	if on {
		m.as.connected[id] = false
	} else {
		delete(m.as.connected, id)
	}
}

// --- the repository of a space ---------------------------------------------------------

func assetStatusOf(r *store.Remote) space.RemoteStatus { return statusOf(r) }

func (m *Manager) persistAssetStatus(id string) func(space.RemoteStatus) {
	return func(st space.RemoteStatus) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := store.UpdateRemoteStatus(ctx, m.opt.Store.Q(), id, store.RoleAssets, store.RemoteStatus{
			State: st.State, ErrorKind: st.Kind, LastError: st.Error, LastGood: st.LastGood, RemoteTip: st.RemoteTip, LastSync: st.LastSync,
		}, m.opt.Now())
		if err != nil {
			m.log.Warn("could not store the state of an attachment repository", "space", id, "error", err)
		}
	}
}

func (m *Manager) openAssetRepo(ctx context.Context, id string, row *store.Remote) (*assetrepo.Repo, error) {
	cache := m.AssetCache()
	if cache == nil {
		return nil, errors.New("the attachment cache is not available")
	}
	return assetrepo.Open(ctx, assetrepo.Options{
		Space: id, Dir: m.assetsDir(id), URL: row.URL, Branch: row.Branch,
		Cred: m.credential(id, row), Cache: cache, Status: assetStatusOf(row),
		OnStatus: m.persistAssetStatus(id),
		OnUsed: func() error {
			// Tried a few times here; the repository tries again at its next
			// push when it still fails.
			var err error
			for try := 0; try < 3; try++ {
				if try > 0 {
					time.Sleep(time.Duration(try) * 200 * time.Millisecond)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err = store.MarkAssetsUsed(ctx, m.opt.Store.Q(), id)
				cancel()
				if err == nil {
					return nil
				}
			}
			m.log.Warn("could not record that an attachment went to the attachment repository", "space", id, "error", err)
			return err
		},
		Runner: m.opt.RemoteRunner, AllowUnsafe: m.opt.RemotePolicy.AllowUnsafe,
		NetTimeout: m.opt.NetTimeout, FetchFresh: m.opt.FetchFresh, Logger: m.log.With("space", id, "repository", "assets"),
		ClearStaleLocks: m.opt.Serve, // the server holds serve.lock
	})
}

// assetOpenLock is the lock of the space's repository directory (assetState.opening).
func (m *Manager) assetOpenLock(id string) *sync.Mutex {
	m.as.mu.Lock()
	defer m.as.mu.Unlock()
	if m.as.opening == nil {
		m.as.opening = map[string]*sync.Mutex{}
	}
	l := m.as.opening[id]
	if l == nil {
		l = &sync.Mutex{}
		m.as.opening[id] = l
	}
	return l
}

// assetRepo returns the attachment repository of the space, opened from its
// row (no network), or nil when the space has none.
func (m *Manager) assetRepo(ctx context.Context, id string) (*assetrepo.Repo, *store.Remote, error) {
	if m.single != nil {
		return nil, nil, nil
	}
	m.AssetCache()
	row, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	m.as.mu.Lock()
	r := m.as.repos[id]
	m.as.mu.Unlock()
	if r != nil {
		return r, row, nil
	}
	l := m.assetOpenLock(id)
	l.Lock()
	defer l.Unlock()
	return m.assetRepoLocked(ctx, id)
}

// assetRepoLocked is assetRepo for a caller that holds the space's open lock:
// the row is read again under it, as a swap may have changed it.
func (m *Manager) assetRepoLocked(ctx context.Context, id string) (*assetrepo.Repo, *store.Remote, error) {
	row, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	m.as.mu.Lock()
	r := m.as.repos[id]
	m.as.mu.Unlock()
	if r != nil {
		return r, row, nil
	}
	r, err = m.openAssetRepo(ctx, id, row)
	if err != nil {
		return nil, nil, err
	}
	m.as.mu.Lock()
	if cur := m.as.repos[id]; cur != nil {
		m.as.mu.Unlock()
		r.Close()
		return cur, row, nil
	}
	m.as.repos[id] = r
	m.as.mu.Unlock()
	return r, row, nil
}

func (m *Manager) forgetAssetRepo(id string) {
	m.as.mu.Lock()
	r := m.as.repos[id]
	delete(m.as.repos, id)
	m.as.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// backend is the space's view of its attachment repository.
type backend struct {
	m  *Manager
	id string
	r  *assetrepo.Repo
}

func (b backend) Lookup(rel string) (space.AssetInfo, bool) { return b.r.Lookup(rel) }

func (b backend) Open(ctx context.Context, rel string) (*os.File, space.AssetInfo, error) {
	return b.r.Open(ctx, rel)
}

// Direct: uploads go to the repository, except while the attachments of the
// space are being copied (they go into assets/ of the space then, and the
// switch takes them over).
func (b backend) Direct() bool {
	b.m.as.mu.Lock()
	defer b.m.as.mu.Unlock()
	return !b.m.as.moving[b.id]
}

func (b backend) Put(ctx context.Context, files []space.AssetFile, author, committer gitstore.Identity) error {
	fs := make([]assetrepo.File, len(files))
	for i, f := range files {
		fs[i] = assetrepo.File{Rel: f.Rel, Data: f.Data}
	}
	msg := "Add an attachment"
	if len(files) > 1 {
		msg = fmt.Sprintf("Add %d attachments", len(files))
	}
	return b.r.Put(ctx, fs, author, committer, msg)
}

// attachAssets gives a space that was just opened its attachment repository.
// Nothing here touches the network: the table is what the last refresh left,
// and a refresh follows in the background.
func (m *Manager) attachAssets(ctx context.Context, id string, sp *space.Space) {
	r, _, err := m.assetRepo(ctx, id)
	if err != nil {
		m.log.Error("could not open the attachment repository of a space; its attachments are not available", "space", id, "error", err)
		return
	}
	if r == nil {
		return
	}
	m.settleSwitch(ctx, id, sp)
	sp.SetAssets(backend{m, id, r})
	m.goBg(func() { m.refreshAssets(id, sp) })
}

// refreshAssets brings the table up to date with the remote (the fetch of a
// pull), and the checks that said an attachment was missing with it.
func (m *Manager) refreshAssets(id string, sp *space.Space) {
	ctx, cancel := context.WithTimeout(m.closedCtx(), 2*time.Minute)
	defer cancel()
	r, _, err := m.assetRepo(ctx, id)
	if err != nil || r == nil {
		return
	}
	added, _, err := r.Refresh(ctx, false)
	if err != nil {
		var ue *space.UnavailableError
		if !errors.As(err, &ue) {
			m.log.Warn("could not refresh the attachment table", "space", id, "error", err)
		}
		return
	}
	if len(added) > 0 && len(added) <= 5000 {
		sp.AssetsAppeared(added)
	}
}

// --- the views --------------------------------------------------------------------------

// MoveView is how far moving the attachments has come.
type MoveView struct {
	State            string
	Files, FilesDone int64
	Bytes, BytesDone int64
	Error            string
}

// AssetsView is the attachment repository as the API shows it.
type AssetsView struct {
	RemoteView
	Move *MoveView
}

func (m *Manager) assetsView(ctx context.Context, h *Handle, row *store.Remote) *AssetsView {
	v := &AssetsView{RemoteView: RemoteView{URL: row.URL, Branch: row.Branch, CredType: row.CredType, Username: row.Username,
		State: row.State, LastSync: row.LastSync, Error: row.LastError}}
	if row.State == store.RemoteError {
		v.ErrorCode = row.ErrorKind
	}
	if r, _, _ := m.assetRepo(context.Background(), row.SpaceID); r != nil {
		st := r.Status()
		v.State, v.LastSync, v.Error, v.ErrorCode = st.State, st.LastSync, st.Error, ""
		if st.State == space.RemoteError {
			v.ErrorCode = st.Kind
		}
	}
	v.Provider = row.Provider
	if v.Provider == "" {
		v.Provider = store.ProviderGit
	}
	switch row.CredType {
	case CredConnection:
		v.Connection = m.connectionView(row)
	case CredSSH:
		v.PublicKey = row.PubKey
		v.Fingerprint, _ = gitstore.Fingerprint(row.PubKey)
		v.HostKey = gitstore.HostKeyFingerprint(m.knownHosts(row.SpaceID))
	}
	v.Move = m.moveView(ctx, row)
	return v
}

// AssetsRemote returns the attachment repository of the space, or ErrNoRemote
// when it has none.
func (m *Manager) AssetsRemote(ctx context.Context, h *Handle) (*AssetsView, error) {
	if m.single != nil {
		return nil, ErrNotFound
	}
	row, err := store.GetRemote(ctx, m.opt.Store.Q(), h.Info.ID, store.RoleAssets)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return nil, ErrNoAssets
	}
	if err != nil {
		return nil, err
	}
	m.settleMove(ctx, h, row)
	return m.assetsView(ctx, h, row), nil
}

// --- connecting --------------------------------------------------------------------------

func (m *Manager) needSpaceRemote(ctx context.Context, id string) (*store.Remote, error) {
	r, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleContent)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !r.Connected()) {
		require := "any"
		if r := m.Storage().Remotes.Require; r != nil {
			require = *r
		}
		return nil, &RequiredError{Require: require, Msg: "The space has to follow a remote repository before it can have an attachment repository."}
	}
	return r, err
}

// ConnectAssets connects the attachment repository of the space, or replaces
// its address and credential. The repository is looked at before anything is
// stored: an unreachable one, or one the space cannot take, changes nothing.
func (m *Manager) ConnectAssets(ctx context.Context, h *Handle, req ConnectRequest) (*AssetsView, error) {
	if m.single != nil {
		return nil, ErrNotFound
	}
	if _, err := m.box(); err != nil {
		return nil, err
	}
	id := h.Info.ID
	spaceRow, err := m.needSpaceRemote(ctx, id)
	if err != nil {
		return nil, err
	}
	var remote gitstore.Remote
	var ghCred *connections.Cred
	if req.CredType == CredConnection {
		// As for the space's own repository (Connect): a person's connection
		// syncs only spaces of their own tenant.
		sp, err := store.GetSpace(ctx, m.opt.Store.Q(), store.AllTenants(), id) // the space the caller has open, by id
		if err != nil {
			return nil, err
		}
		if req.Via == connections.ViaUser {
			if err := m.sameTenant(ctx, req.Provider, sp.TenantID, req.UserID); err != nil {
				return nil, err
			}
		}
		if remote, ghCred, err = m.resolveConnection(ctx, sp.TenantID, req); err != nil {
			return nil, err
		}
	} else if remote, err = gitstore.ParseURL(req.URL, m.hostPolicy()); err != nil {
		return nil, err
	}
	return m.connectAssetsWith(ctx, h, req, remote, ghCred, spaceRow)
}

func (m *Manager) connectAssetsWith(ctx context.Context, h *Handle, req ConnectRequest, remote gitstore.Remote, ghCred *connections.Cred, spaceRow *store.Remote) (*AssetsView, error) {
	box, err := m.box()
	if err != nil {
		return nil, err
	}
	id := h.Info.ID
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "main"
	}
	if err := gitstore.ValidBranch(branch); err != nil {
		return nil, fmt.Errorf("%w: %s is not a usable branch name", ErrInvalid, branch)
	}
	m.remoteMu.Lock()
	defer m.remoteMu.Unlock()
	old, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if old != nil && old.URL == "" {
		old = nil
	}
	if old != nil && m.moveActive(ctx, old) {
		return nil, assetsConflict("The attachments are being moved.")
	}
	if old != nil && old.HasAssets() && old.URL != remote.URL {
		return nil, assetsConflict("Attachments are in the attachment repository already; it cannot be replaced by another one.")
	}
	if old != nil && old.URL != remote.URL {
		// Files it had when it was connected (or that went to it without being
		// recorded): the space may refer to them.
		if r, _, _ := m.assetRepo(ctx, id); r != nil && r.Count() > 0 {
			return nil, assetsConflict("The attachment repository has files; it cannot be replaced by another one.")
		}
	}
	row := store.Remote{SpaceID: id, URL: remote.URL, Branch: branch, State: store.RemoteOK, Updated: m.opt.Now()}
	switch req.CredType {
	case CredToken:
		if remote.Scheme != "https" && remote.Scheme != "http" && remote.Scheme != "file" {
			return nil, fmt.Errorf("%w: a token goes with an https address; use an ssh key for %s", ErrInvalid, remote.URL)
		}
		row.CredType = CredToken
		row.Username = strings.TrimSpace(req.Username)
		if row.Username == "" && old != nil {
			row.Username = old.Username
		}
		if row.Username == "" {
			row.Username = "x-access-token"
		}
		if strings.ContainsAny(row.Username, " \t\r\n:\x00") {
			return nil, fmt.Errorf("%w: the user name is not usable", ErrInvalid)
		}
		switch {
		case req.Token != "":
			if strings.ContainsAny(req.Token, "\r\n\x00") || len(req.Token) > 4096 {
				return nil, fmt.Errorf("%w: the token is not usable", ErrInvalid)
			}
			if row.TokenEnc, err = box.Seal(id, "token", []byte(req.Token)); err != nil {
				return nil, err
			}
		case old != nil && old.TokenEnc != "" && old.URL == remote.URL:
			row.TokenEnc = old.TokenEnc
		default:
			return nil, fmt.Errorf("%w: a token is required", ErrInvalid)
		}
	case CredSSH:
		// The key pair of the space is the attachment repository's too: add its
		// public key to that repository as well.
		if remote.Scheme != "ssh" {
			return nil, fmt.Errorf("%w: an ssh key goes with an ssh address (git@host:path); use a token for %s", ErrInvalid, remote.URL)
		}
		if spaceRow.PrivKeyEnc == "" {
			return nil, fmt.Errorf("%w: the space has no ssh key; connect the attachment repository with a token, or generate the space's key first", ErrInvalid)
		}
		row.CredType, row.PrivKeyEnc, row.PubKey = CredSSH, spaceRow.PrivKeyEnc, spaceRow.PubKey
	case CredConnection:
		row.CredType, row.Provider = CredConnection, req.Provider
		row.Via, row.InstallationID, row.RepoID, row.CredUserID = ghCred.Via, ghCred.InstallationID, ghCred.RepoID, ghCred.UserID
	default:
		return nil, fmt.Errorf("%w: credential.type must be ssh, token or connection", ErrInvalid)
	}
	return m.finishConnectAssets(ctx, h, old, row)
}

// finishConnectAssets opens the repository in a directory of its own, looks at
// it (the fetch also proves the credential), applies the rules about what it
// may hold, and only then stores the row.
func (m *Manager) finishConnectAssets(ctx context.Context, h *Handle, old *store.Remote, row store.Remote) (*AssetsView, error) {
	id := h.Info.ID
	cache := m.AssetCache()
	if cache == nil {
		return nil, errors.New("the attachment cache is not available")
	}
	// A repository tried out is a directory of its own until it is accepted.
	trial := m.assetsDir(id) + ".new"
	os.RemoveAll(trial)
	defer os.RemoveAll(trial)
	repo, err := assetrepo.Open(ctx, assetrepo.Options{
		Space: id, Dir: trial, URL: row.URL, Branch: row.Branch, Cred: m.credential(id, &row), Cache: cache,
		Runner: m.opt.RemoteRunner, AllowUnsafe: m.opt.RemotePolicy.AllowUnsafe, NetTimeout: m.opt.NetTimeout,
		FetchFresh: time.Nanosecond,
	})
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	if _, _, err := repo.Refresh(ctx, true); err != nil {
		return nil, err
	}
	if repo.Count() > 0 && (old == nil || old.URL != row.URL) {
		// A repository with content is taken only while the space has never used
		// one and has no attachments of its own: it takes the space's files.
		if old != nil && old.HasAssets() {
			return nil, assetsConflict("The attachment repository has files of its own, and the space has used another one.")
		}
		files, err := h.Space.AssetsAtHead(ctx)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			return nil, assetsConflict("That repository has files, and the space has attachments of its own in assets/.")
		}
	}
	// Accepted: the trial directory becomes the repository's. Nothing opens the
	// repository until the row is the new one and it is open again.
	r, arow, err := m.swapAssetRepo(ctx, id, trial, repo, row)
	if err != nil || r == nil {
		return nil, errors.Join(err, errors.New("the attachment repository could not be opened"))
	}
	h.Space.SetAssets(backend{m, id, r})
	m.goBg(func() { m.refreshAssets(id, h.Space) })
	return m.assetsView(ctx, h, arow), nil
}

func (m *Manager) swapAssetRepo(ctx context.Context, id, trial string, repo *assetrepo.Repo, row store.Remote) (*assetrepo.Repo, *store.Remote, error) {
	l := m.assetOpenLock(id)
	l.Lock()
	defer l.Unlock()
	m.forgetAssetRepo(id)
	repo.Close()
	os.RemoveAll(m.assetsDir(id))
	if err := os.MkdirAll(filepath.Dir(m.assetsDir(id)), 0o755); err != nil {
		return nil, nil, err
	}
	m.at("assets-swap")
	if err := os.Rename(trial, m.assetsDir(id)); err != nil {
		return nil, nil, err
	}
	row.State, row.LastSync, row.Updated = store.RemoteOK, m.opt.Now(), m.opt.Now()
	row.Role = store.RoleAssets
	if err := store.PutRemote(ctx, m.opt.Store.Q(), row); err != nil {
		return nil, nil, err
	}
	m.noteConnected(id, true)
	return m.assetRepoLocked(ctx, id)
}

// DisconnectAssets takes the attachment repository away from the space, while
// nothing has gone to it.
func (m *Manager) DisconnectAssets(ctx context.Context, h *Handle) error {
	if m.single != nil {
		return ErrNotFound
	}
	id := h.Info.ID
	m.remoteMu.Lock()
	defer m.remoteMu.Unlock()
	row, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return ErrNoAssets
	}
	if err != nil {
		return err
	}
	if m.moveActive(ctx, row) {
		return assetsConflict("The attachments are being moved.")
	}
	if row.HasAssets() {
		return assetsConflict("Attachments have gone to the attachment repository; moving them back into the space is not possible.")
	}
	// Nothing has gone to it, but what it holds may be in the table from before
	// (an empty one cannot be told from a used one by the rows alone).
	if r, _, _ := m.assetRepo(ctx, id); r != nil && r.Count() > 0 {
		return assetsConflict("The attachment repository has files; the space may refer to them.")
	}
	h.Space.SetAssets(nil)
	l := m.assetOpenLock(id)
	l.Lock()
	defer l.Unlock()
	m.forgetAssetRepo(id)
	if err := store.DeleteRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets); err != nil {
		return err
	}
	m.noteConnected(id, false)
	if r := os.RemoveAll(m.assetsDir(id)); r != nil {
		m.log.Warn("could not remove the files of an attachment repository", "space", id, "error", r)
	}
	if c := m.AssetCache(); c != nil {
		c.DropSpace(id)
	}
	return nil
}

func (m *Manager) moveRunning(id string) bool {
	m.AssetCache()
	m.as.mu.Lock()
	defer m.as.mu.Unlock()
	return m.as.moving[id]
}

// assetsGone cleans up after a space that is deleted.
func (m *Manager) assetsGone(id string) {
	if m.single != nil {
		return
	}
	l := m.assetOpenLock(id)
	l.Lock()
	m.forgetAssetRepo(id)
	os.RemoveAll(m.assetsDir(id))
	l.Unlock()
	if c := m.AssetCache(); c != nil {
		c.DropSpace(id)
	}
}

// hostOf names the host an attachment repository's address is on (for logs).
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}

func (m *Manager) closeAssetRepos() {
	m.as.mu.Lock()
	repos := m.as.repos
	m.as.repos = map[string]*assetrepo.Repo{}
	m.as.mu.Unlock()
	for _, r := range repos {
		r.Close()
	}
}

// verifyAssets is the part of the checks before an eviction that concerns the
// attachment repository (docs/api.md "Eviction"): nothing the space holds in
// its attachments may be lost with the clone.
//
//   - A move is not under way (until it ends, the space's own assets/ is the
//     only complete copy in one place).
//   - After a move, assets/ of the clone holds no file: what is in it is
//     ignored by Git, so no check of the clone would see it, and it would go.
//   - Nothing is being pushed to the attachment repository, no upload of an
//     earlier run is left, and its remote can be reached.
func (m *Manager) verifyAssets(ctx context.Context, id, root string) error {
	if m.single != nil {
		return nil
	}
	row, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return nil
	}
	if err != nil {
		return err
	}
	if m.moveActive(ctx, row) {
		return busyf("Its attachments are being moved into the attachment repository.")
	}
	if row.State != store.RemoteOK {
		return busyf("Its attachment repository is %s, so what it holds cannot be checked against it.", row.State)
	}
	if row.Moved() {
		if n := localAssetFiles(root, 1); n > 0 {
			return busyf("assets/ of the clone still has files that are not in the repository's history.")
		}
	}
	repo, _, err := m.assetRepo(ctx, id)
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}
	reason, err := repo.CheckClean(ctx)
	if err != nil {
		return m.evictUnreachable(err)
	}
	if reason != "" {
		return busyf("%s.", reason)
	}
	return nil
}

// localAssetFiles counts the files below assets/ of a clone, up to limit.
func localAssetFiles(root string, limit int) int {
	n := 0
	filepath.WalkDir(filepath.Join(root, space.AssetsDir), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
			if n >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return n
}

// ConnectAssetsSync connects the attachment repository to a repository on a
// provider, made on the spot when g.Create is set (the same plan, credential
// and checks as for a space: docs/api.md "Creating a space with a
// repository"), or one that is there. A repository that was made and could
// not be connected is named in the error, and remembered for the person as an
// empty leftover.
func (m *Manager) ConnectAssetsSync(ctx context.Context, h *Handle, userID, via string, g SyncCreate) (*AssetsView, error) {
	if m.single != nil {
		return nil, ErrNotFound
	}
	if _, err := m.needSpaceRemote(ctx, h.Info.ID); err != nil {
		return nil, err
	}
	gh, err := m.provider(g.Provider)
	if err != nil {
		return nil, err
	}
	if err := m.hostAllowed(gh); err != nil {
		return nil, err
	}
	if !gh.ValidRepoName(g.Name) {
		return nil, invalidAt(gh.Descriptor().ID, "sync.name is 1 to 100 characters of letters, digits, '.', '_' and '-', not '.' or '..' and not ending in .git")
	}
	sp, err := store.GetSpace(ctx, m.opt.Store.Q(), store.AllTenants(), h.Info.ID) // the space the caller has open, by id
	if err != nil {
		return nil, err
	}
	if via == connections.ViaUser {
		if err := m.sameTenant(ctx, g.Provider, sp.TenantID, userID); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	var cred *connections.Cred
	var repo *connections.Repo
	created := false
	if g.Create {
		plan, err := gh.PlanCreate(ctx, userID, g.Owner)
		if err != nil {
			return nil, err
		}
		if repo, err = gh.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: g.Name, Private: g.Private, Description: g.Description}); err != nil {
			return nil, err
		}
		created = true
		if cred, err = gh.ChooseCredential(ctx, plan, repo); err != nil {
			return nil, m.markRepo(ctx, gh, userID, h.Info.Name, err, repo, created)
		}
	} else if cred, repo, err = gh.ResolveExistingIn(ctx, sp.TenantID, userID, g.Owner, g.Name, via); err != nil {
		return nil, err
	}
	u := gh.RepoURL(repo.Owner, repo.Name)
	scheme := "https"
	if strings.HasPrefix(u, "http://") {
		scheme = "http"
	}
	remote := gitstore.Remote{URL: u, Scheme: scheme, Host: strings.ToLower(gh.Descriptor().Host)}
	spaceRow, err := store.GetRemote(ctx, m.opt.Store.Q(), h.Info.ID, store.RoleContent)
	if err != nil {
		return nil, m.markRepo(ctx, gh, userID, h.Info.Name, err, repo, created)
	}
	v, err := m.connectAssetsWith(ctx, h, ConnectRequest{URL: u, CredType: CredConnection, Provider: g.Provider, UserID: userID, Via: cred.Via}, remote, cred, spaceRow)
	if err != nil {
		return nil, m.markRepo(ctx, gh, userID, h.Info.Name, err, repo, created)
	}
	return v, nil
}

// markRepo names a repository that was made on the provider in the error of a
// later step, so that the same request with create false can use it, and
// remembers it for the person as an empty leftover.
func (m *Manager) markRepo(ctx context.Context, p connections.Provider, userID, spaceName string, err error, repo *connections.Repo, created bool) error {
	if !created || repo == nil {
		return err
	}
	if rerr := p.RecordLeftover(ctx, userID, repo, spaceName); rerr != nil {
		m.log.Warn("could not remember a repository left empty by a failed attachment repository", "repo", repo.FullName, "error", rerr)
	}
	var he *connections.Error
	if errors.As(err, &he) {
		c := *he
		c.Repo = repo.Ref()
		return &c
	}
	return &connections.Error{Provider: p.Descriptor().ID, Code: connections.CodePushFailed, Status: http.StatusBadGateway, Msg: err.Error(), Repo: repo.Ref(), Err: err}
}

func (m *Manager) assetsModeOf(has bool) string {
	if has {
		return "repository"
	}
	return "space"
}

// probeAssetsRepo opens a new attachment repository's meta clone in its place
// under the data directory and fetches it once: the credential works and the
// address is a repository. A failure leaves nothing behind.
func (m *Manager) probeAssetsRepo(ctx context.Context, id string, row *store.Remote) error {
	cache := m.AssetCache()
	if cache == nil {
		return errors.New("the attachment cache is not available")
	}
	os.RemoveAll(m.assetsDir(id))
	repo, err := assetrepo.Open(ctx, assetrepo.Options{
		Space: id, Dir: m.assetsDir(id), URL: row.URL, Branch: row.Branch, Cred: m.credential(id, row), Cache: cache,
		Runner: m.opt.RemoteRunner, AllowUnsafe: m.opt.RemotePolicy.AllowUnsafe, NetTimeout: m.opt.NetTimeout, FetchFresh: time.Nanosecond,
	})
	if err != nil {
		return err
	}
	defer repo.Close()
	_, _, err = m.refreshProbe(ctx, repo)
	return err
}

func (m *Manager) refreshProbe(ctx context.Context, repo *assetrepo.Repo) ([]string, []string, error) {
	// The first fetch of a repository the GitHub has just made is sometimes not
	// found for a few seconds: it is tried again as pushes are.
	var err error
	delays := append([]time.Duration{0}, m.opt.PushRetry...)
	deadline := time.Now().Add(m.opt.PushDeadline)
	for _, d := range delays {
		if d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		var a, r []string
		if a, r, err = repo.Refresh(ctx, true); err == nil {
			return a, r, nil
		}
		if !retryable(err) || time.Now().After(deadline) {
			break
		}
	}
	return nil, nil, err
}
