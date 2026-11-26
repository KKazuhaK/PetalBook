// Spaces, pages, folders, tags, types, templates and favourites: lists, gets, creates, updates and deletes on the
// space's collections. A page's revision is a hash of what it holds, so the same content is the same rev; a write
// that names another rev than the page has is the one conflict the editor's save controller needs (see drafts.ts).

import { excerptOf } from '../../../lib/excerpt.ts'
import { folderTypeName, partialPropsOf, propsOfMeta } from '../../../lib/pageTypes.ts'
import type {
  Favorite,
  FieldKind,
  Page,
  PageMeta,
  PageSummary,
  PageType,
  PageTemplate,
  SpaceInfo,
  TypeField,
} from '../../types.ts'
import type { DraftRec, SpaceData, StoredPage } from '../data/model.ts'
import { add, find, hash, newId, paged, Refusal, remove, reply, rfc3339, type Ctx, type Route } from '../kit.ts'
import { matches, sortValue } from './query.ts'

export const spaceOf = (c: Ctx): SpaceData =>
  c.params.space === 'default'
    ? c.data.spaces[0]
    : find([...c.data.spaces, ...c.data.admin.otherSpaces], (s) => s.info.id === c.params.space, 'the space')
export const pageOf = (s: SpaceData, id: string): StoredPage => find(s.pages, (p) => p.id === id, 'the page')

const str = (m: PageMeta, key: string): string | undefined =>
  typeof m[key] === 'string' && m[key] !== '' ? (m[key] as string) : undefined
export const listOf = (v: unknown): string[] =>
  (typeof v === 'string' ? [v] : Array.isArray(v) ? v : [])
    .filter((x): x is string => typeof x === 'string' && x.trim() !== '')
    .map((x) => x.trim())
