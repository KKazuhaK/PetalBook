// The saved views of one space of the in-memory server (docs/api.md, "Saved
// views"): the views of folders and of tags, their rev, the checks of a save,
// and what a folder's move or removal does to them.

import { HttpError } from './store.ts'
import { MAX_VIEW_FILTERS, parseFilter, type PropFilter } from './propQuery.ts'
import { gitBlobId } from './sha1.ts'

const MAX_VIEWS = 20
const LAYOUTS: readonly string[] = ['list', 'cards', 'table', 'board', 'read']
const KNOWN = ['id', 'name', 'layout', 'sort', 'group', 'filter', 'hidden']

export interface SavedView {
  id: string
  name: string
  layout: string
  sort: string
  group: string
  filter: PropFilter[]
  hidden: string[]
  // Keys the server does not know are kept as they came.
  [extra: string]: unknown
}
export interface ViewList {
  views: SavedView[]
  rev: string
}

const bytes = (s: string) => new TextEncoder().encode(s).length
const invalid = (message: string) => new HttpError(400, 'invalid', message)
const isUnder = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')
const oneLine = (s: string, max: number) => bytes(s) <= max && !/[\p{Cc}]/u.test(s)
const listOf = (views: SavedView[]): ViewList => ({ views: structuredClone(views), rev: gitBlobId(JSON.stringify(views)) })

