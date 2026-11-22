// The page types of one space of the in-memory server, and the folders bound to
// them (docs/api.md, "Page types"). Strict about the rules the web app meets: the
// limits, the refusals, how a type is merged, and what a folder's move or removal
// does to the bindings.

import type { FieldKind, FolderBinding, PageType, TypeField, TypeInput } from '../types.ts'
import { BUILTIN_KEYS, RESERVED_KEYS, folderTypeName } from '../../lib/pageTypes.ts'
import { HttpError } from './store.ts'
import { gitBlobId } from './sha1.ts'

const MAX_TYPES = 100
const MAX_FIELDS = 200
const KINDS: readonly string[] = ['text', 'longtext', 'select', 'multiselect', 'date', 'number', 'checkbox', 'url']

const bytes = (s: string) => new TextEncoder().encode(s).length
const invalid = (message: string) => new HttpError(400, 'invalid', message)
const conflict = (current: StoredType | undefined) => new HttpError(409, 'conflict', '类型已被修改', current ? { current: view(current) } : undefined)

interface StoredField {
  key: string
  title: string
  kind: FieldKind
  options?: string[]
}
interface StoredType {
  name: string
  title: string
  fields: StoredField[]
  // Set by a test: the file is damaged and cannot be read or written.
  error?: string
}

function schemaOf(f: StoredField): unknown {
  const title = f.title ? { title: f.title } : {}
  switch (f.kind) {
    case 'text':
      return { ...title, type: 'string' }
    case 'longtext':
      return { ...title, type: 'string', 'x-multiline': true }
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
    case 'other':
      return { ...title }
  }
}

// The rev of a type: the blob id of what its file would say, "unreadable" for a damaged file.
const revOf = (t: StoredType) => (t.error ? 'unreadable' : gitBlobId(JSON.stringify({ title: t.title, fields: t.fields.map((f) => [f.key, f.title, f.kind, f.options ?? null]) })))

const view = (t: StoredType): PageType => ({
  name: t.name,
  title: t.title,
  rev: revOf(t),
  fields: t.fields.map((f): TypeField => ({ key: f.key, title: f.title, kind: f.kind, ...(f.kind === 'select' || f.kind === 'multiselect' ? { options: [...(f.options ?? [])] } : {}), schema: schemaOf(f) })),
  ...(t.error ? { error: t.error } : {}),
})

const isUnder = (path: string, parent: string) => path === parent || path.startsWith(parent + '/')

