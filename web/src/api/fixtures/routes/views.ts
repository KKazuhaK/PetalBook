// The saved views of a folder or a tag. A write names the rev it was made from; the views are kept as they came, with
// an id made for a new one and the members every view has filled in.

import type { SavedView, ViewList } from '../../types.ts'
import { hash, Refusal, type Route } from '../kit.ts'
import { spaceOf } from './files.ts'

const scopeOf = (folder: unknown, tag: unknown) => (typeof folder === 'string' ? `folder=${folder}` : `tag=${tag}`)
const listOf = (rows: SavedView[]): ViewList => ({ views: structuredClone(rows), rev: hash(JSON.stringify(rows)) })

export const viewRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/views',
    (c) => {
      const scope = scopeOf(c.query.get('folder'), c.query.get('tag'))
      return listOf(spaceOf(c).views[scope]?.views ?? [])
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/views',
    (c) => {
      const s = spaceOf(c)
      const scope = scopeOf(c.body.folder, c.body.tag)
      const had = s.views[scope]?.views ?? []
      const current = listOf(had)
      if (c.body.baseRev !== current.rev) throw new Refusal(409, 'conflict', 'the views were changed', { current })
      const next = (c.body.views as Partial<SavedView>[]).map((v): SavedView => {
        const id = v.id && !v.id.startsWith('@') ? v.id : hash(`view ${(c.data.counter += 1)}`).slice(0, 6)
        return { filter: [], hidden: [], sort: '', group: '', ...v, id } as SavedView
      })
      const out = listOf(next)
      if (next.length) s.views[scope] = { rev: out.rev, views: next }
      else delete s.views[scope]
      return out
    },
  ],
]
