// The daily text's source as the settings pages edit it: one draft per language ("" is all languages), and the
// conversion to and from the shape the server speaks (docs/api.md, "The daily text").

import type { DailyPrefs, DailySettings, DailySource, FilterOp, PropFilter } from '@/api/types'
import { canonicalTag } from './langTags'

export type SourceKind = 'folder' | 'tag' | 'type'
export const SOURCE_KINDS: readonly SourceKind[] = ['folder', 'tag', 'type']
export const FILTER_OPS: readonly (FilterOp | 'notIn')[] = ['in', 'notIn', 'empty', 'notEmpty', 'contains']

export interface SourceDraft {
  kind: SourceKind
  value: string
  filter: PropFilter[]
}
// What a language does about its text: nothing of its own (the default and the interfaces take over), the pages of
// the space, or an interface the administrators set up.
export type Mode = 'default' | 'own' | 'api'
export interface LangDraft extends SourceDraft {
  mode: Mode
}
export type Drafts = Record<string, LangDraft>

const SOURCE_KEYS = new Set(['folder', 'tag', 'type', 'filter'])

export const emptyDraft = (): LangDraft => ({ mode: 'default', kind: 'folder', value: '', filter: [] })

export function sourceToDraft(src: DailySource): SourceDraft | null {
  const kind = SOURCE_KINDS.find((k) => typeof src[k] === 'string')
  if (!kind) return null
  return { kind, value: src[kind] as string, filter: (src.filter ?? []).map((f) => ({ ...f, values: f.values ? [...f.values] : undefined })) }
}

export function draftToSource(d: SourceDraft): DailySource {
  const out: DailySource = { [d.kind]: d.value }
  const filter = d.filter.filter((f) => f.key.trim() !== '').map((f) => ({ ...f, key: f.key.trim() }))
  if (filter.length) out.filter = filter
  return out
}

// The languages a settings object has a source for ("" is the one written beside the language keys).
export function languagesOf(settings: DailySettings | null): string[] {
  if (!settings) return []
  const langs = Object.keys(settings).filter((k) => !SOURCE_KEYS.has(k))
  return [...(sourceToDraft(settings) ? [''] : []), ...langs]
}

export function draftsFrom(settings: DailySettings | null): Drafts {
  const out: Drafts = {}
  if (!settings) return out
  const all = sourceToDraft(settings)
  if (all) out[''] = { mode: 'own', ...all }
  for (const [k, v] of Object.entries(settings)) {
    if (SOURCE_KEYS.has(k) || typeof v !== 'object' || v === null || Array.isArray(v)) continue
    const d = sourceToDraft(v as DailySource)
    if (d) out[k] = { mode: 'own', ...d }
  }
  return out
}

// What the drafts say, to send: only languages whose mode is "the pages of the space" have a source. Null: none.
export function settingsFrom(drafts: Drafts): DailySettings | null {
  const out: Record<string, unknown> = {}
  for (const [lang, d] of Object.entries(drafts)) {
    if (d.mode !== 'own' || d.value === '') continue
    const src = draftToSource(d)
    if (lang === '') Object.assign(out, src)
    else out[lang] = src
  }
  return Object.keys(out).length ? (out as DailySettings) : null
}

// The first part of a language tag: `zh` for `zh-Hans`.
export const baseLang = (code: string) => code.toLowerCase().split('-')[0]

// The tabs of the settings page: "all languages", the server's list, then any language that something already
// names (a source in the file, an interface), so nothing that is set is out of reach.
export function tabsOf(languages: readonly string[], settings: DailySettings | null, prefs: DailyPrefs | null): string[] {
  // Tags are canonical as the server writes them (zh-Hans, pt-BR): they are kept as they are, and compared by case.
  const seen = new Set<string>([''])
  const tabs: string[] = ['']
  const add = (code: string) => {
    const c = canonicalTag(code) ?? code
    if (c !== '' && !seen.has(c.toLowerCase())) {
      seen.add(c.toLowerCase())
      tabs.push(c)
    }
  }
  for (const l of languages) add(l)
  for (const l of languagesOf(settings)) add(l)
  for (const a of prefs?.available ?? []) add(a.lang)
  return tabs
}

// The interfaces that belong to a tab: those made for that language, or for every language on the first tab.
export function apisOfTab(prefs: DailyPrefs | null, tab: string) {
  const t = tab.toLowerCase()
  return (prefs?.available ?? []).filter((a) => (tab === '' ? a.lang === '' : a.lang !== '' && (a.lang.toLowerCase() === t || (baseLang(a.lang) === baseLang(tab) && !tab.includes('-')))))
}

// A filter's values as typed in one box (separated by commas).
export const valuesText = (f: PropFilter) => (f.values ?? []).map(String).join(', ')
export const parseValues = (text: string): string[] =>
  text
    .split(/[,，]/)
    .map((v) => v.trim())
    .filter((v) => v !== '')
export const opTakesValues = (op: PropFilter['op']) => op === 'in' || op === 'notIn' || op === 'contains'