export function createTypeBook(folderExists: (path: string) => string | null) {
  const types = new Map<string, StoredType>()
  let bindings: FolderBinding[] = []

  const checkName = (name: string) => {
    if (name === '' || bytes(name) > 100 || /[<>:"/\\|?*\p{Cc}]/u.test(name) || name.startsWith('.') || name.endsWith('.') || name.endsWith(' ')) throw invalid('类型名不合法')
  }
  const line = (what: string, s: string) => {
    if (/[\r\n]/.test(s) || bytes(s) > 200) throw invalid(`${what}要写成一行，不超过 200 字节`)
  }

  return {
    list: (): { types: PageType[]; bindings: FolderBinding[] } => ({
      types: [...types.values()].sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0)).map(view),
      bindings: bindings.map((b) => ({ ...b })),
    }),

    put(name: string, input: TypeInput): PageType {
      checkName(name)
      const same = [...types.keys()].find((n) => n !== name && n.toLowerCase() === name.toLowerCase())
      if (same) throw new HttpError(409, 'exists', `已有一个叫 ${same} 的类型，名字只差大小写`)
      const old = types.get(name)
      if (old?.error) throw invalid('这个类型的文件坏了，要手动修好才能改')
      // Made from the type as it is: a type that is there needs its rev, one that is not needs none.
      if ((old ? revOf(old) : '') !== (input.baseRev ?? '')) throw conflict(old)
      if (!old && types.size >= MAX_TYPES) throw invalid(`一个空间最多 ${MAX_TYPES} 个类型`)
      if (typeof input.title !== 'string' && input.title !== undefined) throw invalid('title 要是字符串')
      const title = (input.title ?? '').trim()
      line('类型的显示名', title)
      if (!Array.isArray(input.fields)) throw invalid('fields 要是列表')
      if (input.fields.length > MAX_FIELDS) throw invalid(`一个类型最多 ${MAX_FIELDS} 个字段`)
      const seen = new Set<string>()
      const fields: StoredField[] = input.fields.map((f) => {
        const key = typeof f.key === 'string' ? f.key : ''
        if (key === '') throw invalid('字段的键不能为空')
        // Only a new field has its key checked: one the type has keeps its key as it is.
        const before = old?.fields.find((x) => x.key === key)
        if (!before && (bytes(key) > 64 || /[\p{Cc}:]/u.test(key) || BUILTIN_KEYS.has(key) || RESERVED_KEYS.has(key))) throw invalid(`字段的键 “${key}” 不能用`)
        if (seen.has(key)) throw invalid(`字段 “${key}” 写了两次`)
        seen.add(key)
        const ftitle = (f.title ?? '').trim()
        line('字段的显示名', ftitle)
        const kind = (f.kind ?? before?.kind) as FieldKind | undefined
        if (!kind) throw invalid(`新字段 “${key}” 要写种类`)
        if (kind === 'other' ? before?.kind !== 'other' : !KINDS.includes(kind)) throw invalid(`字段 “${key}” 的种类不对`)
        const picks = kind === 'select' || kind === 'multiselect'
        if (!picks) {
          if (f.options !== undefined && f.options !== null) throw invalid(`字段 “${key}” 不是选择，不能有选项`)
          return { key, title: ftitle, kind }
        }
        // A field that stays a select and leaves its options out keeps them.
        const options = f.options ?? (before && before.kind === kind ? before.options : undefined)
        if (!options || options.length < 1 || options.length > 500) throw invalid(`字段 “${key}” 要有 1 到 500 个选项`)
        const uniq = new Set<string>()
        for (const o of options) {
          if (typeof o !== 'string' || o.trim() === '' || bytes(o) > 200 || uniq.has(o)) throw invalid(`字段 “${key}” 的选项不能空、不能重复、不超过 200 字节`)
          uniq.add(o)
        }
        return { key, title: ftitle, kind, options: [...options] }
      })
      // A field of kind `other` that the type already had and the request keeps stays as it was.
      const next: StoredType = { name, title, fields }
      types.set(name, next)
      return view(next)
    },

    remove(name: string, baseRev: string) {
      const bound = bindings.some((b) => b.type === name)
      if (!types.has(name) && !bound) throw new HttpError(404, 'not_found', '没有这个类型')
      const old = types.get(name)
      if ((old ? revOf(old) : '') !== baseRev) throw conflict(old)
      types.delete(name)
      bindings = bindings.filter((b) => b.type !== name)
    },

    // The folder as spelled (the answer's `folder`).
    bind(folder: string, type: string): string {
      const spelled = folderExists(folder)
      if (spelled === null) throw new HttpError(404, 'not_found', '找不到这个文件夹')
      if (type !== '' && !types.has(type)) throw invalid('没有这个类型')
      const rest = bindings.filter((b) => b.folder !== spelled)
      if (type === '') bindings = rest
      else {
        const at = bindings.findIndex((b) => b.folder === spelled)
        bindings = at >= 0 ? bindings.map((b, i) => (i === at ? { folder: spelled, type } : b)) : [...bindings, { folder: spelled, type }]
      }
      return spelled
    },

    effective: (folder: string): string => folderTypeName(bindings, folder),

    // Renaming or moving a folder moves the bindings of it and of the folders in it.
    folderMoved(from: string, to: string) {
      bindings = bindings.map((b) => (isUnder(b.folder, from) ? { ...b, folder: to + b.folder.slice(from.length) } : b))
    },
    folderRemoved(path: string) {
      bindings = bindings.filter((b) => !isUnder(b.folder, path))
    },

    // Test hook: a field is of a kind the app does not know (a file written by hand).
    forceKind(name: string, key: string, kind: FieldKind) {
      const f = types.get(name)?.fields.find((x) => x.key === key)
      if (f) f.kind = kind
    },

    // Test hook: the file of a type was damaged by hand.
    damage(name: string, message: string) {
      types.set(name, { name, title: '', fields: [], error: message })
    },
  }
}
