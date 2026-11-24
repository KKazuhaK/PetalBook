// The daily text source and the favourites of one space of the in-memory
// server (docs/api.md, "Daily text" and "Favourites"): the settings and their
// checks, the pick of a day, a person's favourites, and what a folder's move
// does to both. The text a pick is made from comes from the space's store.

import { HttpError, ID_RE } from './store.ts'
import { parseFilter, MAX_VIEW_FILTERS } from './propQuery.ts'
import { sha256Hex } from './sha256.ts'
import type { PageSummary } from '../types.ts'
import { canonicalTag, matchTag } from '../../lib/langTags.ts'

const invalid = (message: string) => new HttpError(400, 'invalid', message)
const isUnder = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')
const MAX_FAVORITES = 200
const BODY_LIMIT = 2000
const MAX_SKIP = 1 << 20
const MAX_LANGS = 20

export interface DailySource {
  folder?: string
  tag?: string
  type?: string
  filter: { key: string; op: string; values: unknown[] }[]
}
// One source for every language ("all"), one for each language (by), or both:
// the one for every language is the fallback.
export interface DailyConfig {
  all?: DailySource
  by: Record<string, DailySource>
}

export interface Candidate {
  summary: PageSummary
  body: string
}

export interface HomeEnv {
  now: () => number
  // The pages that meet a source, in the order they were made.
  matching: (src: DailySource) => Candidate[]
  // The folder as it is spelled here; null when there is none.
  spelledFolder: (path: string) => string | null
  hasFolder: (path: string) => boolean
  hasType: (name: string) => boolean
  hasPage: (id: string) => PageSummary | undefined
  viewName: (kind: 'folder' | 'tag', scope: string, id: string) => string | null
  guard: () => void
}

interface Fav {
  kind: 'page' | 'folder' | 'view'
  target: string
  created: number
}

export const validLang = (s: string): boolean => canonicalTag(s) !== null
const badLanguage = (tag: string) =>
  new HttpError(400, 'bad_language', `“${tag}” 不是 zh-Hans、en、pt-BR 这样的语言标签`)

// The number a day starts at: the first 8 bytes of a hash of the space and the day.
const dayNumber = (key: string, date: string): bigint => {
  const hex = sha256Hex(new TextEncoder().encode(`${key}\u0000${date}`)).slice(0, 16)
  return BigInt('0x' + hex)
}
export const pickNumber = (key: string, date: string, skip: number, total: number): number =>
  Number((dayNumber(key, date) + BigInt(skip)) % BigInt(total))

const cut = (text: string, max: number): [string, boolean] => {
  const chars = Array.from(text)
  return chars.length <= max ? [text, false] : [chars.slice(0, max).join(''), true]
}

const isSource = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