const plain = (md: string) =>
  md
    .replace(/[#>*_`~|-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
const labelOf = (p: StoredPage) =>
  str(p.meta, 'title') ??
  Array.from(plain(p.body.split('\n').find((l) => plain(l)) ?? ''))
    .slice(0, 30)
    .join('')

export const pathOf = (p: StoredPage) => (p.folder ? `${p.folder}/${p.file}` : p.file)
export const revOf = (p: Pick<StoredPage, 'meta' | 'body'>) => hash(JSON.stringify([p.meta, p.body]))
export const typeOf = (s: SpaceData, p: StoredPage) =>
  str(p.meta, 'type')?.trim() || folderTypeName(s.bindings, p.folder)
const ancestors = (folder: string) => {
  const out: string[] = []
  for (let f = folder; f !== ''; f = f.includes('/') ? f.slice(0, f.lastIndexOf('/')) : '') out.unshift(f)
  return out
}
export const addFolder = (s: SpaceData, folder: string) => {
  for (const f of ancestors(folder)) if (!s.folders.includes(f)) s.folders.push(f)
}
const under = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')

// The file name a page has in its folder: made from the title, numbered when another page has it.
export function fileFor(s: SpaceData, folder: string, meta: PageMeta, id: string, now: number): string {
  const title = str(meta, 'title')
  const created = str(meta, 'created') ?? new Date(now).toISOString().slice(0, 10)
  const base =
    title
      ?.replace(/[\\/:*?"<>|]/g, ' ')
      .replace(/\s+/g, ' ')
      .trim() || `${created}-${id.slice(-4).toLowerCase()}`
  let file = `${base}.md`
  for (let n = 2; s.pages.some((p) => p.id !== id && p.folder === folder && p.file === file); n++)
    file = `${base} ${n}.md`
  return file
}

// --- how a page is shown ------------------------------------------------------------------------------------------

export function pageView(s: SpaceData, p: StoredPage): Page {
  const partial = partialPropsOf(p.meta)
  return {
    id: p.id,
    path: pathOf(p),
    folder: p.folder,
    rev: revOf(p),
    updated: rfc3339(p.updated),
    meta: structuredClone(p.meta),
    header: Object.entries(p.meta)
      .map(([k, v]) => `${k}: ${JSON.stringify(v)}\n`)
      .join(''),
    body: p.body,
    effectiveType: typeOf(s, p),
    ...(partial.length ? { partialProps: partial } : {}),
  }
}

// The draft the signed-in person has on a page, as GET gives it.
export const draftView = (s: SpaceData, p: StoredPage) => {
  const d = s.drafts[p.id]
  return d
    ? {
        version: d.version,
        baseRev: d.baseRev,
        meta: structuredClone(d.meta),
        body: d.body,
        updated: rfc3339(d.updated),
        stale: d.baseRev !== revOf(p),
      }
    : null
}

export function summary(s: SpaceData, p: StoredPage, withProps = false): PageSummary {
  const partial = partialPropsOf(p.meta)
  const tags = listOf(p.meta.tags)
  return {
    id: p.id,
    path: pathOf(p),
    folder: p.folder,
    ...(str(p.meta, 'title') ? { title: str(p.meta, 'title') } : {}),
    ...(str(p.meta, 'subtitle') ? { subtitle: str(p.meta, 'subtitle') } : {}),
    source: listOf(p.meta.source),
    ...(str(p.meta, 'icon') ? { icon: str(p.meta, 'icon') } : {}),
    ...(str(p.meta, 'color') ? { color: str(p.meta, 'color') } : {}),
    label: labelOf(p),
    untitled: !str(p.meta, 'title'),
    tags,
    type: str(p.meta, 'type') ?? '',
    effectiveType: typeOf(s, p),
    ...(str(p.meta, 'created') ? { created: str(p.meta, 'created') } : {}),
    updated: rfc3339(p.updated),
    excerpt: excerptOf(p.body),
    ...(withProps
      ? { props: propsOfMeta(p.meta), rev: revOf(p), ...(partial.length ? { partialProps: partial } : {}) }
      : {}),
  } as PageSummary
}

// The header a write keeps: the id and the creation date are the page's, whatever the caller sent.
export const keepIdentity = (old: PageMeta, given: PageMeta): PageMeta => {
  const out = { ...given }
  if (old.id !== undefined) out.id = old.id
  if (old.created !== undefined && out.created === undefined) out.created = old.created
  return out
}

// Writes the given content to a page; the file name follows the title. Returns the page.
export function writePage(now: number, s: SpaceData, p: StoredPage, meta: PageMeta, body: string): StoredPage {
  const next = keepIdentity(p.meta, meta)
  if (JSON.stringify([next, body]) === JSON.stringify([p.meta, p.body])) return p
  p.meta = next
  p.body = body
  p.file = fileFor(s, p.folder, next, p.id, now)
  p.updated = now
  return p
}

// Whether a draft says what the page says: the header as a write would merge it, and the body.
export const sameParsed = (p: StoredPage, d: DraftRec) =>
  d.body === p.body && JSON.stringify(sortKeys(keepIdentity(p.meta, d.meta))) === JSON.stringify(sortKeys(p.meta))
const sortKeys = (m: PageMeta) => Object.fromEntries(Object.entries(m).sort(([a], [b]) => (a < b ? -1 : 1)))

// A save never takes a draft away (its text may exist nowhere else), except one that says exactly what was saved.
const dropSame = (s: SpaceData, p: StoredPage) => {
  const d = s.drafts[p.id]
  if (d && sameParsed(p, d)) delete s.drafts[p.id]
}

// --- lists ----------------------------------------------------------------------------------------------------------

function select(s: SpaceData, q: URLSearchParams): StoredPage[] {
  const folder = q.get('folder')
  const recursive = q.get('recursive') === 'true'
  const tags = q.getAll('tag')
  const text = (q.get('q') ?? '').trim().toLowerCase()
  const filters = q.get('filter') ? JSON.parse(q.get('filter')!) : []
  const day = (ms: number) => rfc3339(ms).slice(0, 10)
  let items = s.pages.filter((p) => {
    if (folder !== null && !(recursive ? folder === '' || under(p.folder, folder) : p.folder === folder)) return false
    if (!tags.every((t) => listOf(p.meta.tags).includes(t))) return false
    const [uf, ut, cf, ct] = ['updatedFrom', 'updatedTo', 'createdFrom', 'createdTo'].map((k) => q.get(k))
    if ((uf && day(p.updated) < uf) || (ut && day(p.updated) > ut)) return false
    const created = str(p.meta, 'created')
    if ((cf || ct) && (!created || (cf && created < cf) || (ct && created > ct))) return false
    if (text) {
      const fields =
        q.get('in') === 'title'
          ? [str(p.meta, 'title') ?? '']
          : [str(p.meta, 'title') ?? '', str(p.meta, 'subtitle') ?? '', p.body]
      if (!text.split(/\s+/).every((w) => fields.some((f) => f.toLowerCase().includes(w)))) return false
    }
    return filters.every((f: Parameters<typeof matches>[1]) => matches(p.meta, f))
  })
  const sort = q.get('sort') ?? '-updated'
  const desc = sort.startsWith('-')
  const key = desc ? sort.slice(1) : sort
  const dir = desc ? -1 : 1
  const by = (p: StoredPage): string | number | undefined =>
    key === 'updated'
      ? p.updated
      : key === 'created'
        ? (str(p.meta, 'created') ?? '')
        : key === 'title'
          ? (str(p.meta, 'title') ?? undefined)?.toLowerCase()
          : sortValue(p.meta, key.replace(/^prop:/, ''))
  items = [...items].sort((a, b) => {
    const x = by(a)
    const y = by(b)
    // A page without the value goes last, whichever way it sorts.
    if (x === undefined || y === undefined) return x === y ? 0 : x === undefined ? 1 : -1
    return dir * (x < y ? -1 : x > y ? 1 : 0)
  })
  return items
}

// --- routes ---------------------------------------------------------------------------------------------------------

const newSpace = (c: Ctx, name: string): SpaceInfo => ({
  id: newId(c.data, '01FS'),
  name,
  role: 'owner',
  permissions: ['read', 'write', 'members', 'manage'],
  personal: false,
  sync: 'local',
  inbox: 'Inbox',
  directRole: 'owner',
  viaGroups: false,
  disabled: false,
})

const schemaOf = (f: { title?: string; kind: FieldKind; options?: string[] }) => {
  const title = f.title ? { title: f.title } : {}
  switch (f.kind) {
    case 'select':
      return { ...title, type: 'string', enum: f.options ?? [] }
    case 'multiselect':
      return { ...title, type: 'array', items: { type: 'string', enum: f.options ?? [] }, uniqueItems: true }
    case 'date':
      return { ...title, type: 'string', format: 'date' }
    case 'number':
      return { ...title, type: 'number' }
    case 'checkbox':
      return { ...title, type: 'boolean' }
    case 'url':
      return { ...title, type: 'string', format: 'uri' }
    default:
      return { ...title, type: 'string' }
  }
}
export const typeView = (name: string, title: string, fields: TypeField[]): PageType => ({
  name,
  title,
  fields,
  rev: hash(JSON.stringify([title, fields.map((f) => [f.key, f.title, f.kind, f.options ?? null])])),
})

const templateView = (name: string, meta: PageMeta, body: string): PageTemplate => ({
  name,
  path: `.petalbook/templates/${name}.md`,
  rev: hash(JSON.stringify([meta, body])),
  meta,
  body,
})

// A favourite as it is now: the name and the path of what it points at, and whether that is still there.
function favoriteView(s: SpaceData, f: Favorite): Favorite {
  if (f.kind === 'page') {
    const p = s.pages.find((x) => x.id === f.target)
    return p ? { ...f, name: labelOf(p), path: pathOf(p), missing: false } : { ...f, name: '', missing: true }
  }
  if (f.kind === 'folder')
    return { ...f, name: f.target.split('/').pop() ?? f.target, path: f.target, missing: !s.folders.includes(f.target) }
  const m = /^(folder|tag):(.*)#([^#]+)$/s.exec(f.target)
  const view = m ? s.views[`${m[1]}=${m[2]}`]?.views.find((v) => v.id === m[3]) : undefined
  if (!m) return { ...f, name: '', missing: true }
  return {
    ...f,
    name: view?.name ?? '',
    of: m[2],
    ofKind: m[1] as 'folder' | 'tag',
    missing: !view || (m[1] === 'folder' && !s.folders.includes(m[2])),
  }
}
const favoriteList = (s: SpaceData) => ({ items: s.favorites.map((f) => favoriteView(s, f)), limit: 50 })

export const fileRoutes: Route[] = [
  // The spaces the person is in (an administrator reaches the others through the administration).
  ['GET', '/api/spaces', (c) => ({ spaces: c.data.spaces.map((s) => s.info).filter((i) => i.role !== '') })],
  [
    'POST',
    '/api/spaces',
    (c) => {
      const s = {
        ...structuredClone(c.data.spaces[0]),
        pages: [],
        folders: [],
        favorites: [],
        views: {},
        drafts: {},
        checks: [],
      }
      s.info = newSpace(c, c.body.name)
      c.data.spaces.push(s)
      return reply(201, s.info)
    },
  ],
  [
    'PATCH',
    '/api/spaces/:space',
    (c) => {
      const s = spaceOf(c)
      Object.assign(s.info, c.body)
      return { ...s.info, storage: { assets: 'local', cache: 'local', remote: null } }
    },
  ],

  // pages
  [
    'GET',
    '/api/spaces/:space/pages',
    (c) => {
      const s = spaceOf(c)
      const withBody = c.query.get('body') === '1'
      const page = paged(select(s, c.query), c.query, withBody ? 20 : 50)
      return {
        items: page.items.map((p) => ({
          ...summary(s, p, c.query.get('props') === '1'),
          ...(withBody ? { body: p.body } : {}),
        })),
        next: page.next,
      }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/pages/groups',
    (c) => {
      const s = spaceOf(c)
      const key = c.query.get('key') ?? ''
      const counts = new Map<string, number>()
      let empty = 0
      for (const p of select(s, c.query)) {
        const v = listOf(p.meta[key])
        if (!v.length) empty++
        for (const x of v) counts.set(x, (counts.get(x) ?? 0) + 1)
      }
      return { groups: [...counts].map(([value, count]) => ({ value, count })), empty, truncated: false }
    },
  ],
  [
    'POST',
    '/api/spaces/:space/pages',
    (c) => {
      const s = spaceOf(c)
      const { folder = '', template, meta: given = {}, body = '' } = c.body
      const from = typeof template === 'string' ? s.templates.find((t) => t.name === template) : undefined
      const id = newId(c.data)
      const meta: PageMeta = { id, ...(from?.meta ?? {}), ...given }
      if (meta.created === undefined) meta.created = new Date(c.now()).toISOString().slice(0, 10)
      const p: StoredPage = {
        id,
        folder,
        file: fileFor(s, folder, meta, id, c.now()),
        meta,
        body: body === '' && from ? (from.body ?? '') : body,
        updated: c.now(),
      }
      add(s.pages, p)
      addFolder(s, folder)
      return reply(201, pageView(s, p))
    },
  ],
  [
    'POST',
    '/api/spaces/:space/pages/batch',
    (c) => {
      const s = spaceOf(c)
      const ids: string[] = c.body.ids
      for (const id of ids) {
        const p = pageOf(s, id)
        if (c.body.op === 'delete') remove(s.pages, p)
        else {
          p.folder = c.body.folder
          p.file = fileFor(s, p.folder, p.meta, p.id, c.now())
          addFolder(s, p.folder)
        }
      }
      return { pages: ids.length }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/pages/:id',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      const draft = draftView(s, p)
      return { ...pageView(s, p), ...(draft ? { draft } : {}) }
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/pages/:id',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      if (c.body.baseRev !== revOf(p))
        throw new Refusal(409, 'conflict', 'the page was changed', { current: pageView(s, p) })
      writePage(c.now(), s, p, c.body.meta, c.body.body)
      dropSame(s, p)
      return { ...pageView(s, p), textRev: revOf(p) }
    },
  ],
  [
    'PATCH',
    '/api/spaces/:space/pages/:id/props',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      if (c.body.baseRev !== revOf(p))
        throw new Refusal(409, 'conflict', 'the page was changed', { current: pageView(s, p) })
      const meta = { ...p.meta, ...(c.body.set ?? {}) }
      for (const k of c.body.unset ?? []) delete meta[k]
      writePage(c.now(), s, p, meta, p.body)
      dropSame(s, p)
      return pageView(s, p)
    },
  ],
  [
    'POST',
    '/api/spaces/:space/pages/:id/move',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      p.folder = c.body.folder
      p.file = fileFor(s, p.folder, p.meta, p.id, c.now())
      p.updated = c.now()
      addFolder(s, p.folder)
      return pageView(s, p)
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/pages/:id',
    (c) => {
      const s = spaceOf(c)
      const p = pageOf(s, c.params.id)
      remove(s.pages, p)
      delete s.drafts[p.id]
    },
  ],
  [
    'POST',
    '/api/spaces/:space/pages/:id/assets',
    (c) => {
      const name = c.query.get('name') ?? 'file'
      return reply(201, { path: `assets/${name}`, name, size: c.raw?.length ?? 0 })
    },
  ],

  // folders, tags, appearance
  [
    'GET',
    '/api/spaces/:space/tree',
    (c) => {
      const s = spaceOf(c)
      const newest = (f: string) =>
        Math.max(0, ...s.pages.filter((p) => f === '' || under(p.folder, f)).map((p) => p.updated))
      return {
        folders: [...s.folders]
          .sort((a, b) => a.localeCompare(b, 'zh'))
          .map((path) => ({
            path,
            pages: s.pages.filter((p) => p.folder === path).length,
            ...(newest(path) ? { updated: rfc3339(newest(path)) } : {}),
          })),
      }
    },
  ],
  [
    'POST',
    '/api/spaces/:space/folders',
    (c) => {
      addFolder(spaceOf(c), c.body.path)
      return reply(201, { path: c.body.path })
    },
  ],
  [
    'POST',
    '/api/spaces/:space/folders/move',
    (c) => {
      const s = spaceOf(c)
      const { from, to } = c.body
      const rename = (f: string) => to + f.slice(from.length)
      s.folders = s.folders.map((f) => (under(f, from) ? rename(f) : f))
      for (const p of s.pages) if (under(p.folder, from)) p.folder = rename(p.folder)
      addFolder(s, to)
      return { path: to }
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/folders',
    (c) => {
      const s = spaceOf(c)
      const path = c.query.get('path') ?? ''
      s.pages = s.pages.filter((p) => !under(p.folder, path))
      s.folders = s.folders.filter((f) => !under(f, path))
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/folders/type',
    (c) => {
      const s = spaceOf(c)
      const { folder, type } = c.body
      s.bindings = s.bindings.filter((b) => b.folder !== folder)
      if (type) s.bindings.push({ folder, type })
      return { folder, type }
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/folders/style',
    (c) => {
      const { folder, icon, color } = c.body
      spaceOf(c).folderStyles[folder] = { icon, color }
      return { folder, icon, ...(color ? { color } : {}) }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/appearance',
    (c) => {
      const s = spaceOf(c)
      return {
        folders: Object.entries(s.folderStyles).map(([folder, v]) => ({
          folder,
          icon: v.icon,
          ...(v.color ? { color: v.color } : {}),
        })),
        iconColors: ['gray', 'red', 'orange', 'amber', 'green', 'teal', 'blue', 'purple', 'pink', 'rose'],
        sourcePresets: s.sourcePresets,
      }
    },
  ],
  ['PUT', '/api/spaces/:space/sources/presets', (c) => ({ presets: (spaceOf(c).sourcePresets = c.body.presets) })],
  [
    'GET',
    '/api/spaces/:space/sources',
    (c) => {
      const counts = new Map<string, number>()
      for (const p of spaceOf(c).pages) for (const x of listOf(p.meta.source)) counts.set(x, (counts.get(x) ?? 0) + 1)
      return { sources: [...counts].map(([source, pages]) => ({ source, pages })) }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/tags',
    (c) => {
      const s = spaceOf(c)
      const counts = new Map<string, number>()
      for (const p of s.pages) for (const t of new Set(listOf(p.meta.tags))) counts.set(t, (counts.get(t) ?? 0) + 1)
      return {
        tags: [...counts]
          .map(([tag, pages]) => ({ tag, pages, ...(s.tagColors[tag] ? { color: s.tagColors[tag] } : {}) }))
          .sort((a, b) => b.pages - a.pages || a.tag.localeCompare(b.tag, 'zh')),
      }
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/tags/:tag',
    (c) => {
      const s = spaceOf(c)
      if (c.body.color === null) delete s.tagColors[c.params.tag]
      else s.tagColors[c.params.tag] = c.body.color
      return { tag: c.params.tag, ...(c.body.color ? { color: c.body.color } : {}) }
    },
  ],

  // types
  [
    'GET',
    '/api/spaces/:space/types',
    (c) => {
      const s = spaceOf(c)
      const notices = s.info.permissions.includes('manage') && s.settingsNotices.length > 0
      return {
        types: [...s.types].sort((a, b) => (a.name < b.name ? -1 : 1)),
        bindings: s.bindings,
        ...(notices ? { settingsNotices: s.settingsNotices } : {}),
      }
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/types/:name',
    (c) => {
      const s = spaceOf(c)
      const fields: TypeField[] = (c.body.fields ?? []).map(
        (f: { key: string; title?: string; kind?: FieldKind; options?: string[] }) => ({
          key: f.key,
          title: f.title ?? '',
          kind: f.kind ?? 'text',
          ...(f.options ? { options: f.options } : {}),
          schema: schemaOf({ ...f, kind: f.kind ?? 'text' }),
        }),
      )
      const type = typeView(c.params.name, c.body.title ?? '', fields)
      const at = s.types.findIndex((t) => t.name === type.name)
      if (at >= 0) s.types[at] = type
      else s.types.push(type)
      return type
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/types/:name',
    (c) => {
      const s = spaceOf(c)
      s.types = s.types.filter((t) => t.name !== c.params.name)
      s.bindings = s.bindings.filter((b) => b.type !== c.params.name)
    },
  ],

  // templates
  [
    'GET',
    '/api/spaces/:space/templates',
    (c) => ({
      templates: [...spaceOf(c).templates]
        .sort((a, b) => (a.name < b.name ? -1 : 1))
        .map(({ body: _body, ...rest }) => (void _body, rest)),
      defaults: spaceOf(c).defaults,
    }),
  ],
  [
    'PUT',
    '/api/spaces/:space/templates/defaults',
    (c) => {
      const d = spaceOf(c).defaults
      if (c.body.quickNote !== undefined) d.quickNote = c.body.quickNote
      if (c.body.folder !== undefined) {
        if (c.body.template) d.folders[c.body.folder] = c.body.template
        else delete d.folders[c.body.folder]
      }
      return d
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/templates/:name',
    (c) => {
      const s = spaceOf(c)
      const t = templateView(c.params.name, c.body.meta, c.body.body)
      const at = s.templates.findIndex((x) => x.name === t.name)
      if (at >= 0) s.templates[at] = t
      else s.templates.push(t)
      return t
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/templates/:name',
    (c) => {
      const s = spaceOf(c)
      s.templates = s.templates.filter((t) => t.name !== c.params.name)
      // The defaults that named it go with it.
      for (const [folder, name] of Object.entries(s.defaults.folders))
        if (name === c.params.name) delete s.defaults.folders[folder]
      if (s.defaults.quickNote === c.params.name) s.defaults.quickNote = ''
    },
  ],

  // favourites: kept as what they point at, and named when listed (a page renamed or gone shows as it is now)
  ['GET', '/api/spaces/:space/favorites', (c) => favoriteList(spaceOf(c))],
  [
    'POST',
    '/api/spaces/:space/favorites',
    (c) => {
      const s = spaceOf(c)
      const { kind, target } = c.body as { kind: Favorite['kind']; target: string }
      if (!s.favorites.some((f) => f.kind === kind && f.target === target))
        add(s.favorites, { kind, target, name: '', missing: false, created: rfc3339(c.now()) })
      return favoriteList(s)
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/favorites',
    (c) => {
      const s = spaceOf(c)
      s.favorites = s.favorites.filter((f) => !(f.kind === c.query.get('kind') && f.target === c.query.get('target')))
      return favoriteList(s)
    },
  ],
  [
    'PUT',
    '/api/spaces/:space/favorites/order',
    (c) => {
      const s = spaceOf(c)
      const rank = (f: Favorite) =>
        c.body.items.findIndex((i: { kind: string; target: string }) => i.kind === f.kind && i.target === f.target)
      s.favorites.sort((a, b) => rank(a) - rank(b))
      return favoriteList(s)
    },
  ],
]
