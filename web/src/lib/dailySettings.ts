// The daily text's source as the settings pages edit it: one draft per language ("" is all languages), and the
// conversion to and from the shape the server speaks (docs/api.md, "The daily text").

import type { DailyPrefs, DailySettings, DailySource, DailySpaceSource, FilterOp, PropFilter } from '@/api/types'
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
  return {
    kind,
    value: src[kind] as string,
    filter: (src.filter ?? []).map((f) => ({ ...f, values: f.values ? [...f.values] : undefined })),
  }
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
export function tabsOf(
  languages: readonly string[],
  settings: DailySettings | null,
  prefs: DailyPrefs | null,
): string[] {
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
  return (prefs?.available ?? []).filter((a) =>
    tab === ''
      ? a.lang === ''
      : a.lang !== '' && (a.lang.toLowerCase() === t || (baseLang(a.lang) === baseLang(tab) && !tab.includes('-'))),
  )
}

// A filter's values as typed in one box (separated by commas).
export const valuesText = (f: PropFilter) => (f.values ?? []).map(String).join(', ')
export const parseValues = (text: string): string[] =>
  text
    .split(/[,，]/)
    .map((v) => v.trim())
    .filter((v) => v !== '')
export const opTakesValues = (op: PropFilter['op']) => op === 'in' || op === 'notIn' || op === 'contains'

// --- a space's list of sources for each language (docs/api.md, "daily/prefs") ----------------------------------

// One source in the list of a language of a space: an interface the administrators set up, the administrators' lines,
// or pages of this space.
export type SpaceItem = { kind: 'api'; id: string } | { kind: 'list' } | { kind: 'own'; draft: SourceDraft }

// The lists as the editor holds them, by tab: a space's own `sources`, else its interfaces (`apis`) each
// on the tab of the language it is made for, in the order they were chosen.
export function listsFromPrefs(prefs: DailyPrefs | null, tabs: readonly string[]): Record<string, SpaceItem[]> {
  const out: Record<string, SpaceItem[]> = {}
  if (!prefs) return out
  if (prefs.sources) {
    for (const [lang, list] of Object.entries(prefs.sources)) {
      const tab = lang === '' ? '' : (tabs.find((t) => t.toLowerCase() === lang.toLowerCase()) ?? lang)
      out[tab] = list.map((s): SpaceItem => {
        if (s.kind === 'list') return { kind: 'list' }
        if (s.kind === 'own')
          return { kind: 'own', draft: sourceToDraft(s.source) ?? { kind: 'folder', value: '', filter: [] } }
        return { kind: 'api', id: s.id }
      })
    }
    return out
  }
  for (const id of prefs.apis) {
    // One that is switched off is on no tab (it is not offered): it stays where all languages are, marked.
    const tab = tabs.find((t) => apisOfTab(prefs, t).some((a) => a.id === id)) ?? ''
    ;(out[tab] ??= []).push({ kind: 'api', id })
  }
  return out
}

// What to send for the lists of the tabs that take "an interface" as their source. Only interfaces: `apis` (an
// ordered list of ids and their options); else `sources` by language.
export function prefsPayload(
  lists: Record<string, SpaceItem[]>,
  options: Record<string, Record<string, string[]>>,
  tabs: readonly string[],
): {
  apis: string[]
  options?: Record<string, Record<string, string[]>>
  sources?: Record<string, DailySpaceSource[]>
} {
  const kept = tabs.filter((t) => (lists[t] ?? []).length > 0)
  const all = kept.flatMap((t) => lists[t])
  if (all.every((i) => i.kind === 'api')) {
    const apis = [...new Set(all.map((i) => (i.kind === 'api' ? i.id : '')))]
    const used = Object.fromEntries(Object.entries(options).filter(([id]) => apis.includes(id)))
    return { apis, ...(Object.keys(used).length > 0 ? { options: used } : {}) }
  }
  const sources: Record<string, DailySpaceSource[]> = {}
  for (const t of kept)
    sources[t] = lists[t].map((i): DailySpaceSource => {
      if (i.kind === 'list') return { kind: 'list' }
      if (i.kind === 'own') return { kind: 'own', source: draftToSource(i.draft) }
      return { kind: 'api', id: i.id, ...(options[i.id] ? { options: options[i.id] } : {}) }
    })
  return { apis: [], sources }
}
