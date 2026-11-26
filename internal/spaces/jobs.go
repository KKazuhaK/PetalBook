package spaces

import (
	"petalbook.app/server/internal/jobs"
	"petalbook.app/server/internal/store"
)

// The manager's work on the jobs runner (internal/jobs): exports and the
// actions of the recycle bin are jobs of their own kinds. The server gives the
// manager its runner (UseJobs) and starts it once every package has registered
// its kinds; a manager that was given none (tests, a tool) makes one of its own
// the first time it enqueues, and starts it.

// UseJobs gives the manager the runner whose jobs it adds, and registers its
// kinds on it. Call it before the runner is started; the caller closes the
// runner.
func (m *Manager) UseJobs(r *jobs.Runner) {
	m.jobsMu.Lock()
	m.jobs, m.ownsJobs = r, false
	m.jobsMu.Unlock()
	m.registerKinds(r)
}

func (m *Manager) registerKinds(r *jobs.Runner) {
	r.Register(store.JobExport, exportKind{m})
	for _, kind := range []string{store.JobRecycleMove, store.JobRecycleRestore, store.JobRecyclePurge} {
		r.Register(kind, recycleKind{m, kind})
	}
}

// runner is the manager's runner, made and started on first use when none was
// given.
func (m *Manager) runner() *jobs.Runner {
	m.jobsMu.Lock()
	r := m.jobs
	if r == nil {
		r = jobs.New(jobs.Options{Store: m.opt.Store, Logger: m.log, Now: m.opt.Now})
		m.jobs, m.ownsJobs = r, true
		m.registerKinds(r)
	}
	m.jobsMu.Unlock()
	r.Start() // once: a second call does nothing
	return r
}

// closeJobs stops the runner the manager made itself: the jobs it runs work on
// the spaces, which are closed after it.
func (m *Manager) closeJobs() {
	m.jobsMu.Lock()
	r, own := m.jobs, m.ownsJobs
	m.jobsMu.Unlock()
	if r != nil && own {
		r.Close()
	}
}
