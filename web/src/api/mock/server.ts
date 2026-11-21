// An in-memory implementation of docs/api.md, for `npm run dev:mock` and for
// tests. It is deliberately strict about the parts the web app depends on
// (revs, 409 conflicts, header order, untitled pages, cursors) and simple
// about everything else. It has no DOM or Node dependencies.

import type { Page, PageMeta, PageSummary } from '../types.ts'
import { folderRuleMessage } from '../../lib/folderRules.ts'
import { gitBlobId } from './sha1.ts'
import { SEED_EMPTY_FOLDERS, SPACE_NAME, seedPages } from './seed.ts'

export interface MockResponse {
  status: number
  body?: unknown
}

export interface MockOptions {
  now?: () => number
  // Start with no pages at all.
  empty?: boolean
  // Fixed ids (seed pages use deterministic ids so tests can address them).
  random?: () => number
}

interface Stored {
  id: string
  folder: string
  fileName: string
  meta: PageMeta
  body: string
  rev: string
  updated: number
}

const ALPHABET = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
const ID_RE = /^[0-9A-HJKMNP-TV-Z]{10}$/

class HttpError extends Error {
  status: number
  code: string
  extra?: Record<string, unknown>
  constructor(status: number, code: string, message: string, extra?: Record<string, unknown>) {
    super(message)
    this.status = status
    this.code = code
    this.extra = extra
  }
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

export const validFolder = (path: string) => folderRuleMessage(path) === null

const ancestors = (path: string): string[] => {
  const out: string[] = []
  const parts = path.split('/').filter(Boolean)
  for (let i = 1; i <= parts.length; i++) out.push(parts.slice(0, i).join('/'))
  return out
}

// The file as it would sit on disk. Only used to compute the rev, so the YAML
// flavour does not matter, but it must change whenever header or body does.
function serializeFile(meta: PageMeta, body: string): string {
  const lines = Object.entries(meta).map(([k, v]) => `${k}: ${JSON.stringify(v)}`)
  return `---\n${lines.join('\n')}\n---\n${body}`
}

function plain(markdown: string): string {
  return markdown
    .replace(/```[^\n]*\n([\s\S]*?)```/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}(#{1,6}|>|[-*+]|\d+\.|- \[[ x]\])\s+/gm, '')
    .replace(/^\s*\|?\s*:?-{3,}.*$/gm, '')
    .replace(/[*_`~|]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

function firstLine(body: string): string {
  for (const raw of body.split('\n')) {
    const line = plain(raw)
    if (line) return line
  }
  return ''
}

// Titles compare by code point (not by locale), case-insensitively.
function compareCodePoints(a: string, b: string): number {
  const x = Array.from(a)
  const y = Array.from(b)
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i].codePointAt(0)! - y[i].codePointAt(0)!
    if (d !== 0) return d
  }
  return x.length - y.length
}

const stringField = (meta: PageMeta, key: string): string | undefined => {
  const v = meta[key]
  return typeof v === 'string' && v !== '' ? v : undefined
}
const tagsOf = (meta: PageMeta): string[] => (Array.isArray(meta.tags) ? meta.tags.filter((t: unknown): t is string => typeof t === 'string') : [])

const rfc3339 = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')

export function createMockServer(options: MockOptions = {}) {
  const now = options.now ?? Date.now
  const random = options.random ?? Math.random
  const pages = new Map<string, Stored>()
  const folders = new Set<string>([''])
  let counter = 0

  const newId = () => {
    for (;;) {
      let id = '01'
      for (let i = 0; i < 8; i++) id += ALPHABET[Math.floor(random() * 32)]
      if (!pages.has(id)) return id
    }
  }
  const addFolder = (path: string) => ancestors(path).forEach((p) => folders.add(p))

  const fileNameFor = (folder: string, meta: PageMeta, id: string, self?: string): string => {
    const title = stringField(meta, 'title')
    const created = stringField(meta, 'created') ?? new Date(now()).toISOString().slice(0, 10)
    const base = title ? title.replace(/[\\/:*?"<>|\p{Cc}]/gu, ' ').replace(/\s+/g, ' ').trim().slice(0, 80) || id : `${created}-${id.slice(-4).toLowerCase()}`
    const taken = (name: string) => [...pages.values()].some((p) => p.id !== self && p.folder === folder && p.fileName === name)
    let name = `${base}.md`
    for (let n = 2; taken(name); n++) name = `${base} ${n}.md`
    return name
  }

  const store = (p: Omit<Stored, 'rev'>): Stored => {
    const s: Stored = { ...p, rev: gitBlobId(serializeFile(p.meta, p.body)) }
    pages.set(s.id, s)
    addFolder(s.folder)
    return s
  }

  const toPage = (p: Stored): Page => ({
    id: p.id,
    path: p.folder ? `${p.folder}/${p.fileName}` : p.fileName,
    folder: p.folder,
    rev: p.rev,
    updated: rfc3339(p.updated),
    meta: structuredClone(p.meta),
    body: p.body,
  })

  const toSummary = (p: Stored): PageSummary => {
    const title = stringField(p.meta, 'title')
    const subtitle = stringField(p.meta, 'subtitle')
    const tags = tagsOf(p.meta)
    return {
      id: p.id,
      path: p.folder ? `${p.folder}/${p.fileName}` : p.fileName,
      folder: p.folder,
      ...(title ? { title } : {}),
      ...(subtitle ? { subtitle } : {}),
      // "" for an untitled page with nothing in it.
      label: title ?? Array.from(firstLine(p.body)).slice(0, 30).join(''),
      untitled: !title,
      ...(tags.length ? { tags } : {}),
      type: '',
      ...(typeof p.meta.created === 'string' ? { created: p.meta.created } : {}),
      updated: rfc3339(p.updated),
      excerpt: Array.from(plain(p.body)).slice(0, 200).join(''),
    }
  }

  // --- seed ---------------------------------------------------------------
  if (!options.empty) {
    for (const f of SEED_EMPTY_FOLDERS) addFolder(f)
    seedPages.forEach((sp, i) => {
      const id = `01JD${(i + 1).toString(32).toUpperCase().padStart(6, '0')}`.replace(/[ILOU]/g, 'X')
      const meta: PageMeta = { id }
      if (sp.title) meta.title = sp.title
      if (sp.subtitle) meta.subtitle = sp.subtitle
      if (sp.tags?.length) meta.tags = [...sp.tags]
      meta.created = sp.created
      Object.assign(meta, sp.extra)
      store({ id, folder: sp.folder, fileName: fileNameFor(sp.folder, meta, id), meta, body: sp.body, updated: now() - sp.ago * 60_000 })
    })
  }

  // --- handlers -------------------------------------------------------------
  const get = (id: string): Stored => {
    const p = pages.get(id)
    if (!p) throw new HttpError(404, 'not_found', '找不到这个页面')
    return p
  }

  const sortItems = (items: Stored[], sort: string, relevance?: Map<string, number>) => {
    const desc = sort.startsWith('-')
    const key = desc ? sort.slice(1) : sort
    const dir = desc ? -1 : 1
    const created = (p: Stored) => stringField(p.meta, 'created') ?? ''
    return [...items].sort((a, b) => {
      if (relevance && sort === 'relevance') return (relevance.get(b.id) ?? 0) - (relevance.get(a.id) ?? 0) || b.updated - a.updated
      if (key === 'title') {
        const ta = stringField(a.meta, 'title')
        const tb = stringField(b.meta, 'title')
        // Untitled pages go last, in creation order, whichever way it sorts.
        if (!ta && !tb) return created(a).localeCompare(created(b)) || a.id.localeCompare(b.id)
        if (!ta) return 1
        if (!tb) return -1
        return dir * compareCodePoints(ta.toLowerCase(), tb.toLowerCase()) || a.id.localeCompare(b.id)
      }
      if (key === 'created') return dir * created(a).localeCompare(created(b)) || b.updated - a.updated
      return dir * (a.updated - b.updated) || a.id.localeCompare(b.id)
    })
  }

  const listPages = (q: URLSearchParams) => {
    const folder = q.get('folder')
    const recursive = q.get('recursive') === 'true'
    const tags = q.getAll('tag')
    const text = (q.get('q') ?? '').trim().toLowerCase()
    const limitRaw = q.get('limit')
    const limit = limitRaw === null ? 50 : Number(limitRaw)
    if (!Number.isInteger(limit) || limit < 1 || limit > 200) throw new HttpError(400, 'invalid', 'limit 要在 1 到 200 之间')
    const sortParam = q.get('sort')
    if (sortParam !== null && !/^-?(updated|created|title)$/.test(sortParam)) throw new HttpError(400, 'invalid', '不支持的排序')

    let items = [...pages.values()]
    if (folder !== null) {
      if (!validFolder(folder)) throw new HttpError(400, 'invalid', '文件夹路径不合法')
      items = items.filter((p) => (recursive ? folder === '' || p.folder === folder || p.folder.startsWith(folder + '/') : p.folder === folder))
    }
    if (tags.length) items = items.filter((p) => tags.every((t) => tagsOf(p.meta).includes(t)))

    let relevance: Map<string, number> | undefined
    if (text) {
      const words = text.split(/\s+/)
      relevance = new Map()
      items = items.filter((p) => {
        const title = (stringField(p.meta, 'title') ?? '').toLowerCase()
        const sub = (stringField(p.meta, 'subtitle') ?? '').toLowerCase()
        const body = p.body.toLowerCase()
        if (!words.every((w) => title.includes(w) || sub.includes(w) || body.includes(w))) return false
        relevance!.set(p.id, words.reduce((n, w) => n + (title.includes(w) ? 3 : 0) + (sub.includes(w) ? 2 : 0) + (body.includes(w) ? 1 : 0), 0))
        return true
      })
    }
    const sort = sortParam ?? (text ? 'relevance' : '-updated')
    items = sortItems(items, sort, relevance)

    let offset = 0
    const cursor = q.get('cursor')
    if (cursor) {
      const m = /^o(\d+)$/.exec(atob(cursor))
      if (!m) throw new HttpError(400, 'invalid', 'cursor 不合法')
      offset = Number(m[1])
    }
    const slice = items.slice(offset, offset + limit)
    const end = offset + slice.length
    return { items: slice.map(toSummary), next: end < items.length ? btoa(`o${end}`) : '' }
  }

  const parseMeta = (raw: unknown): PageMeta => {
    if (raw === undefined || raw === null) return {}
    if (!isRecord(raw)) throw new HttpError(400, 'invalid', 'meta 必须是对象')
    return raw
  }

  const create = (body: Record<string, unknown>): Page => {
    const folder = typeof body.folder === 'string' ? body.folder : ''
    if (!validFolder(folder)) throw new HttpError(400, 'invalid', '文件夹路径不合法')
    const given = parseMeta(body.meta)
    if (body.body !== undefined && typeof body.body !== 'string') throw new HttpError(400, 'invalid', 'body 必须是字符串')
    const id = newId()
    const created = new Date(now()).toISOString().slice(0, 10)
    // The server owns id and created; any the client sent are ignored. Id goes
    // first, then the client's fields in the order given, then created.
    const meta: PageMeta = { id }
    const rest = Object.entries(given).filter(([k]) => k !== 'id' && k !== 'created')
    for (const [k, v] of rest.filter(([k]) => ['title', 'subtitle', 'tags'].includes(k))) meta[k] = v
    meta.created = created
    for (const [k, v] of rest.filter(([k]) => !['title', 'subtitle', 'tags'].includes(k))) meta[k] = v
    const s = store({ id, folder, fileName: fileNameFor(folder, meta, id), meta, body: (body.body as string | undefined) ?? '', updated: now() })
    return toPage(s)
  }

  // PUT replaces the header. Keys already in the file keep their place;
  // `id` and `created` stay even when left out; other absent keys are removed;
  // new keys are appended in the order given. Values are taken as they come.
  const mergeHeader = (old: PageMeta, given: PageMeta): PageMeta => {
    const out: PageMeta = {}
    for (const k of Object.keys(old)) {
      if (k in given) out[k] = given[k]
      else if (k === 'id' || k === 'created') out[k] = old[k]
    }
    for (const k of Object.keys(given)) if (!(k in out)) out[k] = given[k]
    return out
  }

  const save = (id: string, input: Record<string, unknown>): Page => {
    const cur = get(id)
    if (!isRecord(input.meta)) throw new HttpError(400, 'invalid', '缺少 meta')
    if (typeof input.body !== 'string') throw new HttpError(400, 'invalid', '缺少 body')
    if (typeof input.baseRev !== 'string' || input.baseRev === '') throw new HttpError(400, 'invalid', '缺少 baseRev')
    const meta = input.meta
    if (meta.id !== undefined && meta.id !== id) throw new HttpError(400, 'invalid', 'meta.id 与页面 id 不一致')
    if (input.baseRev !== cur.rev) throw new HttpError(409, 'conflict', '页面已被修改', { current: toPage(cur) })
    const next = mergeHeader(cur.meta, meta)
    const s = store({ id, folder: cur.folder, fileName: fileNameFor(cur.folder, next, id, id), meta: next, body: input.body, updated: now() })
    return toPage(s)
  }

  const move = (id: string, input: Record<string, unknown>): Page => {
    const cur = get(id)
    if (typeof input.folder !== 'string' || !validFolder(input.folder)) throw new HttpError(400, 'invalid', '文件夹路径不合法')
    const folder = input.folder
    const moved: Stored = { ...cur, folder, fileName: fileNameFor(folder, cur.meta, id, id), updated: now() }
    pages.set(id, moved)
    addFolder(folder)
    return toPage(moved)
  }

  const tree = () => {
    const counts = new Map<string, number>()
    for (const p of pages.values()) counts.set(p.folder, (counts.get(p.folder) ?? 0) + 1)
    return { folders: [...folders].sort((a, b) => a.localeCompare(b, 'zh')).map((path) => ({ path, pages: counts.get(path) ?? 0 })) }
  }

  const tags = () => {
    const counts = new Map<string, number>()
    for (const p of pages.values()) for (const t of new Set(tagsOf(p.meta))) counts.set(t, (counts.get(t) ?? 0) + 1)
    return { tags: [...counts].map(([tag, n]) => ({ tag, pages: n })).sort((a, b) => b.pages - a.pages || a.tag.localeCompare(b.tag, 'zh')) }
  }

  const route = (method: string, url: URL, body: Record<string, unknown>): MockResponse => {
    const path = url.pathname.replace(/^\/api/, '')
    if (method === 'GET' && path === '/meta') return { status: 200, body: { version: 'mock', spaces: [{ id: 'default', name: SPACE_NAME }] } }

    // Mock only: change a page behind the app's back, as another editor or a
    // `git pull` would, to exercise the conflict path by hand.
    const touch = /^\/_mock\/touch\/([^/]+)$/.exec(path)
    if (method === 'POST' && touch) {
      const cur = get(touch[1])
      const meta = { ...cur.meta }
      if (typeof body.title === 'string') meta.title = body.title
      const s = store({ ...cur, meta, body: typeof body.body === 'string' ? body.body : cur.body + '\n（别处追加的一行）\n', updated: now() })
      return { status: 200, body: toPage(s) }
    }

    const m = /^\/spaces\/([^/]+)(\/.*)?$/.exec(path)
    if (!m) throw new HttpError(404, 'not_found', '没有这个接口')
    if (m[1] !== 'default') throw new HttpError(404, 'not_found', '没有这个空间')
    const rest = m[2] ?? ''

    if (rest === '/pages') {
      if (method === 'GET') return { status: 200, body: listPages(url.searchParams) }
      if (method === 'POST') return { status: 201, body: create(body) }
    }
    const one = /^\/pages\/([^/]+)(\/move)?$/.exec(rest)
    if (one) {
      const id = decodeURIComponent(one[1])
      if (!ID_RE.test(id) && !pages.has(id)) throw new HttpError(404, 'not_found', '找不到这个页面')
      if (!one[2]) {
        if (method === 'GET') return { status: 200, body: toPage(get(id)) }
        if (method === 'PUT') return { status: 200, body: save(id, body) }
        if (method === 'DELETE') {
          get(id)
          pages.delete(id)
          return { status: 204 }
        }
      } else if (method === 'POST') return { status: 200, body: move(id, body) }
    }
    if (rest === '/tree' && method === 'GET') return { status: 200, body: tree() }
    if (rest === '/tags' && method === 'GET') return { status: 200, body: tags() }
    if (rest === '/folders' && method === 'POST') {
      const p = body.path
      if (typeof p !== 'string') throw new HttpError(400, 'invalid', '缺少 path')
      // The root always exists, so creating it is "already there", not invalid.
      if (p !== '' && !validFolder(p)) throw new HttpError(400, 'invalid', folderRuleMessage(p) ?? '文件夹路径不合法')
      if (folders.has(p)) throw new HttpError(409, 'exists', '这个文件夹已经存在')
      addFolder(p)
      return { status: 201, body: { path: p } }
    }
    if (rest === '/reindex' && method === 'POST') return { status: 200, body: { pages: pages.size, ms: 1 } }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  return {
    // `target` is a path with query, as sent by the client ("/api/...").
    handle(method: string, target: string, bodyText?: string, contentType?: string | null): MockResponse {
      counter++
      try {
        // Writes must say they are JSON, like the real server insists.
        if (method.toUpperCase() !== 'GET' && !/^application\/json\s*(;|$)/i.test(contentType ?? '')) throw new HttpError(415, 'invalid', '请求需要 Content-Type: application/json')
        let body: Record<string, unknown> = {}
        if (bodyText) {
          try {
            const parsed: unknown = JSON.parse(bodyText)
            if (!isRecord(parsed)) throw new Error('not an object')
            body = parsed
          } catch {
            throw new HttpError(400, 'invalid', '请求体不是合法的 JSON')
          }
        }
        return route(method.toUpperCase(), new URL(target, 'http://mock.local'), body)
      } catch (e) {
        if (e instanceof HttpError) return { status: e.status, body: { error: e.code, message: e.message, ...e.extra } }
        throw e
      }
    },
    // Test helpers.
    get requestCount() {
      return counter
    },
    peek: (id: string) => (pages.has(id) ? toPage(get(id)) : undefined),
    ids: () => [...pages.keys()],
  }
}

export type MockServer = ReturnType<typeof createMockServer>
