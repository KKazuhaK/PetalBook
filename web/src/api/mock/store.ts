// One space of the in-memory server: pages, folders and the per-user drafts.
// Strict about the parts the web app depends on (revs, 409 conflicts, header
// order, untitled pages, cursors, draft semantics), simple about the rest.

import type { DraftReceipt, HistoryList, Page, PageDraft, PageMeta, PageSummary, PageVersion, TagColor, VersionChange } from '../types.ts'
import { TAG_COLORS, parseTagColor } from '../../lib/tagColors.ts'
import { folderRuleMessage } from '../../lib/folderRules.ts'
import { gitBlobId } from './sha1.ts'
import { SEED_EMPTY_FOLDERS, seedPages } from './seed.ts'

export class HttpError extends Error {
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

interface Stored {
  id: string
  folder: string
  fileName: string
  meta: PageMeta
  body: string
  rev: string
  updated: number
}

// One commit that touched a page's file.
interface VersionRec {
  commit: string
  time: number
  author: string
  path: string
  rev?: string
  change: VersionChange
  meta: PageMeta
  body: string
}

interface DraftRecord {
  version: string
  baseRev: string
  meta: PageMeta
  body: string
  updated: number
  // When the draft was first stored since it last went away.
  first: number
}

export const ALPHABET = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
export const ID_RE = /^[0-9A-HJKMNP-TV-Z]{10}$/

export const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

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

// The text of a page for a listing: up to 400 characters, one line per
// non-blank line of the body (docs/api.md, "Page summary").
function excerptOf(markdown: string): string {
  const lines = markdown
    .replace(/```[^\n]*\n([\s\S]*?)```/g, '$1')
    .split('\n')
    .map((l) => plain(l))
    .filter(Boolean)
  return Array.from(lines.join('\n')).slice(0, 400).join('')
}

function firstLine(body: string): string {
  for (const raw of body.split('\n')) {
    const line = plain(raw)
    if (line) return line
  }
  return ''
}

// Titles compare by code point (not by locale), case-insensitively.
export function compareCodePoints(a: string, b: string): number {
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

export const rfc3339 = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')

export interface StoreOptions {
  now: () => number
  random: () => number
  seed: boolean
  // The name on the commits: the space's owner.
  author?: () => string
  // A reason why writes must fail with 503 `unavailable` right now (the space
  // follows a remote that cannot be reached, or is paused), or null.
  blocked?: () => string | null
}

export function createSpaceStore({ now, random, seed, author = () => 'Local user', blocked = () => null }: StoreOptions) {
  const pages = new Map<string, Stored>()
  // Every page's commits, oldest first; kept after the page is deleted.
  const histories = new Map<string, VersionRec[]>()
  let commits = 0
  // Whether any page or folder was ever made: a pristine space may take a remote's content.
  let touched = false
  const guard = () => {
    const why = blocked()
    if (why) throw new HttpError(503, 'unavailable', why)
  }
  const folders = new Set<string>([''])
  // userId + page id -> draft
  const drafts = new Map<string, DraftRecord>()
  const draftKey = (userId: string, id: string) => `${userId}\u0000${id}`
  let versions = 0
  // Opaque and new on every store, even within one second.
  const newVersion = () => `v${(++versions).toString(36)}${Math.floor(random() * 1e6).toString(36)}`

  const newId = () => {
    for (;;) {
      let id = '01'
      for (let i = 0; i < 8; i++) id += ALPHABET[Math.floor(random() * 32)]
      if (!pages.has(id)) return id
    }
  }
  const addFolder = (path: string) => {
    ancestors(path).forEach((p) => folders.add(p))
    if (path !== '') touched = true
  }

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
    touched = true
    return s
  }

  const pathOf = (p: Stored) => (p.folder ? `${p.folder}/${p.fileName}` : p.fileName)

  // A commit for what the file is now. Ids look like Git's and are unique.
  const record = (p: Stored, change: VersionChange, at = now()) => {
    const list = histories.get(p.id) ?? []
    histories.set(p.id, list)
    const commit = gitBlobId(`commit ${++commits} ${p.id} ${p.rev} ${at}`)
    list.push({ commit, time: at, author: author(), path: pathOf(p), ...(change === 'delete' ? {} : { rev: p.rev }), change, meta: structuredClone(p.meta), body: p.body })
  }

  const toPage = (p: Stored): Page => ({
    id: p.id,
    path: pathOf(p),
    folder: p.folder,
    rev: p.rev,
    updated: rfc3339(p.updated),
    meta: structuredClone(p.meta),
    // The header as this pretend file has it (see serializeFile): JSON values, which are YAML too.
    header: Object.entries(p.meta)
      .map(([k, v]) => `${k}: ${JSON.stringify(v)}\n`)
      .join(''),
    body: p.body,
  })

  const toSummary = (p: Stored): PageSummary => {
    const title = stringField(p.meta, 'title')
    const subtitle = stringField(p.meta, 'subtitle')
    const source = stringField(p.meta, 'source')
    const tags = tagsOf(p.meta)
    return {
      id: p.id,
      path: pathOf(p),
      folder: p.folder,
      ...(title ? { title } : {}),
      ...(subtitle ? { subtitle } : {}),
      ...(source ? { source } : {}),
      // "" for an untitled page with nothing in it.
      label: title ?? Array.from(firstLine(p.body)).slice(0, 30).join(''),
      untitled: !title,
      ...(tags.length ? { tags } : {}),
      type: '',
      ...(typeof p.meta.created === 'string' ? { created: p.meta.created } : {}),
      updated: rfc3339(p.updated),
      excerpt: excerptOf(p.body),
    }
  }

  // Kept apart from the pages, as `.petalbook/tags.yml` is on the server; a
  // colour outlives the last page that uses the tag.
  const tagColors = new Map<string, TagColor>()

  // --- seed ---------------------------------------------------------------
  if (seed) {
    // A couple of coloured tags, so the colours show in a fresh mock.
    tagColors.set('随笔', 'rose')
    tagColors.set('待办', 'amber')
    for (const f of SEED_EMPTY_FOLDERS) addFolder(f)
    seedPages.forEach((sp, i) => {
      const id = `01JD${(i + 1).toString(32).toUpperCase().padStart(6, '0')}`.replace(/[ILOU]/g, 'X')
      const meta: PageMeta = { id }
      if (sp.title) meta.title = sp.title
      if (sp.subtitle) meta.subtitle = sp.subtitle
      if (sp.tags?.length) meta.tags = [...sp.tags]
      meta.created = sp.created
      Object.assign(meta, sp.extra)
      const at = now() - sp.ago * 60_000
      // Earlier versions first (oldest to newest), each a commit of its own.
      let prev: Stored | undefined
      for (const e of sp.earlier ?? []) {
        const em: PageMeta = { id }
        if (e.title) em.title = e.title
        if (e.subtitle) em.subtitle = e.subtitle
        if (e.tags?.length) em.tags = [...e.tags]
        em.created = sp.created
        const folder = e.folder ?? sp.folder
        const old: Stored = { id, folder, fileName: `${e.title ?? id}.md`, meta: em, body: e.body, rev: gitBlobId(serializeFile(em, e.body)), updated: now() - e.ago * 60_000 }
        record(old, prev ? (prev.rev === old.rev ? 'move' : 'edit') : 'add', old.updated)
        prev = old
      }
      const s = store({ id, folder: sp.folder, fileName: fileNameFor(sp.folder, meta, id), meta, body: sp.body, updated: at })
      record(s, prev ? (prev.rev === s.rev ? 'move' : 'edit') : 'add', at)
    })
  }

  // --- pages --------------------------------------------------------------
  const get = (id: string): Stored => {
    const p = pages.get(id)
    if (!p) throw new HttpError(404, 'not_found', '找不到这个页面')
    return p
  }
  // An id that cannot exist is the same 404 as one that does not.
  const lookup = (id: string): Stored => {
    if (!ID_RE.test(id) && !pages.has(id)) throw new HttpError(404, 'not_found', '找不到这个页面')
    return get(id)
  }

  const draftOf = (userId: string, p: Stored): PageDraft | undefined => {
    const d = drafts.get(draftKey(userId, p.id))
    if (!d) return undefined
    return { version: d.version, baseRev: d.baseRev, meta: structuredClone(d.meta), body: d.body, updated: rfc3339(d.updated), stale: d.baseRev !== p.rev }
  }

  // GET adds the caller's draft, if there is one.
  const getPage = (userId: string, id: string): Page => {
    const p = lookup(id)
    const draft = draftOf(userId, p)
    return { ...toPage(p), ...(draft ? { draft } : {}) }
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
    const inParam = q.get('in')
    if (inParam !== null && inParam !== 'title') throw new HttpError(400, 'invalid', 'in 只能是 title')
    const titleOnly = inParam === 'title'
    // Days, UTC, both ends included; one that is not a day is refused.
    const day = (name: string): string | null => {
      const v = q.get(name)
      if (v === null) return null
      if (!/^\d{4}-\d{2}-\d{2}$/.test(v) || Number.isNaN(Date.parse(v))) throw new HttpError(400, 'invalid', `${name} 要写成 2026-11-01 这样的日期`)
      return v
    }
    const updatedFrom = day('updatedFrom')
    const updatedTo = day('updatedTo')
    const createdFrom = day('createdFrom')
    const createdTo = day('createdTo')
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
    // `updated` is the day the file was last written; `created` is the header's
    // own date, and a page without one never matches a created range.
    if (updatedFrom || updatedTo)
      items = items.filter((p) => {
        const d = rfc3339(p.updated).slice(0, 10)
        return (!updatedFrom || d >= updatedFrom) && (!updatedTo || d <= updatedTo)
      })
    if (createdFrom || createdTo)
      items = items.filter((p) => {
        const d = stringField(p.meta, 'created')
        return d !== undefined && (!createdFrom || d >= createdFrom) && (!createdTo || d <= createdTo)
      })

    let relevance: Map<string, number> | undefined
    if (text) {
      const words = text.split(/\s+/)
      relevance = new Map()
      items = items.filter((p) => {
        const title = (stringField(p.meta, 'title') ?? '').toLowerCase()
        const sub = (stringField(p.meta, 'subtitle') ?? '').toLowerCase()
        const src = (stringField(p.meta, 'source') ?? '').toLowerCase()
        const body = p.body.toLowerCase()
        if (titleOnly) {
          if (!words.every((w) => title.includes(w))) return false
          relevance!.set(p.id, words.length)
          return true
        }
        if (!words.every((w) => title.includes(w) || sub.includes(w) || src.includes(w) || body.includes(w))) return false
        relevance!.set(p.id, words.reduce((n, w) => n + (title.includes(w) ? 3 : 0) + (sub.includes(w) ? 2 : 0) + (src.includes(w) ? 1.5 : 0) + (body.includes(w) ? 1 : 0), 0))
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
    guard()
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
    for (const [k, v] of rest.filter(([k]) => ['title', 'subtitle', 'source', 'tags'].includes(k))) meta[k] = v
    meta.created = created
    for (const [k, v] of rest.filter(([k]) => !['title', 'subtitle', 'source', 'tags'].includes(k))) meta[k] = v
    const s = store({ id, folder, fileName: fileNameFor(folder, meta, id), meta, body: (body.body as string | undefined) ?? '', updated: now() })
    record(s, 'add')
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

  // The checks a save and a draft store share.
  const checkInput = (id: string, input: Record<string, unknown>): { baseRev: string; meta: PageMeta; body: string } => {
    if (!isRecord(input.meta)) throw new HttpError(400, 'invalid', '缺少 meta')
    if (typeof input.body !== 'string') throw new HttpError(400, 'invalid', '缺少 body')
    if (typeof input.baseRev !== 'string' || input.baseRev === '') throw new HttpError(400, 'invalid', '缺少 baseRev')
    if (input.meta.id !== undefined && input.meta.id !== id) throw new HttpError(400, 'invalid', 'meta.id 与页面 id 不一致')
    return { baseRev: input.baseRev, meta: input.meta, body: input.body }
  }

  // Write `meta`/`body` to the file if `baseRev` is still its rev. Writing the
  // bytes the file already has changes nothing.
  const writeFile = (cur: Stored, baseRev: string, given: PageMeta, body: string): Stored => {
    if (baseRev !== cur.rev) throw new HttpError(409, 'conflict', '页面已被修改', { current: toPage(cur) })
    const next = mergeHeader(cur.meta, given)
    const fileName = fileNameFor(cur.folder, next, cur.id, cur.id)
    const rev = gitBlobId(serializeFile(next, body))
    if (rev === cur.rev && fileName === cur.fileName) return cur
    const s = store({ id: cur.id, folder: cur.folder, fileName, meta: next, body, updated: now() })
    record(s, rev === cur.rev ? 'move' : 'edit')
    return s
  }

  const save = (userId: string, id: string, input: Record<string, unknown>): Page => {
    const cur = lookup(id)
    guard()
    const { baseRev, meta, body } = checkInput(id, input)
    const s = writeFile(cur, baseRev, meta, body)
    // A save never takes a draft away (its text may exist nowhere else), except
    // one that says exactly what was just saved.
    const d = drafts.get(draftKey(userId, id))
    if (d && sameParsed(s, d)) drafts.delete(draftKey(userId, id))
    return toPage(s)
  }

  const move = (id: string, input: Record<string, unknown>): Page => {
    const cur = lookup(id)
    guard()
    if (typeof input.folder !== 'string' || !validFolder(input.folder)) throw new HttpError(400, 'invalid', '文件夹路径不合法')
    const folder = input.folder
    const moved: Stored = { ...cur, folder, fileName: fileNameFor(folder, cur.meta, id, id), updated: now() }
    // Moving to where it already is changes nothing, so it is no version.
    if (pathOf(moved) === pathOf(cur)) return toPage(cur)
    pages.set(id, moved)
    addFolder(folder)
    record(moved, 'move')
    return toPage(moved)
  }

  const remove = (id: string) => {
    const gone = lookup(id)
    guard()
    pages.delete(id)
    record(gone, 'delete')
    for (const k of [...drafts.keys()]) if (k.endsWith(`\u0000${id}`)) drafts.delete(k)
  }

  // Several pages in one change. Everything is checked before anything is
  // touched, so a bad request or an unknown id leaves the space as it was.
  const batch = (input: Record<string, unknown>): { pages: number } => {
    guard()
    const op = input.op
    if (op !== 'move' && op !== 'delete') throw new HttpError(400, 'invalid', 'op 要是 move 或 delete')
    const list = input.ids
    if (!Array.isArray(list) || list.length < 1 || list.length > 500 || list.some((i) => typeof i !== 'string') || new Set(list).size !== list.length) {
      throw new HttpError(400, 'invalid', 'ids 要是 1 到 500 个不重复的页面 id')
    }
    const idList = list as string[]
    if (op === 'move' && (typeof input.folder !== 'string' || !validFolder(input.folder))) throw new HttpError(400, 'invalid', '文件夹路径不合法')
    const found = idList.map((id) => lookup(id))
    if (op === 'delete') {
      for (const p of found) remove(p.id)
      return { pages: found.length }
    }
    const folder = input.folder as string
    for (const cur of found) {
      const moved: Stored = { ...cur, folder, fileName: fileNameFor(folder, cur.meta, cur.id, cur.id), updated: now() }
      // A page already there counts and stays as it is.
      if (pathOf(moved) === pathOf(cur)) continue
      pages.set(cur.id, moved)
      addFolder(folder)
      record(moved, 'move')
    }
    addFolder(folder)
    return { pages: found.length }
  }

  // --- drafts ---
  // Whether a draft says what the file says: header (as it would be merged, key
  // order aside) and body, parsed rather than compared as bytes.
  const canon = (v: unknown): string => (Array.isArray(v) ? `[${v.map(canon).join(',')}]` : v && typeof v === 'object' ? `{${Object.keys(v).sort().map((k) => `${JSON.stringify(k)}:${canon((v as Record<string, unknown>)[k])}`).join(',')}}` : JSON.stringify(v) ?? 'null')
  const sameParsed = (cur: Stored, d: DraftRecord) => d.body === cur.body && canon(mergeHeader(cur.meta, d.meta)) === canon(cur.meta)
  // 409 for a request that names a draft the server does not have: the caller's
  // draft as it is (or null) and the page as it is.
  const draftConflict = (userId: string, p: Stored) => new HttpError(409, 'conflict', '服务器上的草稿不是这一份', { draft: draftOf(userId, p) ?? null, current: toPage(p) })

  const putDraft = (userId: string, id: string, input: Record<string, unknown>): DraftReceipt => {
    const cur = lookup(id)
    const { baseRev, meta, body } = checkInput(id, input)
    if (input.prev !== undefined && typeof input.prev !== 'string') throw new HttpError(400, 'invalid', 'prev 要是字符串')
    const prev = (input.prev as string | undefined) ?? ''
    const key = draftKey(userId, id)
    const before = drafts.get(key)
    // Refused when the draft there is not the one being replaced.
    if ((before?.version ?? '') !== prev) throw draftConflict(userId, cur)
    const t = now()
    const version = newVersion()
    drafts.set(key, { version, baseRev, meta: structuredClone(meta), body, updated: t, first: before && before.baseRev === baseRev ? before.first : t })
    return { version, baseRev, updated: rfc3339(t), stale: baseRev !== cur.rev }
  }

  const deleteDraft = (userId: string, id: string, version?: string | null, rev?: string | null) => {
    const d = drafts.get(draftKey(userId, id))
    // With a rev too: the file must be at it.
    if (version && rev && lookup(id).rev !== rev) throw draftConflict(userId, lookup(id))
    // With a version: only that draft. Another one, or none any more (the server
    // committed it already), is a 409; without a version it is 204 either way.
    if (version && (!d || d.version !== version)) throw draftConflict(userId, lookup(id))
    drafts.delete(draftKey(userId, id))
  }

  // The draft of that version, exactly as a save would write it. Anything else
  // (another version, none, or a stale draft) is a 409 and nothing changes.
  const commit = (userId: string, id: string, input: Record<string, unknown>): Page => {
    const cur = lookup(id)
    guard()
    if (typeof input.version !== 'string' || input.version === '') throw new HttpError(400, 'invalid', '缺少 version')
    const d = drafts.get(draftKey(userId, id))
    if (!d || d.version !== input.version) throw draftConflict(userId, cur)
    if (d.baseRev !== cur.rev) {
      // Stale. If it says what the file says (parsed header and body) there is
      // nothing to lose: the draft is the file, so it goes and the page is returned.
      if (!sameParsed(cur, d)) throw draftConflict(userId, cur)
      drafts.delete(draftKey(userId, id))
      return toPage(cur)
    }
    const s = writeFile(cur, d.baseRev, d.meta, d.body)
    drafts.delete(draftKey(userId, id))
    return toPage(s)
  }

  // The server's own commits: a draft quiet for `idleMs`, or first stored
  // `maxMs` ago, is committed. A stale draft is left alone.
  const sweep = (idleMs: number, maxMs: number) => {
    // The server writes drafts once the remote is back; until then they wait.
    if (blocked()) return
    const t = now()
    for (const [key, d] of [...drafts]) {
      if (t - d.updated < idleMs && t - d.first < maxMs) continue
      const id = key.split('\u0000')[1]
      const cur = pages.get(id)
      // A draft of a page that is not there is held, not deleted.
      if (!cur) continue
      if (d.baseRev !== cur.rev) {
        // A stale draft that says what the file says is only a leftover: removed.
        if (sameParsed(cur, d)) drafts.delete(key)
        continue
      }
      writeFile(cur, d.baseRev, d.meta, d.body)
      drafts.delete(key)
    }
  }

  // --- history ---
  // A deleted page keeps its history here, so a test can see the version with
  // no rev; the real server may answer 404 for a page that is gone.
  const versionsOf = (id: string): VersionRec[] => {
    if (!pages.has(id) && !histories.has(id)) throw new HttpError(404, 'not_found', '找不到这个页面')
    return histories.get(id) ?? []
  }
  const itemOf = (v: VersionRec) => ({ commit: v.commit, time: rfc3339(v.time), author: v.author, path: v.path, ...(v.rev ? { rev: v.rev } : {}), change: v.change })

  const history = (id: string, q: URLSearchParams): HistoryList => {
    const list = versionsOf(id)
    const limitRaw = q.get('limit')
    const limit = limitRaw === null ? 30 : Number(limitRaw)
    if (!Number.isInteger(limit) || limit < 1 || limit > 100) throw new HttpError(400, 'invalid', 'limit 要在 1 到 100 之间')
    let offset = 0
    const cursor = q.get('cursor')
    if (cursor) {
      let m: RegExpExecArray | null = null
      try {
        m = /^h(\d+)$/.exec(atob(cursor))
      } catch {
        // Not base64 at all.
      }
      if (!m) throw new HttpError(400, 'invalid', 'cursor 不合法')
      offset = Number(m[1])
    }
    const newestFirst = [...list].reverse()
    const slice = newestFirst.slice(offset, offset + limit)
    const end = offset + slice.length
    return { items: slice.map(itemOf), next: end < newestFirst.length ? btoa(`h${end}`) : '' }
  }

  const version = (id: string, commit: string): PageVersion => {
    const v = versionsOf(id).find((x) => x.commit === commit)
    // Another page's commit is as unknown here as one that does not exist.
    if (!v) throw new HttpError(404, 'not_found', '这个提交不是该页面的版本')
    return { ...itemOf(v), meta: v.change === 'delete' ? {} : structuredClone(v.meta), body: v.change === 'delete' ? '' : v.body }
  }

  // The remote's content for a space that had none: pages made as the remote's
  // commits (mock only; the pages are invented).
  const adopt = (items: { folder: string; title: string; body: string; tags?: string[] }[]) => {
    for (const it of items) {
      const id = newId()
      const meta: PageMeta = { id, title: it.title, ...(it.tags?.length ? { tags: it.tags } : {}), created: new Date(now()).toISOString().slice(0, 10) }
      const s = store({ id, folder: it.folder, fileName: fileNameFor(it.folder, meta, id), meta, body: it.body, updated: now() })
      record(s, 'add')
    }
  }

  const tree = () => {
    const counts = new Map<string, number>()
    for (const p of pages.values()) counts.set(p.folder, (counts.get(p.folder) ?? 0) + 1)
    return { folders: [...folders].sort((a, b) => a.localeCompare(b, 'zh')).map((path) => ({ path, pages: counts.get(path) ?? 0 })) }
  }

  const tags = () => {
    const counts = new Map<string, number>()
    for (const p of pages.values()) for (const t of new Set(tagsOf(p.meta))) counts.set(t, (counts.get(t) ?? 0) + 1)
    return { tags: [...counts].map(([tag, n]) => ({ tag, pages: n, ...(tagColors.has(tag) ? { color: tagColors.get(tag) } : {}) })).sort((a, b) => b.pages - a.pages || a.tag.localeCompare(b.tag, 'zh')) }
  }

  // `{"color": "rose"}` sets, `{"color": null}` removes; the tag need not be in use.
  const setTagColor = (tag: string, body: Record<string, unknown>) => {
    guard()
    if (tag === '') throw new HttpError(400, 'invalid', '标签不能是空的')
    if (!('color' in body)) throw new HttpError(400, 'invalid', '缺少 color')
    const c = body.color
    if (c === null) {
      tagColors.delete(tag)
      return { tag }
    }
    const parsed = parseTagColor(c)
    if (parsed === null) throw new HttpError(400, 'invalid', `color 只能是 ${TAG_COLORS.join('、')} 之一、#rrggbb，或 null`)
    const color = parsed.kind === 'preset' ? parsed.name : parsed.hex
    tagColors.set(tag, color)
    return { tag, color }
  }

  const createFolder = (p: unknown) => {
    if (typeof p !== 'string') throw new HttpError(400, 'invalid', '缺少 path')
    // The root always exists, so creating it is "already there", not invalid.
    if (p !== '' && !validFolder(p)) throw new HttpError(400, 'invalid', folderRuleMessage(p) ?? '文件夹路径不合法')
    if (folders.has(p)) throw new HttpError(409, 'exists', '这个文件夹已经存在')
    addFolder(p)
    return { path: p }
  }

  const isUnder = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')

  // Moves a folder with everything in it; this is also how one is renamed.
  // Pages keep their ids, so nothing that points at them changes.
  const moveFolder = (input: Record<string, unknown>): { path: string } => {
    guard()
    const { from, to } = input
    if (typeof from !== 'string' || typeof to !== 'string') throw new HttpError(400, 'invalid', '缺少 from 或 to')
    if (from === '') throw new HttpError(400, 'invalid', '根目录不能移动')
    if (!folders.has(from)) throw new HttpError(404, 'not_found', '找不到这个文件夹')
    if (to === '' || !validFolder(to)) throw new HttpError(400, 'invalid', folderRuleMessage(to) ?? '文件夹路径不合法')
    if (to === from || to.startsWith(from + '/')) throw new HttpError(400, 'invalid', '不能把文件夹移到它自己里面')
    // Another spelling of the folder itself (a change of letter case) is not "already there".
    const sameFolder = to.toLowerCase() === from.toLowerCase()
    if (folders.has(to) && !sameFolder) throw new HttpError(409, 'exists', '目标位置已经有这个文件夹')
    const rename = (path: string) => to + path.slice(from.length)
    const inside = [...folders].filter((f) => isUnder(f, from))
    for (const f of inside) folders.delete(f)
    for (const f of inside) addFolder(rename(f))
    addFolder(to)
    for (const p of [...pages.values()].filter((x) => isUnder(x.folder, from))) {
      const moved: Stored = { ...p, folder: rename(p.folder) }
      pages.set(p.id, moved)
      record(moved, 'move')
    }
    return { path: to }
  }

  // Without `recursive` the folder must hold no page, in it or below it.
  const removeFolder = (path: string | null, recursive: boolean) => {
    guard()
    if (path === '') throw new HttpError(400, 'invalid', '根目录不能删除')
    if (path === null || !folders.has(path)) throw new HttpError(404, 'not_found', '找不到这个文件夹')
    const doomed = [...pages.values()].filter((p) => isUnder(p.folder, path))
    if (doomed.length > 0 && !recursive) throw new HttpError(409, 'conflict', '文件夹里还有页面')
    for (const p of doomed) remove(p.id)
    for (const f of [...folders]) if (isUnder(f, path)) folders.delete(f)
  }

  // Mock only: change a page behind the app's back, as another editor or a
  // `git pull` would, to exercise the conflict path by hand.
  const touch = (id: string, body: Record<string, unknown>): Page => {
    const cur = get(id)
    const meta = { ...cur.meta }
    if (typeof body.title === 'string') meta.title = body.title
    const s = store({ ...cur, meta, body: typeof body.body === 'string' ? body.body : cur.body + '\n（别处追加的一行）\n', updated: now() })
    record(s, 'edit')
    return toPage(s)
  }

  return {
    listPages,
    getPage,
    create,
    save,
    move,
    remove,
    putDraft,
    deleteDraft,
    commit,
    sweep,
    tree,
    tags,
    setTagColor,
    createFolder,
    moveFolder,
    removeFolder,
    batch,
    touch,
    history,
    version,
    adopt,
    pristine: () => !touched,
    reindex: () => ({ pages: pages.size, ms: 1 }),
    peek: (id: string) => (pages.has(id) ? toPage(get(id)) : undefined),
    peekDraft: (userId: string, id: string) => {
      const p = pages.get(id)
      return p ? draftOf(userId, p) : undefined
    },
    ids: () => [...pages.keys()],
  }
}

export type SpaceStore = ReturnType<typeof createSpaceStore>
