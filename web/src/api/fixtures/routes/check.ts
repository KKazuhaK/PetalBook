// The check page of a space: the problems the server found in it, kept as data (`checks` of the space) and listed by
// kind in pages. What counts as a problem is the server's to work out; a scene puts the ones it needs. A fix names the
// rev the problem was listed with, and a stale one is the same 409 as a stale page save.

import type { ChecksResponse, Page, Problem, ProblemKind } from '../../types.ts'
import { paged, Refusal, remove, type Route } from '../kit.ts'
import { pageView, spaceOf } from './files.ts'

const KINDS: ProblemKind[] = [
  'no_id',
  'duplicate_id',
  'bad_header',
  'type_mismatch',
  'not_in_type',
  'broken_link',
  'missing_asset',
  'duplicate_title',
]

export const checkRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/checks',
    (c): ChecksResponse => {
      const all = spaceOf(c).checks
      const counts = Object.fromEntries(KINDS.map((k) => [k, all.filter((p) => p.kind === k).length])) as Record<
        ProblemKind,
        number
      >
      const kind = c.query.get('kind')
      const shown = all
        .filter((p) => !kind || p.kind === kind)
        .sort((a, b) => KINDS.indexOf(a.kind) - KINDS.indexOf(b.kind))
      const page = paged(shown, c.query, 50)
      return { counts, items: structuredClone(page.items), next: page.next }
    },
  ],
  [
    'POST',
    '/api/spaces/:space/checks/fix',
    (c): Page => {
      const s = spaceOf(c)
      const problem = s.checks.find((p: Problem) => p.kind === c.body.kind && p.path === c.body.path)
      if (!problem) throw new Refusal(404, 'not_found', 'that problem is not there any more')
      if (problem.rev !== c.body.baseRev) throw new Refusal(409, 'conflict', 'the file was changed')
      remove(s.checks, problem)
      // The page, as it is now; a file that is no page (a header that does not read) is told as an empty one.
      const page = s.pages.find((p) => p.id === problem.id)
      if (page) return pageView(s, page)
      return {
        id: '',
        path: problem.path,
        folder: '',
        rev: 'fixed',
        updated: '',
        meta: {},
        header: '',
        body: '',
        effectiveType: '',
      } as Page
    },
  ],
]
