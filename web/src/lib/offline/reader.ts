// Answers a GET of the app from the copy on this device, when the server cannot be reached: the list of pages (by
// folder, tag, title and the text that is here), one page, the tree and the tags. What needs more than the
// catalogue and the text kept (properties, tables, boards, history) is not answered: the request fails as it did.

import { ApiError } from '@/api/client'
import type { CatalogEntry, Page, PageList, PageSummary, TagsResponse, TreeResponse } from '@/api/types'
import { t } from '@/i18n'
import { existingStore } from './manager'
import type { OfflineStore } from './store'

const PAGE_SIZE = 50
const MAX_LIMIT = 200

const under = (folder: string, root: string) => root === '' || folder === root || folder.startsWith(root + '/')

// The text of a page for a listing: up to 400 characters, one line per non-blank line of the body.
export function excerptOf(markdown: string): string {
  const lines = markdown
    .replace(/```[^\n]*\n([\s\S]*?)```/g, '$1')
    .split('\n')
    .map((l) =>
      l
        .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
        .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
        .replace(/^\s{0,3}(#{1,6}|>|[-*+]|\d+\.|- \[[ x]\])\s+/, '')
        .replace(/[*_`~|]/g, '')
        .replace(/\s+/g, ' ')
        .trim(),
    )
    .filter(Boolean)
  return Array.from(lines.join('\n')).slice(0, 400).join('')
}

const compareCodePoints = (a: string, b: string) => {
  const x = Array.from(a)
  const y = Array.from(b)
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i].codePointAt(0)! - y[i].codePointAt(0)!
    if (d !== 0) return d
  }
  return x.length - y.length
}

const day = (s: string) => /^\d{4}-\d{2}-\d{2}$/.test(s)

function summaryOf(e: CatalogEntry, page?: Page): PageSummary {
  const m = page?.meta
  const subtitle = typeof m?.subtitle === 'string' && m.subtitle !== '' ? m.subtitle : undefined
  const source = Array.isArray(m?.source)
    ? m.source.filter((s): s is string => typeof s === 'string' && s !== '')
    : typeof m?.source === 'string' && m.source !== ''
      ? [m.source]
      : []
  return {
    id: e.id,
    path: e.path,
    folder: e.folder,
    ...(e.title ? { title: e.title } : {}),
    ...(subtitle ? { subtitle } : {}),
    source,
    label: e.label,
    untitled: e.untitled,
    ...(e.tags.length ? { tags: e.tags } : {}),
    type: e.type,
    effectiveType: page?.effectiveType ?? e.type,
    ...(e.created ? { created: e.created } : {}),
    updated: e.updated,
    excerpt: page ? excerptOf(page.body) : '',
  }
}

// What the listing's query can ask that the copy cannot answer.
const UNSUPPORTED = ['filter', 'props', 'body']

async function listPages(store: OfflineStore, q: URLSearchParams): Promise<PageList | undefined> {
  if (UNSUPPORTED.some((k) => q.has(k))) return undefined
  const folder = q.get('folder')
  const recursive = q.get('recursive') === 'true'
  const tags = q.getAll('tag')
  const text = (q.get('q') ?? '').trim().toLowerCase()
  const titleOnly = q.get('in') === 'title'
  const updatedFrom = q.get('updatedFrom')
  const updatedTo = q.get('updatedTo')
  const createdFrom = q.get('createdFrom')
  const createdTo = q.get('createdTo')
  let items = await store.catalog()
  if (folder !== null) items = items.filter((e) => (recursive ? under(e.folder, folder) : e.folder === folder))
  if (tags.length) items = items.filter((e) => tags.every((t) => e.tags.includes(t)))
  if (updatedFrom && day(updatedFrom)) items = items.filter((e) => e.updated.slice(0, 10) >= updatedFrom)
  if (updatedTo && day(updatedTo)) items = items.filter((e) => e.updated.slice(0, 10) <= updatedTo)
  if (createdFrom && day(createdFrom)) items = items.filter((e) => (e.created ?? '') >= createdFrom)
  if (createdTo && day(createdTo))
    items = items.filter((e) => (e.created ?? '') !== '' && (e.created ?? '') <= createdTo)

  const bodies = new Map<string, Page>()
  if (text !== '') {
    const terms = text.split(/\s+/).filter(Boolean)
    const wanted = new Set(items.map((e) => e.id))
    const kept = new Map<string, string>()
    if (!titleOnly) {
      await store.eachPage((r) => {
        if (!wanted.has(r.id)) return
        const m = r.page.meta
        kept.set(
          r.id,
          [m.title, m.subtitle, ...(Array.isArray(m.source) ? m.source : [m.source]), r.page.body]
            .filter((x) => typeof x === 'string')
            .join('\n')
            .toLowerCase(),
        )
        bodies.set(r.id, r.page)
      })
    }
    items = items.filter((e) => {
      const hay = `${(e.title ?? e.label).toLowerCase()}\n${kept.get(e.id) ?? ''}`
      return terms.every((t) => hay.includes(t))
    })
  }

  const sort = q.get('sort') ?? (text !== '' ? 'relevance' : '-updated')
  const desc = sort.startsWith('-')
  const key = desc ? sort.slice(1) : sort
  const byId = (a: CatalogEntry, b: CatalogEntry) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
  if (key === 'title') {
    items = [...items].sort(
      (a, b) =>
        Number(a.untitled) - Number(b.untitled) ||
        (desc ? -1 : 1) * compareCodePoints((a.title ?? '').toLowerCase(), (b.title ?? '').toLowerCase()) ||
        byId(a, b),
    )
  } else if (key === 'created') {
    items = [...items].sort(
      (a, b) =>
        (desc ? -1 : 1) *
          ((a.created ?? '') < (b.created ?? '') ? -1 : (a.created ?? '') > (b.created ?? '') ? 1 : 0) || byId(a, b),
    )
  } else if (key === 'updated' || key === 'relevance') {
    // Newest first unless asked the other way; when searching, the titles that match come first.
    const rank = (e: CatalogEntry) => (text !== '' && (e.title ?? e.label).toLowerCase().includes(text) ? 0 : 1)
    const dir = key === 'updated' && !desc ? 1 : -1
    items = [...items].sort(
      (a, b) =>
        (key === 'relevance' ? rank(a) - rank(b) : 0) ||
        dir * (a.updated < b.updated ? -1 : a.updated > b.updated ? 1 : 0) ||
        byId(a, b),
    )
  } else return undefined // a sort by a property

  const limit = Math.min(Math.max(Number(q.get('limit')) || PAGE_SIZE, 1), MAX_LIMIT)
  const from = /^\d+$/.test(q.get('cursor') ?? '') ? Number(q.get('cursor')) : 0
  const slice = items.slice(from, from + limit)
  // The text of the page, for the excerpt, when it is here (the search has read some already).
  const out: PageSummary[] = []
  for (const e of slice) out.push(summaryOf(e, bodies.get(e.id) ?? (await store.page(e.id))?.page))
  return { items: out, next: from + slice.length < items.length ? String(from + slice.length) : '' }
}

async function treeOf(store: OfflineStore): Promise<TreeResponse> {
  const stored = await store.tree()
  if (stored) return stored
  const counts = new Map<string, number>([['', 0]])
  const newest = new Map<string, string>()
  for (const e of await store.catalog()) {
    counts.set(e.folder, (counts.get(e.folder) ?? 0) + 1)
    for (let f = e.folder; ; f = f.includes('/') ? f.slice(0, f.lastIndexOf('/')) : '') {
      if (!counts.has(f)) counts.set(f, 0)
      if (e.updated > (newest.get(f) ?? '')) newest.set(f, e.updated)
      if (f === '') break
    }
  }
  return {
    folders: [...counts]
      .sort((a, b) => compareCodePoints(a[0], b[0]))
      .map(([path, pages]) => ({ path, pages, ...(newest.has(path) ? { updated: newest.get(path) } : {}) })),
  }
}

async function tagsOf(store: OfflineStore): Promise<TagsResponse> {
  const stored = await store.tags()
  if (stored) return stored
  const counts = new Map<string, number>()
  for (const e of await store.catalog()) for (const t of new Set(e.tags)) counts.set(t, (counts.get(t) ?? 0) + 1)
  return {
    tags: [...counts]
      .map(([tag, pages]) => ({ tag, pages }))
      .sort((a, b) => b.pages - a.pages || compareCodePoints(a.tag, b.tag)),
  }
}

// `path` is what the client asked for, without the API prefix. undefined: not something the copy answers.
export async function readOffline(user: string | null, path: string): Promise<unknown> {
  if (!user) return undefined
  const m = /^\/spaces\/([^/?]+)(\/[^?]*)?(?:\?(.*))?$/.exec(path)
  if (!m) return undefined
  let space: string
  try {
    space = decodeURIComponent(m[1])
  } catch {
    return undefined
  }
  const rest = m[2] ?? ''
  const q = new URLSearchParams(m[3] ?? '')
  if (rest !== '/pages' && rest !== '/tree' && rest !== '/tags' && !/^\/pages\/[^/]+$/.test(rest)) return undefined
  const store = await existingStore(user, space)
  if (!store) return undefined
  if (!(await store.state()).complete) return undefined
  if (rest === '/pages') return listPages(store, q)
  if (rest === '/tree') return treeOf(store)
  if (rest === '/tags') return tagsOf(store)
  const id = decodeURIComponent(rest.slice('/pages/'.length))
  const rec = await store.page(id)
  if (rec) return rec.page
  if (await store.catalogEntry(id)) throw new ApiError(0, 'not_offline', t.offline.notDownloaded)
  throw new ApiError(404, 'not_found', t.offline.notHere)
}
