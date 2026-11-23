// The templates of one space of the in-memory server (docs/api.md,
// "Templates"): the template pages, the default templates of folders and of
// quick notes, the checks of a save, what a folder's move or removal does to
// the defaults, and filling a new page from a template.

import { HttpError, isRecord } from './store.ts'
import { gitBlobId } from './sha1.ts'
import type { PageMeta } from '../types.ts'

const MAX_TEMPLATES = 200
const MAX_FILE = 256 << 10
const MAX_NAME = 80
const RESERVED = /^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$/i

const bytes = (s: string) => new TextEncoder().encode(s).length
const invalid = (message: string, code = 'invalid') => new HttpError(400, code, message)
const isUnder = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')

interface Stored {
  name: string
  meta: PageMeta
  body: string
  rev: string
}
interface Defaults {
  folders: Record<string, string>
  quickNote: string
}

// The file as it would sit on disk: only its size and rev matter here.
const fileOf = (meta: PageMeta, body: string) => {
  const lines = Object.entries(meta).map(([k, v]) => `${k}: ${JSON.stringify(v)}`)
  return lines.length ? `---\n${lines.join('\n')}\n---\n${body}` : body
}

// A page name's rules, as the server applies them to a template's.
const validName = (name: string): boolean =>
  name !== '' &&
  name !== 'defaults' &&
  name === name.normalize('NFC') &&
  Array.from(name).length <= MAX_NAME &&
  !/[<>:"/\\|?*\p{Cc}]/u.test(name) &&
  !name.startsWith('.') &&
  !/[.\s]$/.test(name) &&
  name === name.trim() &&
  !RESERVED.test(name)

// {{date}}, {{time}} and {{datetime}} in the time zone given (minutes east of UTC); nothing else.
export const fillVariables = (text: string, nowMs: number, tz: number): string => {
  if (!text.includes('{{')) return text
  const iso = new Date(nowMs + tz * 60_000).toISOString()
  const date = iso.slice(0, 10)
  const time = iso.slice(11, 16)
  return text.replaceAll('{{datetime}}', `${date} ${time}`).replaceAll('{{date}}', date).replaceAll('{{time}}', time)
}

const fillValue = (v: unknown, fill: (s: string) => string): unknown => {
  if (typeof v === 'string') return fill(v)
  if (Array.isArray(v)) return v.map((e) => fillValue(e, fill))
  if (isRecord(v)) return Object.fromEntries(Object.entries(v).map(([k, e]) => [k, fillValue(e, fill)]))
  return v
}

// Whether a header value says nothing: null, blank text, an empty list.
const empty = (v: unknown) => v === null || v === undefined || (typeof v === 'string' && v.trim() === '') || (Array.isArray(v) && v.length === 0)

export function createTemplateBook(opts: { now: () => number; spelledFolder: (path: string) => string | null; validFolder: (path: string) => boolean }) {
  const templates = new Map<string, Stored>()
  let defaults: Defaults = { folders: {}, quickNote: '' }

  const view = (t: Stored, withBody: boolean) => ({ name: t.name, path: `.petalbook/templates/${t.name}.md`, rev: t.rev, meta: structuredClone(t.meta), ...(withBody ? { body: t.body } : {}) })
  const defaultsView = () => ({ folders: { ...defaults.folders }, quickNote: defaults.quickNote })
  const conflict = (cur: Stored | undefined) => new HttpError(409, 'conflict', '模板已被修改', cur ? { current: view(cur, true) } : undefined)
  const notFound = (name: string) => invalid(`没有叫 ${name} 的模板`, 'template_not_found')

  return {
    list: () => ({ templates: [...templates.values()].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)).map((t) => view(t, false)), defaults: defaultsView() }),
    get: (name: string) => {
      const t = templates.get(name)
      if (!t) throw new HttpError(404, 'not_found', '找不到这个模板')
      return view(t, true)
    },
    put: (name: string, input: Record<string, unknown>) => {
      if (!validName(name)) throw invalid('模板名不合法')
      const baseRev = input.baseRev === undefined ? '' : input.baseRev
      if (typeof baseRev !== 'string') throw invalid('baseRev 要是字符串')
      const meta = input.meta === undefined ? {} : input.meta
      if (!isRecord(meta)) throw invalid('meta 要是对象')
      const body = input.body === undefined ? '' : input.body
      if (typeof body !== 'string') throw invalid('body 必须是字符串')
      const file = fileOf(meta as PageMeta, body)
      if (bytes(file) > MAX_FILE) throw invalid('模板最大 256 KiB', 'template_too_large')
      const cur = templates.get(name)
      if ([...templates.keys()].some((n) => n !== name && n.toLowerCase() === name.toLowerCase())) throw new HttpError(409, 'exists', '已经有这个名字')
      if ((cur?.rev ?? '') !== baseRev || (!cur && baseRev !== '')) throw conflict(cur)
      if (!cur && templates.size >= MAX_TEMPLATES) throw invalid('一个空间最多 200 个模板', 'template_limit')
      const rev = gitBlobId(file)
      if (cur && cur.rev === rev) return view(cur, true)
      const next: Stored = { name, meta: structuredClone(meta) as PageMeta, body, rev }
      templates.set(name, next)
      return view(next, true)
    },
    remove: (name: string, baseRev: string) => {
      const cur = templates.get(name)
      const named = defaults.quickNote === name || Object.values(defaults.folders).includes(name)
      if (!cur && !named) throw new HttpError(404, 'not_found', '找不到这个模板')
      if ((cur?.rev ?? '') !== baseRev) throw conflict(cur)
      templates.delete(name)
      defaults = { folders: Object.fromEntries(Object.entries(defaults.folders).filter(([, t]) => t !== name)), quickNote: defaults.quickNote === name ? '' : defaults.quickNote }
    },
    setDefault: (body: Record<string, unknown>) => {
      const hasFolder = body.folder !== undefined
      const hasQuick = body.quickNote !== undefined
      if (hasFolder === hasQuick || (hasFolder && typeof body.template !== 'string') || (hasQuick && (body.template !== undefined || typeof body.quickNote !== 'string')) || (hasFolder && typeof body.folder !== 'string')) {
        throw invalid('给 {"folder", "template"} 或 {"quickNote"}')
      }
      const name = (hasFolder ? body.template : body.quickNote) as string
      if (name !== '' && !templates.has(name)) throw notFound(name)
      if (hasQuick) {
        defaults = { ...defaults, quickNote: name }
        return defaultsView()
      }
      const raw = (body.folder as string).replace(/^\/+|\/+$/g, '')
      if (raw === '' || !opts.validFolder(raw)) throw invalid('文件夹路径不合法')
      const folder = opts.spelledFolder(raw)
      if (folder === null && name !== '') throw new HttpError(404, 'not_found', '找不到这个文件夹')
      const key = folder ?? raw
      const folders = { ...defaults.folders }
      if (name === '') delete folders[key]
      else folders[key] = name
      defaults = { ...defaults, folders }
      return defaultsView()
    },
    // The folder's defaults follow it; what was at the place it moves to is stale.
    folderMoved(from: string, to: string) {
      const next: Record<string, string> = {}
      const moving: [string, string][] = []
      for (const [k, t] of Object.entries(defaults.folders)) {
        if (isUnder(k, from)) moving.push([to + k.slice(from.length), t])
        else if (!isUnder(k, to)) next[k] = t
      }
      for (const [k, t] of moving) next[k] = t
      defaults = { ...defaults, folders: next }
    },
    folderRemoved(path: string) {
      defaults = { ...defaults, folders: Object.fromEntries(Object.entries(defaults.folders).filter(([k]) => !isUnder(k, path))) }
    },
    // The header and body a new page starts from (docs/api.md, "Creating a page
    // from a template"). `quick` says the page is made in the inbox.
    apply(input: { folder: string; template: unknown; tz: unknown; meta: PageMeta; body: unknown }, quick: boolean): { meta: PageMeta; body: unknown } {
      let tz = 0
      if (input.tz !== undefined && input.tz !== null) {
        if (typeof input.tz !== 'number' || !Number.isInteger(input.tz) || input.tz < -840 || input.tz > 840) throw invalid('tz 是东偏 UTC 的分钟数，-840 到 840')
        tz = input.tz
      }
      if (input.template !== undefined && input.template !== null && typeof input.template !== 'string') throw invalid('template 要是字符串')
      let pick = input.template as string | undefined | null
      const explicit = typeof pick === 'string'
      if (!explicit) pick = (quick ? defaults.quickNote : '') || defaults.folders[opts.spelledFolder(input.folder) ?? input.folder] || ''
      if (!pick) return { meta: input.meta, body: input.body }
      const t = templates.get(pick)
      if (!t) {
        if (explicit) throw notFound(pick)
        return { meta: input.meta, body: input.body }
      }
      const fill = (s: string) => fillVariables(s, opts.now(), tz)
      const meta: PageMeta = {}
      for (const [k, v] of Object.entries(t.meta)) {
        if (k === 'id' || k === 'created') continue
        const filled = fillValue(v, fill)
        // The request's field wins, unless it says nothing; the template's type is kept.
        meta[k] = k !== 'type' && k in input.meta && !empty(input.meta[k]) ? input.meta[k] : filled
        if (k === 'type' && empty(filled) && k in input.meta) meta[k] = input.meta[k]
      }
      for (const [k, v] of Object.entries(input.meta)) if (!(k in t.meta)) meta[k] = v
      const given = typeof input.body === 'string' ? input.body : ''
      return { meta, body: given !== '' ? given : fill(t.body) }
    },
  }
}
