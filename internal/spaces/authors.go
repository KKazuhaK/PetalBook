package spaces

import (
	"context"
	"errors"
	"sync"
	"time"

	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
)

const (
	// authorTTL bounds how long a user's name and email are remembered for
	// commits made on their behalf when they are not at hand (the drafts
	// sweeper), and whether they are an administrator or disabled. Any change to
	// the user drops the entry at once (UserChanged); this only covers a change
	// made some other way.
	authorTTL = time.Minute
	// maxAuthors bounds the memory of that cache.
	maxAuthors = 10_000
)

type cachedAuthor struct {
	a        space.Author
	admin    bool // an instance administrator
	disabled bool
	at       time.Time
}

type authorCache struct {
	mu sync.Mutex
	m  map[string]cachedAuthor
}

// AuthorOf is who a change by u is credited to in Git. In single-user mode
// that is the zero Author: the space's own author, as it always was.
func (m *Manager) AuthorOf(u store.User) space.Author {
	if m.single != nil {
		return space.Author{}
	}
	m.noteWrite(u.ID)
	id := IdentityOf(u)
	return space.Author{Name: id.Name, Email: id.Email}
}

// noteWrite records that the person is changing something in a space (the first
// time sets users.first_write; afterwards it is a lookup in memory). Every change
// is credited through AuthorOf, so this is the one place all of them pass.
func (m *Manager) noteWrite(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.opt.Store.NoteWrite(ctx, userID, m.opt.Now()); err != nil {
		m.log.Debug("could not record a person's first change", "error", err)
	}
}

// AuthorByID is AuthorOf for a user known only by id, as the drafts sweeper
// has them. It reads the user once and remembers the answer for a minute, so
// a sweep that applies many drafts does not query the database for each.
// store.ErrNotFound when the user is gone.
func (m *Manager) AuthorByID(ctx context.Context, userID string) (space.Author, error) {
	if m.single != nil {
		return space.Author{}, nil
	}
	c, err := m.userEntry(ctx, userID)
	return c.a, err
}

func (m *Manager) userEntry(ctx context.Context, userID string) (cachedAuthor, error) {
	now := time.Now()
	m.authors.mu.Lock()
	c, ok := m.authors.m[userID]
	m.authors.mu.Unlock()
	if ok && now.Sub(c.at) < authorTTL {
		return c, nil
	}
	u, err := store.GetUser(ctx, m.opt.Store.Q(), store.AllTenants(), userID) // by id, for a user already known to the caller
	if err != nil {
		return cachedAuthor{}, err
	}
	a := m.AuthorOf(*u)
	c = cachedAuthor{a: a, admin: u.IsAdmin(), disabled: u.Disabled, at: now}
	m.authors.mu.Lock()
	if m.authors.m == nil || len(m.authors.m) >= maxAuthors {
		m.authors.m = map[string]cachedAuthor{}
	}
	m.authors.m[userID] = c
	m.authors.mu.Unlock()
	return c, nil
}

// MayWrite reports whether the user may still write in the space: an enabled
// member whose role holds the write permission, or an enabled instance
// administrator. The drafts sweeper asks it before it commits a draft for a
// person who may have been removed or made a viewer since they typed it. It
// costs the cached membership list and the author cache (one user read a
// minute at most), no query per draft. A user who is gone may not.
func (m *Manager) MayWrite(ctx context.Context, userID, spaceID string) (bool, error) {
	if m.single != nil {
		return true, nil // the one local user may do everything; Acquire knows which space exists
	}
	c, err := m.userEntry(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil || c.disabled {
		return false, err
	}
	if c.admin {
		return true, nil
	}
	ps, ok, err := m.permsOf(ctx, userID, spaceID)
	if err != nil || !ok {
		return false, err
	}
	return ps.Has(perm.Write), nil
}

func (m *Manager) forgetAuthor(userID string) {
	m.authors.mu.Lock()
	delete(m.authors.m, userID)
	m.authors.mu.Unlock()
}