export function createViewBook(opts: { random: () => number; spelledFolder: (path: string) => string | null; validFolder: (path: string) => boolean }) {
  // 'f:<folder>' and 't:<tag>' -> views
  const books = new Map<string, SavedView[]>()

  const scopeOf = (folder: string | undefined, tag: string | undefined): string => {
    if (folder !== undefined && tag !== undefined && tag !== '') throw invalid('给文件夹或标签，不要两个都给')
    if (folder !== undefined) {
      if (!opts.validFolder(folder)) throw invalid('文件夹路径不合法')
      return 'f:' + folder
    }
    if (tag === undefined || tag === '') throw invalid('要给出视图属于哪个文件夹或标签')
    if (tag.trim() === '' || bytes(tag) > 200 || /[\r\n\0]/.test(tag)) throw invalid('这不是标签')
    return 't:' + tag
  }

  const checkViews = (raw: unknown): SavedView[] => {
    if (!Array.isArray(raw)) throw invalid('views 要是数组')
    if (raw.length > MAX_VIEWS) throw invalid(`一个文件夹或标签最多 ${MAX_VIEWS} 个视图`)
    const ids = new Set<string>()
    return raw.map((item, i): SavedView => {
      const at = `第 ${i + 1} 个视图`
      if (typeof item !== 'object' || item === null || Array.isArray(item)) throw invalid(`${at}要是对象`)
      const o = item as Record<string, unknown>
      const text = (key: string): string => {
        const v = o[key]
        if (v === undefined || v === null) return ''
        if (typeof v !== 'string') throw invalid(`${at}的 ${key} 要是文字`)
        return v
      }
      const id = text('id')
      if (id !== '') {
        if (!/^[A-Za-z0-9_-]{1,32}$/.test(id) && !/^@\d+$/.test(id)) throw invalid(`${at}的 id 不合法`)
        if (ids.has(id)) throw invalid(`${at}的 id 重复了`)
        ids.add(id)
      }
      const name = text('name')
      if (name.trim() === '' || !oneLine(name, 200)) throw invalid(`${at}要有名字，一行，不超过 200 字节`)
      const layout = text('layout')
      if (!LAYOUTS.includes(layout)) throw invalid(`${at}的 layout 要是 ${LAYOUTS.join('、')} 之一`)
      const sort = text('sort')
      if (sort !== '' && !/^-?(updated|created|title|prop:.+)$/.test(sort)) throw invalid(`${at}的 sort 不支持`)
      const group = text('group')
      if (!oneLine(group, 256)) throw invalid(`${at}的 group 不合法`)
      const filter = o.filter === undefined || o.filter === null ? [] : parseFilter(JSON.stringify(o.filter), MAX_VIEW_FILTERS)
      let hidden: string[] = []
      if (o.hidden !== undefined && o.hidden !== null) {
        if (!Array.isArray(o.hidden) || !o.hidden.every((h) => typeof h === 'string' && h !== '' && oneLine(h, 256))) throw invalid(`${at}的 hidden 要是一串键`)
        hidden = o.hidden as string[]
        if (new Set(hidden).size !== hidden.length || hidden.length > 200) throw invalid(`${at}的 hidden 每个键只能出现一次，最多 200 个`)
      }
      const extra = Object.fromEntries(Object.entries(o).filter(([k]) => !KNOWN.includes(k)))
      for (const k of Object.keys(extra)) if (k.length === 0 || bytes(k) > 64) throw invalid(`${at}的键要是 1 到 64 字节`)
      return { ...extra, id, name, layout, sort, group, filter, hidden }
    })
  }

  const taken = (except: string): Set<string> => {
    const out = new Set<string>()
    for (const [k, vs] of books) if (k !== except) for (const v of vs) out.add(v.id)
    return out
  }

  return {
    get(folder: string | undefined, tag: string | undefined): ViewList {
      return listOf(books.get(scopeOf(folder, tag)) ?? [])
    },

    put(body: Record<string, unknown>): ViewList {
      const folder = typeof body.folder === 'string' ? body.folder : body.folder === undefined || body.folder === null ? undefined : invalid('folder 要是文字')
      if (folder instanceof HttpError) throw folder
      const tag = typeof body.tag === 'string' ? body.tag : undefined
      if (typeof body.baseRev !== 'string' || body.baseRev === '') throw invalid('缺少 baseRev')
      if (body.views === undefined || body.views === null) throw invalid('缺少 views')
      const requested = checkViews(body.views)
      let key = scopeOf(folder, tag)
      if (folder !== undefined) {
        const spelled = opts.spelledFolder(folder)
        if (spelled === null) throw new HttpError(404, 'not_found', '找不到这个文件夹')
        key = 'f:' + spelled
      }
      const cur = books.get(key) ?? []
      const current = listOf(cur)
      if (body.baseRev !== current.rev) throw new HttpError(409, 'conflict', '视图已被别人改过', { current })
      const used = taken(key)
      for (const v of requested) if (v.id) used.add(v.id)
      const next = requested.map((v): SavedView => {
        if (v.id === '') {
          let id: string
          do {
            id = Array.from({ length: 6 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(opts.random() * 36)]).join('')
          } while (used.has(id))
          used.add(id)
          return { ...v, id }
        }
        const before = cur.find((c) => c.id === v.id)
        if (!before) throw invalid(`这里没有 id 为 ${v.id} 的视图；新视图不要带 id`)
        // Keys the server does not know that the request leaves out stay.
        const kept = Object.fromEntries(Object.entries(before).filter(([k]) => !KNOWN.includes(k)))
        // A view written by hand ("@n") is given a real id by any write that names it.
        if (v.id.startsWith('@')) {
          let id: string
          do {
            id = Array.from({ length: 6 }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[Math.floor(opts.random() * 36)]).join('')
          } while (used.has(id))
          used.add(id)
          return { ...kept, ...v, id }
        }
        return { ...kept, ...v }
      })
      if (next.length === 0) books.delete(key)
      else books.set(key, next)
      return listOf(next)
    },

    // Views as a person's hand in space.yml leaves them: no usable id, so each is served as "@n", n its place from 0.
    seedHandWritten(folder: string, views: Omit<SavedView, 'id'>[]) {
      books.set('f:' + folder, views.map((v, i) => ({ filter: [], hidden: [], group: '', sort: '', ...v, id: '@' + i }) as unknown as SavedView))
    },

    // Views follow their folder, and what was at the place it moves to is stale.
    folderMoved(from: string, to: string) {
      const moving = [...books].filter(([k]) => k.startsWith('f:') && isUnder(k.slice(2), from))
      for (const [k] of [...books]) if (k.startsWith('f:') && isUnder(k.slice(2), to)) books.delete(k)
      for (const [k] of moving) books.delete(k)
      for (const [k, vs] of moving) books.set('f:' + to + k.slice(2 + from.length), vs)
    },
    folderRemoved(path: string) {
      for (const k of [...books.keys()]) if (k.startsWith('f:') && isUnder(k.slice(2), path)) books.delete(k)
    },
  }
}
