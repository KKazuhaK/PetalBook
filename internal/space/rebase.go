package space

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/KKazuhaK/PetalBook/internal/links"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

// --- rebasing what people wrote on top of the service's link rewrites ----------
//
// When a page moves, the service rewrites the links in the files that point to
// it. Someone may be typing in one of those files at that moment (an open
// editor, a draft on the server): their text still has the old paths and is
// based on the rev before the rewrite. That is not their conflict to solve. A
// save (or draft) whose base is behind the file only by such rewrites is
// accepted, with the same rewrites made to its text, so the person sees
// nothing and the old path is not written back.
//
// Each rewrite of a file is remembered as a step from the rev it had to the rev
// it got, with what moved; a base that a chain of steps leads from to the
// file's rev now is behind only by rewrites. The steps are kept in a small
// file in the state directory (a draft can wait across a restart), at most
// maxRebaseSteps of them, the oldest forgotten first.

// maxRebaseSteps bounds the steps kept; a draft older than the oldest of them
// is shown as stale, as it would be without this.
const maxRebaseSteps = 20000

// maxRebaseChain bounds how many rewrites one base can be behind.
const maxRebaseChain = 32

// stepMove is what moved, in a step: a page (ID, From, To) or a folder (Dir).
// A page whose id the links carry, moved outside PetalBook, has no From.
type stepMove struct {
	ID   string `json:"id,omitempty"`
	From string `json:"f,omitempty"`
	To   string `json:"t"`
	Dir  bool   `json:"dir,omitempty"`
}

// rebaseStep is one rewrite of the links of a page's file.
type rebaseStep struct {
	ID    string     `json:"id"`  // the page
	Old   string     `json:"old"` // its rev before
	New   string     `json:"new"` // and after
	OldAt string     `json:"fa"`  // where the file was (its links were relative to)
	NewAt string     `json:"ta"`  // where it is
	Moves []stepMove `json:"mv"`
}

type rebaseLog struct {
	mu    sync.Mutex
	path  string // "" keeps the steps in memory only
	steps map[string]rebaseStep
	order []string // keys, oldest first
}

func rebaseKey(id, rev string) string { return id + "\x00" + rev }

// openRebaseLog reads the steps an earlier run kept.
func openRebaseLog(path string) *rebaseLog {
	l := &rebaseLog{path: path, steps: map[string]rebaseStep{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	n := 0
	for sc.Scan() {
		var st rebaseStep
		if json.Unmarshal(sc.Bytes(), &st) == nil && st.ID != "" && st.Old != "" {
			l.put(st)
			n++
		}
	}
	if n > len(l.steps)+maxRebaseSteps/2 {
		l.compact()
	}
	return l
}

func (l *rebaseLog) put(st rebaseStep) {
	k := rebaseKey(st.ID, st.Old)
	if _, ok := l.steps[k]; !ok {
		l.order = append(l.order, k)
	}
	l.steps[k] = st
	for len(l.order) > maxRebaseSteps {
		delete(l.steps, l.order[0])
		l.order = l.order[1:]
	}
}

// compact writes the file again with the steps that are kept.
func (l *rebaseLog) compact() {
	if l.path == "" {
		return
	}
	var buf bytes.Buffer
	for _, k := range l.order {
		b, _ := json.Marshal(l.steps[k])
		buf.Write(b)
		buf.WriteByte('\n')
	}
	tmp := l.path + ".new"
	if os.WriteFile(tmp, buf.Bytes(), 0o644) == nil {
		os.Rename(tmp, l.path)
	}
}

// add remembers steps. Losing them (a full disk) only means a draft can show
// as stale: it is logged by nobody and costs nobody their text.
func (l *rebaseLog) add(steps ...rebaseStep) {
	if l == nil || len(steps) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var buf bytes.Buffer
	for _, st := range steps {
		if st.Old == st.New {
			continue
		}
		l.put(st)
		b, _ := json.Marshal(st)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if l.path == "" || buf.Len() == 0 {
		return
	}
	if f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err == nil {
		f.Write(buf.Bytes())
		f.Close()
	}
}

// chain is the steps that lead from the rev base of page id, as far as they go.
func (l *rebaseLog) chain(id, base string) []rebaseStep {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []rebaseStep
	for rev := base; len(out) < maxRebaseChain; {
		st, ok := l.steps[rebaseKey(id, rev)]
		if !ok {
			break
		}
		out = append(out, st)
		rev = st.New
	}
	return out
}

// rebaseLogPath is where a space keeps its steps.
func rebaseLogPath(stateDir string) string { return filepath.Join(stateDir, "link-rewrites.log") }

// rebaseFile makes the rewrites of steps to data, a page file whose links are
// written as at the base of the first step.
func (s *Space) rebaseFile(steps []rebaseStep, data []byte) []byte {
	for _, st := range steps {
		data = s.applyStep(st, data)
	}
	return data
}

// applyStep makes one rewrite again, on other content: every link that names a
// page that moved is pointed at where it went, and, if the file itself moved,
// every relative link is written from where the file is now.
func (s *Space) applyStep(st rebaseStep, data []byte) []byte {
	rl := s.newRelinker(nil)
	for _, m := range st.Moves {
		switch {
		case m.Dir:
			rl.dirs = append(rl.dirs, m)
		case m.ID != "":
			rl.byID[m.ID] = m.To
			if m.From != "" {
				rl.byOld[m.From] = movedPage{id: m.ID, from: m.From, to: m.To}
			}
		}
	}
	return links.Apply(data, rl.edits(data, st.OldAt, st.NewAt, false))
}

// rebaseTo says how page id's content based on base comes up to the file's
// rev now (cur): the steps, or ok false when it was changed otherwise.
func (s *Space) rebaseTo(id, base, cur string) (steps []rebaseStep, ok bool) {
	if base == "" || base == cur {
		return nil, false
	}
	steps = s.rb.chain(id, base)
	for i, st := range steps {
		if st.New == cur {
			return steps[:i+1], true
		}
	}
	return nil, false
}

// Rebase is for a draft of page id based on baseRev, when the file is no longer
// at that rev: if what changed it since was only the service rewriting links
// (a page it links to moved, or it did), ok is true, rev is the file's rev now
// and body is the draft's body with the same rewrites made. Nothing is
// written.
func (s *Space) Rebase(id, baseRev, body string) (rev, newBody string, ok bool) {
	_, data, _, err := s.load(id, true)
	if err != nil {
		return "", "", false
	}
	cur := page.Rev(data)
	steps, ok := s.rebaseTo(id, baseRev, cur)
	if !ok {
		return "", "", false
	}
	doc := page.Parse(data)
	doc.SetBody(body)
	return cur, page.Parse(s.rebaseFile(steps, doc.Bytes())).Body, true
}

// Rebasable reports whether content based on baseRev of page id is behind
// the file at rev cur only by the service's link rewrites.
func (s *Space) Rebasable(id, baseRev, cur string) bool {
	_, ok := s.rebaseTo(id, baseRev, cur)
	return ok
}

// under reports whether p is in the folder dir (or below it).
func under(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }
