// What the page editor and its panels read beyond the page itself: the links of a page and the pages that link to it,
// a field of a type given another key, and the presets of the sources.

import type { BacklinkList, FieldRename, PageLinkList } from '../../types.ts'
import { Refusal, type Ctx, type Route } from '../kit.ts'
import { excerptAround, linksOf, type FoundLink } from '../links.ts'
import type { SpaceData, StoredPage } from '../data/model.ts'
import { pageOf, pathOf, spaceOf, summary, typeOf, typeView, writePage } from './files.ts'

// The page a link names: by its id when it has one, else by the path of the file.
const targetOf = (s: SpaceData, l: FoundLink): StoredPage | undefined =>
  (l.id && s.pages.find((p) => p.id === l.id)) || s.pages.find((p) => pathOf(p) === l.path)

const PAGE = 20

export const editorRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/pages/:id/links',
    (c): PageLinkList => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      return {
        items: linksOf(p.folder, p.body).map((l) => {
          const t = targetOf(s, l)
          return {
            text: l.text,
            path: l.path,
            ...(l.id ? { id: l.id } : {}),
            ...(l.anchor ? { anchor: l.anchor } : {}),
            broken: !t,
            ...(t
              ? {
                  target: {
                    id: t.id,
                    path: pathOf(t),
                    title: summary(s, t).label,
                    untitled: summary(s, t).untitled,
                  },
                }
              : {}),
          }
        }),
      }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/pages/:id/backlinks',
    (c): BacklinkList => {
      const s = spaceOf(c)
      const me = pageOf(s, c.params.id)
      const found = s.pages
        .filter((p) => p.id !== me.id)
        .map((p) => ({ p, link: linksOf(p.folder, p.body).find((l) => targetOf(s, l)?.id === me.id) }))
        .filter((x): x is { p: StoredPage; link: FoundLink } => x.link !== undefined)
        .sort((a, b) => b.p.updated - a.p.updated)
      const from = Number(c.query.get('cursor') ? atob(c.query.get('cursor')!) : 0) || 0
      const slice = found.slice(from, from + PAGE)
      const end = from + slice.length
      return {
        items: slice.map(({ p, link }) => {
          const sum = summary(s, p)
          return {
            id: p.id,
            path: pathOf(p),
            title: sum.label,
            untitled: sum.untitled,
            excerpt: excerptAround(p.body, link),
          }
        }),
        next: end < found.length ? btoa(String(end)) : '',
        total: found.length,
      }
    },
  ],
  [
    'POST',
    '/api/spaces/:space/types/:name/rename-field',
    (c: Ctx): FieldRename => {
      const s = spaceOf(c)
      const { from, to } = c.body as { from: string; to: string }
      const old = s.types.find((t) => t.name === c.params.name)
      if (!old) throw new Refusal(404, 'not_found', 'the type is not there')
      const type = typeView(
        old.name,
        old.title,
        old.fields.map((f) => (f.key === from ? { ...f, key: to } : f)),
      )
      s.types[s.types.indexOf(old)] = type
      let pages = 0
      const skipped: FieldRename['skipped'] = []
      for (const p of s.pages) {
        if (!(from in p.meta) || typeOf(s, p) !== old.name) continue
        if (to in p.meta) {
          skipped.push({ id: p.id, path: pathOf(p), reason: 'has_key' })
          continue
        }
        const meta = Object.fromEntries(Object.entries(p.meta).map(([k, v]) => [k === from ? to : k, v]))
        writePage(c.now(), s, p, meta, p.body)
        pages++
      }
      return { type, pages, skipped }
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/sources/presets',
    (c) => {
      const s = spaceOf(c)
      s.sourcePresets = c.body.presets ?? null
      return { presets: s.sourcePresets }
    },
  ],
]
