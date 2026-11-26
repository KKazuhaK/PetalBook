// A page's draft: the one protocol the fixture keeps. The editor's save controller is correct only against how a
// server numbers drafts (a version on each store, `prev` naming the one replaced, a 409 that carries the draft and
// the page as they are), so the model-based editor test runs against this. The Go tests of the drafts hold the same
// rules for the real server.

import type { DraftReceipt } from '../../types.ts'
import type { DraftRec, FixtureData, SpaceData, StoredPage } from '../data/model.ts'
import { Refusal, rfc3339, type Route } from '../kit.ts'
import { draftView, pageOf, pageView, revOf, sameParsed, spaceOf, writePage } from './files.ts'

// The 409 of a request that names a draft the server does not have: the draft as it is (or null) and the page.
const conflict = (s: SpaceData, p: StoredPage) =>
  new Refusal(409, 'conflict', 'the draft on the server is not that one', {
    draft: draftView(s, p),
    current: pageView(s, p),
  })

const newVersion = (data: FixtureData) => `v${(++data.counter).toString(36)}`

function commitDraft(now: number, s: SpaceData, p: StoredPage, d: DraftRec) {
  writePage(now, s, p, d.meta, d.body)
  delete s.drafts[p.id]
}

export const draftRoutes: Route[] = [
  [
    'PUT',
    '/api/spaces/:space/pages/:id/draft',
    (c): DraftReceipt => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      const before = s.drafts[p.id]
      if ((before?.version ?? '') !== (c.body.prev ?? '')) throw conflict(s, p)
      const t = c.now()
      const d: DraftRec = {
        version: newVersion(c.data),
        baseRev: c.body.baseRev,
        meta: structuredClone(c.body.meta),
        body: c.body.body,
        updated: t,
        first: before && before.baseRev === c.body.baseRev ? before.first : t,
      }
      s.drafts[p.id] = d
      return { version: d.version, baseRev: d.baseRev, updated: rfc3339(t), stale: d.baseRev !== revOf(p) }
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/pages/:id/draft',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      const version = c.query.get('version')
      const rev = c.query.get('rev')
      if (version && rev && revOf(p) !== rev) throw conflict(s, p)
      if (version && s.drafts[p.id]?.version !== version) throw conflict(s, p)
      delete s.drafts[p.id]
    },
  ],
  [
    'POST',
    '/api/spaces/:space/pages/:id/commit',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      const d = s.drafts[p.id]
      if (!d || d.version !== c.body.version) throw conflict(s, p)
      if (d.baseRev !== revOf(p)) {
        // Stale: if it says what the file says there is nothing to lose, so it goes and the page is returned.
        if (!sameParsed(p, d)) throw conflict(s, p)
        delete s.drafts[p.id]
        return pageView(s, p)
      }
      commitDraft(c.now(), s, p, d)
      return { ...pageView(s, p), textRev: revOf(p) }
    },
  ],
]

// The server's own commits: a draft quiet for `idleMs`, or first stored `maxMs` ago, is written to its page. A stale
// draft is left alone.
export function sweepDrafts(data: FixtureData, t: number, idleMs: number, maxMs: number) {
  for (const s of data.spaces)
    for (const [id, d] of Object.entries(s.drafts)) {
      if (t - d.updated < idleMs && t - d.first < maxMs) continue
      const p = s.pages.find((x) => x.id === id)
      if (!p) continue
      if (d.baseRev !== revOf(p)) {
        if (sameParsed(p, d)) delete s.drafts[id]
        continue
      }
      commitDraft(t, s, p, d)
    }
}