export function createHomeBook(env: HomeEnv) {
  let daily: DailyConfig | null = null
  const memo = new Map<string, string>()
  const favs = new Map<string, Fav[]>()

  const sourceFor = (c: DailyConfig, lang: string): DailySource | null => {
    const m = matchTag(lang, Object.keys(c.by))
    if (m !== null && c.by[m]) return c.by[m]
    if (c.all) return c.all
    const keys = Object.keys(c.by).sort()
    return keys.length ? c.by[keys[0]] : null
  }
  // The shape of the JSON: the source keys and the language keys side by side.
  const jsonOf = (c: DailyConfig): Record<string, unknown> => ({ ...(c.all ?? {}), ...c.by })

  const checkSource = (raw: unknown, where = ''): DailySource => {
    const at = where ? `${where}: ` : ''
    if (!isSource(raw)) throw invalid(`${at}给一个来源：folder、tag 或 type`)
    for (const k of Object.keys(raw))
      if (!['folder', 'tag', 'type', 'filter'].includes(k)) throw invalid(`${at}不认识 ${k}`)
    const given = ['folder', 'tag', 'type'].filter((k) => raw[k] !== undefined && raw[k] !== null)
    if (given.length !== 1) throw invalid(`${at}folder、tag、type 要给且只给一个`)
    const out: DailySource = { filter: [] }
    if (given[0] === 'folder') {
      if (typeof raw.folder !== 'string') throw invalid(`${at}folder 要是文字`)
      if (
        raw.folder.startsWith('/') ||
        raw.folder.endsWith('/') ||
        raw.folder.split('/').some((s) => s === '..' || s === '.')
      )
        throw invalid(`${at}文件夹路径不合法`)
      const spelled = env.spelledFolder(raw.folder)
      if (spelled === null) throw new HttpError(404, 'not_found', '找不到这个文件夹')
      out.folder = spelled
    } else if (given[0] === 'tag') {
      if (typeof raw.tag !== 'string' || raw.tag.trim() === '' || /[\0\n\r]/.test(raw.tag))
        throw invalid(`${at}这不是标签`)
      out.tag = raw.tag
    } else {
      if (typeof raw.type !== 'string' || !env.hasType(raw.type)) throw invalid(`${at}没有这个类型`)
      out.type = raw.type
    }
    if (raw.filter !== undefined && raw.filter !== null) {
      if (!Array.isArray(raw.filter) || raw.filter.length > MAX_VIEW_FILTERS)
        throw invalid(`${at}filter 最多 ${MAX_VIEW_FILTERS} 条`)
      const parsed = parseFilter(JSON.stringify(raw.filter))
      out.filter = parsed.map((f) => ({ key: f.key, op: f.op, values: f.values ?? [] }))
    }
    return out
  }

  const checkConfig = (raw: unknown): DailyConfig => {
    if (!isSource(raw) || Object.keys(raw).length === 0)
      throw invalid('给一个来源：folder、tag 或 type，或每种语言一个')
    const out: DailyConfig = { by: {} }
    const whole = Object.fromEntries(
      Object.entries(raw).filter(([k]) => ['folder', 'tag', 'type', 'filter'].includes(k)),
    )
    if (Object.keys(whole).length) out.all = checkSource(whole)
    const keys = Object.keys(raw).filter((k) => !(k in whole))
    if (keys.length > MAX_LANGS) throw invalid(`最多 ${MAX_LANGS} 种语言`)
    for (const k of keys) {
      const tag = canonicalTag(k)
      if (tag === null) throw badLanguage(k)
      if (out.by[tag]) throw invalid(`“${k}” 写了两次`)
      out.by[tag] = checkSource(raw[k], k)
    }
    return out
  }

  const dayKey = (spaceId: string, src: DailySource, date: string, skip: number) =>
    JSON.stringify([spaceId, src, date, skip])

  const mapPick = (
    src: DailySource,
    spaceId: string,
    date: string,
    skip: number,
  ): { c: Candidate; count: number } | null => {
    const cands = env.matching(src)
    if (cands.length === 0) return null
    const key = dayKey(spaceId, src, date, skip)
    const kept = memo.get(key)
    if (kept) {
      const c =
        cands.find((x) => x.summary.id === kept) ??
        (env.hasPage(kept) ? { summary: env.hasPage(kept)!, body: '' } : undefined)
      if (c) return { c, count: cands.length }
    }
    const c = cands[pickNumber(spaceId, date, skip, cands.length)]
    memo.set(key, c.summary.id)
    return { c, count: cands.length }
  }

  const validDate = (date: string): boolean => {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) return false
    const t = new Date(date + 'T00:00:00Z')
    return (
      !Number.isNaN(t.getTime()) &&
      t.toISOString().slice(0, 10) === date &&
      t.getUTCFullYear() >= 1970 &&
      t.getUTCFullYear() <= 2261
    )
  }

  const favLabel = (f: Fav) => {
    if (f.kind === 'page') {
      const p = env.hasPage(f.target)
      return p ? { name: p.label, path: p.path, missing: false } : { name: '', missing: true }
    }
    if (f.kind === 'folder')
      return { name: f.target.split('/').pop() ?? f.target, path: f.target, missing: !env.hasFolder(f.target) }
    const m = /^(folder|tag):(.*)#([^#]+)$/s.exec(f.target)
    if (!m) return { name: '', missing: true }
    const kind = m[1] as 'folder' | 'tag'
    const name = env.viewName(kind, m[2], m[3])
    return {
      name: name ?? '',
      of: m[2],
      ofKind: kind,
      missing: name === null || (kind === 'folder' && !env.hasFolder(m[2])),
    }
  }
  const favList = (userId: string) => ({
    items: (favs.get(userId) ?? []).map((f) => ({
      kind: f.kind,
      target: f.target,
      ...favLabel(f),
      created: new Date(f.created).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    })),
    limit: MAX_FAVORITES,
  })
  const checkKind = (kind: unknown): 'page' | 'folder' | 'view' => {
    if (kind !== 'page' && kind !== 'folder' && kind !== 'view') throw invalid('kind 是 page、folder 或 view')
    return kind
  }

  return {
    config: () => (daily ? structuredClone(jsonOf(daily)) : null),
    setConfig(body: Record<string, unknown>) {
      env.guard()
      if (!('daily' in body) || Object.keys(body).length !== 1)
        throw invalid('给 daily：每日文案的来源，或 null 来去掉它')
      daily = body.daily === null ? null : checkConfig(body.daily)
      return { daily: this.config() }
    },
    // A source that is a source for lang, or null.
    sourceOf: (lang: string): DailySource | null => (daily ? sourceFor(daily, lang) : null),
    hasOwn: () => daily !== null,
    pick(spaceId: string, q: URLSearchParams, src: DailySource): Record<string, unknown> | null {
      const date = q.get('date') ?? new Date(env.now()).toISOString().slice(0, 10)
      if (!validDate(date)) throw invalid('date 要写成 2026-11-23 这样的日期')
      const rawSkip = q.get('skip')
      let skip = 0
      if (rawSkip !== null && rawSkip !== '') {
        if (!/^\d+$/.test(rawSkip)) throw invalid('skip 要是 0 以上的数')
        if (Number(rawSkip) > MAX_SKIP) throw new HttpError(400, 'skip_too_large', `skip 最大是 ${MAX_SKIP}`)
        skip = Number(rawSkip)
      }
      const got = mapPick(src, spaceId, date, skip)
      if (!got) return null
      const [body, truncated] = cut(got.c.body, BODY_LIMIT)
      return { page: { ...got.c.summary, body, bodyTruncated: truncated }, count: got.count, origin: 'space' }
    },
    // The checks of date, skip and lang, whichever source there is.
    checkQuery(q: URLSearchParams): { date: string; skip: number; lang: string } {
      const date = q.get('date') ?? new Date(env.now()).toISOString().slice(0, 10)
      if (!validDate(date)) throw invalid('date 要写成 2026-11-23 这样的日期')
      const rawSkip = q.get('skip')
      let skip = 0
      if (rawSkip !== null && rawSkip !== '') {
        if (!/^\d+$/.test(rawSkip)) throw invalid('skip 要是 0 以上的数')
        if (Number(rawSkip) > MAX_SKIP) throw new HttpError(400, 'skip_too_large', `skip 最大是 ${MAX_SKIP}`)
        skip = Number(rawSkip)
      }
      const rawLang = q.get('lang') ?? ''
      const lang = rawLang === '' ? '' : (canonicalTag(rawLang) ?? '')
      if (rawLang !== '' && lang === '') throw badLanguage(rawLang)
      return { date, skip, lang }
    },

    favorites: (userId: string) => favList(userId),
    addFavorite(userId: string, body: Record<string, unknown>) {
      const kind = checkKind(body.kind)
      let target = body.target
      if (typeof target !== 'string' || target === '') throw invalid('缺少 target')
      if (kind === 'page') {
        if (!ID_RE.test(target)) throw invalid('这不是页面 id')
        if (!env.hasPage(target)) throw new HttpError(404, 'not_found', '找不到这个页面')
      } else if (kind === 'folder') {
        const s = env.spelledFolder(target)
        if (s === null) throw new HttpError(404, 'not_found', '找不到这个文件夹')
        target = s
      } else {
        const m = /^(folder|tag):(.*)#([^#]+)$/s.exec(target)
        if (!m) throw invalid('视图是 folder:<路径>#<id> 或 tag:<标签>#<id>')
        let scope = m[2]
        if (m[1] === 'folder') {
          const s = env.spelledFolder(scope)
          if (s === null) throw new HttpError(404, 'not_found', '找不到这个文件夹')
          scope = s
        }
        if (env.viewName(m[1] as 'folder' | 'tag', scope, m[3]) === null)
          throw new HttpError(404, 'not_found', '找不到这个视图')
        target = `${m[1]}:${scope}#${m[3]}`
      }
      const list = favs.get(userId) ?? []
      if (!list.some((f) => f.kind === kind && f.target === target)) {
        if (list.length >= MAX_FAVORITES) throw invalid(`每个空间最多 ${MAX_FAVORITES} 个收藏`)
        list.push({ kind, target: target as string, created: env.now() })
        favs.set(userId, list)
      }
      return favList(userId)
    },
    removeFavorite(userId: string, kind: string | null, target: string | null) {
      if ((kind !== 'page' && kind !== 'folder' && kind !== 'view') || !target)
        throw invalid('用 kind 和 target 说明是哪一个收藏')
      favs.set(
        userId,
        (favs.get(userId) ?? []).filter((f) => !(f.kind === kind && f.target === target)),
      )
      return favList(userId)
    },
    orderFavorites(userId: string, body: Record<string, unknown>) {
      if (!Array.isArray(body.items)) throw invalid('items 是收藏按新顺序排的列表')
      if (body.items.length > MAX_FAVORITES) throw invalid(`最多 ${MAX_FAVORITES} 个收藏`)
      const cur = favs.get(userId) ?? []
      const named: Fav[] = []
      for (const it of body.items) {
        if (!isSource(it)) throw invalid('每一项是 {kind, target}')
        const kind = checkKind(it.kind)
        const f = cur.find((x) => x.kind === kind && x.target === it.target)
        if (f && !named.includes(f)) named.push(f)
      }
      favs.set(userId, [...named, ...cur.filter((f) => !named.includes(f))])
      return favList(userId)
    },

    // A folder moved: what names it follows it.
    folderMoved(from: string, to: string) {
      const rename = (p: string) => (isUnder(p, from) ? to + p.slice(from.length) : null)
      const moveSource = (s: DailySource) => {
        const n = s.folder !== undefined ? rename(s.folder) : null
        if (n !== null) s.folder = n
      }
      if (daily) {
        if (daily.all) moveSource(daily.all)
        for (const src of Object.values(daily.by)) moveSource(src)
      }
      for (const list of favs.values()) {
        const gone = new Set<Fav>()
        for (const f of list) {
          if (f.kind === 'folder') {
            const n = rename(f.target)
            if (n !== null) f.target = n
            else if (isUnder(f.target, to)) gone.add(f)
          } else if (f.kind === 'view') {
            const m = /^folder:(.*)#([^#]+)$/s.exec(f.target)
            if (!m) continue
            const n = rename(m[1])
            if (n !== null) f.target = `folder:${n}#${m[2]}`
            else if (isUnder(m[1], to)) gone.add(f)
          }
        }
        for (let i = list.length - 1; i >= 0; i--) if (gone.has(list[i])) list.splice(i, 1)
      }
    },
  }
}

export type HomeBook = ReturnType<typeof createHomeBook>
