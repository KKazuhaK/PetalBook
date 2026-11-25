package spaces

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

// The problems of the spaces a person writes in, for the notification centre.
// They are not rows: what is wrong in a space is in its index, so the answer is
// read from there when it is asked for, and nothing is copied to the members.
//
// Opening a space only to count its problems would load it for everyone who
// has the app open, so nothing is opened here: a space that is open is counted
// from its live index (two small grouped queries), and one that is not is
// answered with what it said when it was last open or closed (problemMemo, in
// memory; a restart forgets it until the space is opened again).

// problemFresh is how long a count of an open space is reused: the badge is
// asked for every minute by everyone who has the app open.
const problemFresh = 20 * time.Second

// problemMax is the most spaces one answer looks at.
const problemMax = 50

type problemEntry struct {
	counts map[string]int
	at     time.Time
}

type problemMemo struct {
	mu sync.Mutex
	m  map[string]problemEntry
}

func (p *problemMemo) get(id string) (problemEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.m[id]
	return e, ok
}

func (p *problemMemo) put(id string, counts map[string]int, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]problemEntry{}
	}
	p.m[id] = problemEntry{counts, at}
}

// SpaceProblems is what is wrong in one space.
type SpaceProblems struct {
	ID     string
	Name   string
	Counts map[string]int // by kind, only the kinds with problems
	Total  int
	// Sig changes when the problems do (it is what a person's "read" is kept against).
	Sig string
}

// noteProblems remembers what an open space says, before it is closed.
func (m *Manager) noteProblems(id string, sp *space.Space) {
	if counts, err := sp.CheckCounts(); err == nil {
		m.problems.put(id, counts, m.opt.Now())
	}
}

// peekOpen holds an open space for a moment without making it open and without
// counting as a use (so it can still be closed for being idle); ok is false for
// a space that is not open.
func (m *Manager) peekOpen(id string) (sp *space.Space, release func(), ok bool) {
	m.mu.Lock()
	e := m.open[id]
	if e == nil || e.closing != nil {
		m.mu.Unlock()
		return nil, nil, false
	}
	select {
	case <-e.done:
	default:
		m.mu.Unlock()
		return nil, nil, false
	}
	if e.err != nil || e.sp == nil {
		m.mu.Unlock()
		return nil, nil, false
	}
	e.refs++
	m.mu.Unlock()
	var once sync.Once
	return e.sp, func() {
		once.Do(func() {
			m.mu.Lock()
			e.refs--
			m.mu.Unlock()
		})
	}, true
}

// SpaceProblems lists the spaces the person can write in that have problems, from
// memory and the indexes of the spaces that are open (see above). Spaces
// switched off are left out.
func (m *Manager) SpaceProblems(ctx context.Context, userID string) ([]SpaceProblems, error) {
	l, err := m.list(ctx, userID)
	if err != nil {
		return nil, err
	}
	m.loadDisabled(ctx) // on failure nothing is shown as disabled; the space cannot be opened anyway
	var out []SpaceProblems
	n := 0
	for _, in := range l {
		if !in.Perms.Has(perm.Write) || in.ViaAdmin {
			continue
		}
		m.mu.Lock()
		off := m.disabled[in.ID]
		m.mu.Unlock()
		if off {
			continue
		}
		if n++; n > problemMax {
			break
		}
		counts := m.problemCounts(in.ID)
		if sp := problemsOf(in.ID, in.Name, counts); sp != nil {
			out = append(out, *sp)
		}
	}
	return out, nil
}

// problemCounts is what the space says now (open), or said last.
func (m *Manager) problemCounts(id string) map[string]int {
	now := m.opt.Now()
	if m.single != nil {
		counts, err := m.single.CheckCounts()
		if err != nil {
			return nil
		}
		return counts
	}
	e, have := m.problems.get(id)
	if have && now.Sub(e.at) < problemFresh {
		return e.counts
	}
	sp, release, ok := m.peekOpen(id)
	if !ok {
		return e.counts // nil when never seen
	}
	defer release()
	counts, err := sp.CheckCounts()
	if err != nil {
		return e.counts
	}
	m.problems.put(id, counts, now)
	return counts
}

func problemsOf(id, name string, counts map[string]int) *SpaceProblems {
	total := 0
	kinds := make([]string, 0, len(counts))
	for k, n := range counts {
		if n > 0 {
			total += n
			kinds = append(kinds, k)
		}
	}
	if total == 0 {
		return nil
	}
	sort.Strings(kinds)
	h := sha1.New()
	only := map[string]int{}
	for _, k := range kinds {
		only[k] = counts[k]
		h.Write([]byte(k + "=" + strconv.Itoa(counts[k]) + ";"))
	}
	return &SpaceProblems{ID: id, Name: name, Counts: only, Total: total, Sig: strings.TrimSpace(hex.EncodeToString(h.Sum(nil))[:12])}
}
