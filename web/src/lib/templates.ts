// What the template screens decide without drawing anything: the browser's time zone as the server wants it, the
// rules of a template's name, the form of a template's header and the template a folder starts from.

import type { PageMeta, PageTemplate, TemplateDefaults } from '@/api/types'

// Minutes east of UTC, for {{date}}, {{time}} and {{datetime}} (the server's `tz`).
export const tzOffset = (): number => -new Date().getTimezoneOffset()

export type NameProblem = 'empty' | 'long' | 'chars' | 'reserved' | 'exists'

// A template's name is a file name: the rules of a page's name (the server has the last word).
export function templateNameProblem(name: string, others: string[]): NameProblem | null {
  if (name === '') return 'empty'
  if (Array.from(name).length > 80) return 'long'
  if (name === 'defaults' || /^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$/i.test(name)) return 'reserved'
  if (/[<>:"/\\|?*\p{Cc}]/u.test(name) || name.startsWith('.') || /[.\s]$/.test(name) || name !== name.trim()) return 'chars'
  if (others.some((o) => o.toLowerCase() === name.toLowerCase())) return 'exists'
  return null
}

// The header fields the form has; the others are kept as the template has them.
export const FORM_KEYS = ['title', 'subtitle', 'type', 'tags'] as const

export interface HeaderDraft {
  title: string
  subtitle: string
  type: string
  // As typed, separated by commas.
  tags: string
}

const text = (v: unknown) => (typeof v === 'string' ? v : '')

export function draftOf(meta: PageMeta | undefined): HeaderDraft {
  const tags = meta?.tags
  return {
    title: text(meta?.title),
    subtitle: text(meta?.subtitle),
    type: text(meta?.type),
    tags: Array.isArray(tags) ? tags.filter((x): x is string => typeof x === 'string').join(', ') : text(tags),
  }
}

export const tagList = (s: string): string[] => [...new Set(s.split(/[,，]/).map((x) => x.trim()).filter((x) => x !== ''))]

// How many header fields the form leaves as they are.
export const otherKeys = (meta: PageMeta | undefined): string[] => Object.keys(meta ?? {}).filter((k) => !(FORM_KEYS as readonly string[]).includes(k) && k !== 'id' && k !== 'created')

// The header to send: the fields the form does not have as the template had them, the others as typed (left out when empty).
export function metaOf(base: PageMeta | undefined, d: HeaderDraft): PageMeta {
  const meta: PageMeta = {}
  for (const [k, v] of Object.entries(base ?? {})) if (!(FORM_KEYS as readonly string[]).includes(k)) meta[k] = v
  if (d.title.trim() !== '') meta.title = d.title.trim()
  if (d.subtitle.trim() !== '') meta.subtitle = d.subtitle.trim()
  if (d.type !== '') meta.type = d.type
  const tags = tagList(d.tags)
  if (tags.length > 0) meta.tags = tags
  return meta
}

// The template a page made in `folder` starts with: the folder's default, if the template is still there.
export function defaultFor(folder: string, defaults: TemplateDefaults | undefined, templates: PageTemplate[]): string {
  const name = defaults?.folders[folder] ?? ''
  return templates.some((t) => t.name === name) ? name : ''
}

// The name of a template shown to a person: its title when it has one, with the name beside it.
export const templateLabel = (t: PageTemplate): string => {
  const title = text(t.meta?.title)
  return title !== '' && title !== t.name ? `${t.name} · ${title}` : t.name
}
