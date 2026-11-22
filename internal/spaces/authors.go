package spaces

import (
	"context"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

const (
	// authorTTL bounds how long a user's name and email are remembered for
	// commits made on their behalf when they are not at hand (the drafts
	// sweeper). A profile change drops the entry at once (UserChanged); this
	// only covers a change made some other way.
	authorTTL = time.Minute
	// maxAuthors bounds the memory of that cache.
	maxAuthors = 10_000
)

type cachedAuthor struct {
	a  space.Author
	at time.Time
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
	id := IdentityOf(u)
	return space.Author{Name: id.Name, Email: id.Email}
}

// AuthorByID is AuthorOf for a user known only by id, as the drafts sweeper
// has them. It reads the user once and remembers the answer for a minute, so
// a sweep that applies many drafts does not query the database for each.
// store.ErrNotFound when the user is gone.
func (m *Manager) AuthorByID(ctx context.Context, userID string) (space.Author, error) {
	if m.single != nil {
		return space.Author{}, nil
	}
	now := time.Now()
	m.authors.mu.Lock()
	c, ok := m.authors.m[userID]
	m.authors.mu.Unlock()
	if ok && now.Sub(c.at) < authorTTL {
		return c.a, nil
	}
	u, err := store.GetUser(ctx, m.opt.Store.Q(), userID)
	if err != nil {
		return space.Author{}, err
	}
	a := m.AuthorOf(*u)
	m.authors.mu.Lock()
	if m.authors.m == nil || len(m.authors.m) >= maxAuthors {
		m.authors.m = map[string]cachedAuthor{}
	}
	m.authors.m[userID] = cachedAuthor{a, now}
	m.authors.mu.Unlock()
	return a, nil
}

func (m *Manager) forgetAuthor(userID string) {
	m.authors.mu.Lock()
	delete(m.authors.m, userID)
	m.authors.mu.Unlock()
}
