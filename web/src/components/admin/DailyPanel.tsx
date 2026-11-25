import { useMemo, useState } from 'react'
import { Plus, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import {
  useAdminDaily,
  useAdminSpaces,
  useDeleteDailyApi,
  useDeleteDailyLines,
  useSaveDailyApi,
  useSaveDailyLines,
  useSaveDailyProvider,
  useSaveDailySite,
  useTryDailyApi,
} from '@/api/admin'
import type {
  AdminDaily,
  DailyApi,
  DailyDefault,
  DailyLine,
  DailyOptionChoice,
  DailySource,
  DailySourceItem,
  DailySettings,
  DailyTryResult,
  KnownLanguage,
  LinesFormat,
} from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsRow, SettingsRows, SettingsSection } from '@/components/settings/SettingsParts'
import { ListButtons, moved, OptionChoices } from '@/components/settings/DailyChoices'
import { SourceFields } from '@/components/settings/SourceFields'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { OptionalMark, RequiredMark } from '@/components/ui/field-mark'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  draftsFrom,
  draftToSource,
  emptyDraft,
  settingsFrom,
  sourceToDraft,
  tabsOf,
  type Drafts,
  type SourceDraft,
} from '@/lib/dailySettings'
import { dayOf } from '@/api/hooks'
import { canonicalTag, matchTag } from '@/lib/langTags'
import { dailyCodeMessage } from './dailyCodes'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'

const d = ta.daily
// The server says why it refused with a code (and what it is about); it is put in the language of the page here.
// A code that is not known shows the server's own words.
const localised = (r: Parameters<typeof dailyCodeMessage>[0]) => dailyCodeMessage(r, d)
const message = (e: unknown) =>
  e instanceof ApiError && (e.reason || e.code === 'invalid' || e.code === 'not_found' || e.code === 'rate_limited')
    ? (localised(e.reason) ?? e.message)
    : t.fmt.labelled(d.failed, e instanceof Error ? e.message : '')

// What administrators decide about the daily text of every space: what spaces may do, the languages, the default,
// the pasted lines, the built-in interfaces (switches) and the external interfaces (any number, with a button to
// try each). It is a chunk of its own, loaded by the administration.
export function DailyPanel() {
  const q = useAdminDaily()
  if (q.isPending) return <PageFallback />
  if (q.isError)
    return <ErrorState message={t.fmt.labelled(d.loadFailed, q.error.message)} onRetry={() => void q.refetch()} />
  const data = q.data
  return (
    <div className="min-w-0 space-y-10" data-slot="admin-daily">
      <p className="text-sm break-words text-muted-foreground">{d.help}</p>
      {nothingOn(data) && (
        <p
          role="note"
          data-slot="daily-off-hint"
          className="rounded-lg border bg-muted/40 p-3 text-sm break-words text-muted-foreground"
        >
          {d.allOffHint}
        </p>
      )}
      <AllowSection data={data} />
      <LanguagesSection data={data} />
      <DefaultSection data={data} />
      <LinesSection data={data} />
      <BuiltinSection data={data} />
      <ApisSection data={data} />
    </div>
  )
}

// Nothing the home page could show: every built-in interface is off (the way a fresh server starts, for privacy), no
// interface of our own is on, and there are no lines.
const nothingOn = (data: AdminDaily) =>
  data.providers.every((p) => !p.enabled) && data.apis.every((a) => !a.enabled) && data.lines.total === 0

// Two switches for what spaces may do. They wait for the server to say (`allow`): until it does they show as on and
// cannot be changed.
function AllowSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailySite()
  const allow = data.allow
  const set = (patch: Partial<{ own: boolean; api: boolean }>) => {
    if (!allow) return
    save.mutate({ allow: { ...allow, ...patch } }, { onError: (e) => void toast.error(message(e)) })
  }
  return (
    <SettingsSection title={d.allowTitle} description={allow ? undefined : d.allowPending}>
      <SettingsRows>
        <SettingsRow title={d.allowOwn} help={d.allowOwnHelp} controlId="daily-allow-own">
          <Switch
            id="daily-allow-own"
            checked={allow?.own ?? true}
            disabled={!allow || save.isPending}
            onCheckedChange={(on) => set({ own: on })}
          />
        </SettingsRow>
        <SettingsRow title={d.allowApi} help={d.allowApiHelp} controlId="daily-allow-api">
          <Switch
            id="daily-allow-api"
            checked={allow?.api ?? true}
            disabled={!allow || save.isPending}
            onCheckedChange={(on) => set({ api: on })}
          />
        </SettingsRow>
      </SettingsRows>
    </SettingsSection>
  )
}

// A language to pick: the common ones, searchable by tag, native name or English name; any other valid language tag
// may be typed (and is offered as it is written canonically). Pressing Enter takes the typed tag, else the first match.
function LanguagePicker({
  id,
  known,
  taken = [],
  onPick,
  disabled,
  required,
}: {
  id: string
  known: readonly KnownLanguage[]
  taken?: readonly string[]
  onPick: (tag: string) => void
  disabled?: boolean
  required?: boolean
}) {
  const [text, setText] = useState('')
  const q = text.trim().toLowerCase()
  const typed = q === '' ? null : canonicalTag(text)
  const matches = known.filter(
    (k) =>
      !taken.includes(k.tag) &&
      (q === '' ||
        k.tag.toLowerCase().includes(q) ||
        k.name.toLowerCase().includes(q) ||
        k.english.toLowerCase().includes(q)),
  )
  const extra = typed !== null && !known.some((k) => k.tag === typed) && !taken.includes(typed) ? typed : null
  const pick = (tag: string) => {
    onPick(tag)
    setText('')
  }
  return (
    <div className="grid min-w-0 grid-cols-1 gap-1.5" data-slot="language-picker">
      <Input
        id={id}
        role="combobox"
        aria-expanded="true"
        aria-controls={`${id}-list`}
        aria-label={d.languageSearch}
        aria-required={required ? 'true' : undefined}
        placeholder={d.languagePick}
        value={text}
        disabled={disabled}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== 'Enter') return
          e.preventDefault()
          const first = typed ?? matches[0]?.tag
          if (first && !taken.includes(first)) pick(first)
        }}
        autoComplete="off"
        className="w-full max-w-sm min-w-0"
      />
      <ul
        id={`${id}-list`}
        role="listbox"
        aria-label={d.languagesTitle}
        className="grid max-h-44 max-w-sm min-w-0 grid-cols-1 overflow-y-auto rounded-lg border"
      >
        {extra !== null && (
          <li role="option" aria-selected="false">
            <button
              type="button"
              data-tag={extra}
              disabled={disabled}
              onClick={() => pick(extra)}
              className="flex w-full min-w-0 items-center justify-between gap-2 px-3 py-1.5 text-left text-sm break-words outline-none hover:bg-muted focus-visible:bg-muted"
            >
              {d.languageTyped(extra)}
            </button>
          </li>
        )}
        {matches.map((k) => (
          <li key={k.tag} role="option" aria-selected="false">
            <button
              type="button"
              data-tag={k.tag}
              disabled={disabled}
              onClick={() => pick(k.tag)}
              className="flex w-full min-w-0 items-center justify-between gap-3 px-3 py-1.5 text-left text-sm outline-none hover:bg-muted focus-visible:bg-muted"
            >
              <span className="min-w-0 break-words">
                {k.name}
                {k.english !== k.name && <span className="text-muted-foreground"> · {k.english}</span>}
              </span>
              <span className="shrink-0 font-mono text-xs text-muted-foreground">{k.tag}</span>
            </button>
          </li>
        ))}
        {matches.length === 0 && extra === null && (
          <li className="px-3 py-2 text-xs break-words text-muted-foreground" data-slot="language-none">
            {q !== '' && typed === null ? d.languageBad : d.languageNone}
          </li>
        )}
      </ul>
    </div>
  )
}

function LanguagesSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailySite()
  const [error, setError] = useState('')
  const write = (languages: string[]) =>
    save.mutate({ languages }, { onSuccess: () => setError(''), onError: (e) => setError(message(e)) })
  return (
    <SettingsSection title={d.languagesTitle} description={d.languagesHelp}>
      <ul className="flex flex-wrap gap-1.5" aria-label={d.languagesTitle}>
        <li className="rounded-full border px-2.5 py-0.5 text-sm text-muted-foreground">{d.allLanguages}</li>
        {data.languages.map((l) => (
          <li key={l} className="flex items-center gap-1 rounded-full border py-0.5 pr-1 pl-2.5 text-sm">
            {l}
            <button
              type="button"
              aria-label={d.removeLanguage(l)}
              title={d.removeLanguage(l)}
              onClick={() => write(data.languages.filter((x) => x !== l))}
              className="rounded-full p-0.5 hover:bg-muted"
            >
              <X className="size-3" />
            </button>
          </li>
        ))}
      </ul>
      <div className="grid gap-1.5">
        <Label htmlFor="daily-lang">
          {d.languageCode}
          <RequiredMark />
        </Label>
        <LanguagePicker
          id="daily-lang"
          required
          known={data.knownLanguages}
          taken={data.languages}
          disabled={save.isPending}
          onPick={(tag) => write([...data.languages, tag])}
        />
      </div>
      <FormMessage>{error}</FormMessage>
    </SettingsSection>
  )
}

// A tab per language, "all languages" first.
function LangTabs({
  langs,
  value,
  onChange,
  label,
}: {
  langs: string[]
  value: string
  onChange: (l: string) => void
  label: string
}) {
  return (
    <div role="tablist" aria-label={label} className="flex flex-wrap gap-1">
      {langs.map((l) => (
        <button
          key={l || 'all'}
          type="button"
          role="tab"
          aria-selected={value === l}
          data-lang={l}
          onClick={() => onChange(l)}
          className={`inline-flex h-8 items-center rounded-lg px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${value === l ? 'bg-muted font-medium' : 'text-muted-foreground hover:text-foreground'}`}
        >
          {l === '' ? d.allLanguages : l}
        </button>
      ))}
    </div>
  )
}

// One source in the list of a language of the default (see DailyDefault).
type Item = { kind: 'api'; id: string } | { kind: 'list' } | { kind: 'space'; space: string; draft: SourceDraft }

// The lists of the default as the editor holds them: whatever the kind it was saved as.
function itemsOf(def: DailyDefault): Record<string, Item[]> {
  if (def?.kind === 'api')
    return Object.fromEntries(
      Object.entries(def.apis).map(([l, ids]) => [l, ids.map((id): Item => ({ kind: 'api', id }))]),
    )
  if (def?.kind !== 'sources') return {}
  return Object.fromEntries(
    Object.entries(def.sources).map(([l, list]) => [
      l,
      list.map((src): Item => {
        if (src.kind === 'list') return { kind: 'list' }
        if (src.kind === 'space')
          return {
            kind: 'space',
            space: src.space,
            draft: sourceToDraft(src.source as DailySource) ?? { ...emptyDraft() },
          }
        return { kind: 'api', id: src.id }
      }),
    ]),
  )
}
function optionsOfItems(def: DailyDefault): DailyOptionChoice {
  const out: DailyOptionChoice = {}
  if (def?.kind === 'sources')
    for (const list of Object.values(def.sources))
      for (const src of list) if (src.kind === 'api' && src.options) out[src.id] = structuredClone(src.options)
  return out
}

function DefaultSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailySite()
  const spaces = useAdminSpaces('')
  const initial = data.default
  const [kind, setKind] = useState<'none' | 'list' | 'space' | 'api'>(
    initial === null ? 'none' : initial.kind === 'sources' ? 'api' : initial.kind,
  )
  // The sources of each language, in order: external interfaces, our own lines, pages of a space.
  const [apis, setApis] = useState<Record<string, Item[]>>(() => itemsOf(initial))
  const [options, setOptions] = useState<DailyOptionChoice>(() =>
    initial?.kind === 'api' ? structuredClone(initial.options ?? {}) : optionsOfItems(initial),
  )
  const [space, setSpace] = useState(initial?.kind === 'space' ? initial.space : '')
  const [drafts, setDrafts] = useState<Drafts>(() => (initial?.kind === 'space' ? draftsFrom(initial.source) : {}))
  const [tab, setTab] = useState('')
  const [error, setError] = useState('')
  const tabs = useMemo(
    () => tabsOf(data.languages, initial?.kind === 'space' ? initial.source : null, null),
    [data.languages, initial],
  )
  const draft = drafts[tab] ?? { ...emptyDraft(), mode: 'own' as const }
  const items = spaces.data?.pages.flatMap((p) => p.items) ?? []
  const spaceItems = items

  const submit = () => {
    setError('')
    let value: DailyDefault = null
    if (kind === 'list') value = { kind: 'list' }
    if (kind === 'api') {
      const chosen = Object.fromEntries(Object.entries(apis).filter(([, items]) => items.length > 0))
      if (Object.keys(chosen).length === 0) return setError(d.defaultApiIncomplete)
      const all = Object.values(chosen).flat()
      if (all.some((i) => i.kind === 'space' && (i.space === '' || i.draft.value === '')))
        return setError(d.defaultSpaceIncomplete)
      if (all.every((i) => i.kind === 'api')) {
        // Interfaces only: a default of kind `api`.
        const used = new Set(all.map((i) => (i.kind === 'api' ? i.id : '')))
        const kept = Object.fromEntries(Object.entries(options).filter(([id]) => used.has(id)))
        value = {
          kind: 'api',
          apis: Object.fromEntries(
            Object.entries(chosen).map(([l, items]) => [l, items.map((i) => (i.kind === 'api' ? i.id : ''))]),
          ),
          ...(Object.keys(kept).length > 0 ? { options: kept } : {}),
        }
      } else {
        value = {
          kind: 'sources',
          sources: Object.fromEntries(
            Object.entries(chosen).map(([l, items]) => [
              l,
              items.map((i): DailySourceItem => {
                if (i.kind === 'list') return { kind: 'list' }
                if (i.kind === 'space')
                  return { kind: 'space', space: i.space, source: draftToSource(i.draft) as DailySettings }
                return { kind: 'api', id: i.id, ...(options[i.id] ? { options: options[i.id] } : {}) }
              }),
            ]),
          ),
        }
      }
    }
    if (kind === 'space') {
      const source = settingsFrom(
        Object.fromEntries(Object.entries(drafts).map(([k, v]) => [k, { ...v, mode: 'own' as const }])),
      )
      if (space === '' || !source) return setError(d.defaultSpaceIncomplete)
      value = { kind: 'space', space, source: source as DailySettings }
    }
    save.mutate({ default: value }, { onSuccess: () => toast.success(d.saved), onError: (e) => setError(message(e)) })
  }
  return (
    <SettingsSection title={d.defaultTitle} description={d.defaultHelp}>
      <div role="radiogroup" aria-label={d.defaultTitle} className="grid gap-2">
        {(['none', 'list', 'space', 'api'] as const).map((k) => (
          <label key={k} className="flex items-center gap-2 text-sm">
            <input type="radio" name="daily-default" checked={kind === k} onChange={() => setKind(k)} />
            {d.defaultKinds[k]}
          </label>
        ))}
      </div>
      {kind === 'list' && <p className="text-xs break-words text-muted-foreground">{d.defaultListHelp}</p>}
      {kind === 'api' && (
        <div className="grid grid-cols-1 gap-3 rounded-lg border p-4" data-slot="default-apis">
          <p className="text-xs break-words text-muted-foreground">{d.defaultApiHelp}</p>
          {[...data.languages, ...Object.keys(apis).filter((l) => l !== '' && !data.languages.includes(l)), ''].map(
            (l) => {
              const items = apis[l] ?? []
              const setItems = (next: Item[]) => setApis((all) => ({ ...all, [l]: next }))
              const setItem = (i: number, patch: Partial<Item>) =>
                setItems(items.map((x, at) => (at === i ? ({ ...x, ...patch } as Item) : x)))
              const nameOf = (id: string) => data.apis.find((a) => a.id === id)?.name ?? id
              const label = (i: Item) =>
                i.kind === 'api' ? nameOf(i.id) : i.kind === 'list' ? d.sourceLines : d.sourceSpace
              return (
                <div
                  key={l || 'rest'}
                  className="grid min-w-0 grid-cols-1 items-start gap-1.5 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]"
                  data-lang={l}
                >
                  <Label htmlFor={`daily-default-api-${l || 'rest'}`} className="break-words">
                    {l === '' ? d.defaultApiRest : d.defaultApiFor(l)}
                  </Label>
                  <div className="grid min-w-0 gap-2">
                    {items.length > 0 && (
                      <ol className="grid gap-2" data-slot="default-api-list">
                        {items.map((it, i) => {
                          const known = it.kind === 'api' ? data.apis.find((a) => a.id === it.id) : undefined
                          const opts =
                            it.kind === 'api' ? (data.providers.find((p) => p.id === it.id)?.options ?? []) : []
                          const key = it.kind === 'api' ? it.id : `${it.kind}-${i}`
                          return (
                            <li
                              key={key}
                              data-api-item={it.kind === 'api' ? it.id : undefined}
                              data-source-kind={it.kind}
                              className="grid min-w-0 gap-1.5 rounded-lg border p-2"
                            >
                              <div className="flex min-w-0 items-center justify-between gap-2">
                                <span className="min-w-0 text-sm break-words">
                                  {i + 1}. {label(it)}
                                  {it.kind === 'api' && (!known || !known.enabled) && (
                                    <span className="text-xs text-muted-foreground"> ({d.defaultApiOff})</span>
                                  )}
                                </span>
                                <ListButtons
                                  name={label(it)}
                                  first={i === 0}
                                  last={i === items.length - 1}
                                  onUp={() => setItems(moved(items, i, -1))}
                                  onDown={() => setItems(moved(items, i, 1))}
                                  onRemove={() => setItems(items.filter((_, at) => at !== i))}
                                  words={d.choice}
                                />
                              </div>
                              {it.kind === 'space' && (
                                <div className="grid gap-2">
                                  <NativeSelect
                                    aria-label={d.spaceLabel}
                                    value={it.space}
                                    onChange={(e) => setItem(i, { space: e.target.value })}
                                  >
                                    <option value="">{d.spaceChoose}</option>
                                    {it.space !== '' && !spaceItems.some((sp) => sp.id === it.space) && (
                                      <option value={it.space}>{it.space}</option>
                                    )}
                                    {spaceItems.map((sp) => (
                                      <option key={sp.id} value={sp.id}>
                                        {sp.name}
                                      </option>
                                    ))}
                                  </NativeSelect>
                                  <SourceFields draft={it.draft} onChange={(draft) => setItem(i, { draft })} />
                                </div>
                              )}
                              {opts.length > 0 && it.kind === 'api' && (
                                <OptionChoices
                                  options={opts}
                                  chosen={options[it.id]}
                                  owner={`${l || 'rest'}-${it.id}`}
                                  words={d.choice}
                                  onChange={(next) =>
                                    setOptions((all) => {
                                      const out = { ...all }
                                      if (Object.keys(next).length === 0) delete out[it.id]
                                      else out[it.id] = next
                                      return out
                                    })
                                  }
                                />
                              )}
                            </li>
                          )
                        })}
                      </ol>
                    )}
                    <NativeSelect
                      id={`daily-default-api-${l || 'rest'}`}
                      value=""
                      onChange={(e) => {
                        const v = e.target.value
                        if (v === '') return
                        if (v === '@list') setItems([...items, { kind: 'list' }])
                        else if (v === '@space') setItems([...items, { kind: 'space', space: '', draft: emptyDraft() }])
                        else setItems([...items, { kind: 'api', id: v }])
                      }}
                    >
                      <option value="">{items.length === 0 ? d.defaultApiNone : d.defaultApiAdd}</option>
                      <optgroup label={d.sourceInterfaces}>
                        {data.apis
                          .filter((a) => !items.some((x) => x.kind === 'api' && x.id === a.id))
                          .filter((a) => l === '' || !a.builtin || matchTag(l, a.languages ?? [a.lang]) !== null)
                          .map((a) => (
                            <option key={a.id} value={a.id}>
                              {a.name}
                              {a.enabled ? '' : ` (${d.enabledOff})`}
                            </option>
                          ))}
                      </optgroup>
                      {!items.some((x) => x.kind === 'list') && <option value="@list">{d.sourceLines}</option>}
                      <option value="@space">{d.sourceSpace}</option>
                    </NativeSelect>
                  </div>
                </div>
              )
            },
          )}
        </div>
      )}
      {kind === 'space' && (
        <div className="grid grid-cols-1 gap-4 rounded-lg border p-4">
          <FormMessage tone="info">{d.spaceWarning}</FormMessage>
          <div className="grid gap-1.5">
            <Label htmlFor="daily-default-space">
              {d.spaceLabel}
              <RequiredMark />
            </Label>
            <NativeSelect
              id="daily-default-space"
              aria-required="true"
              value={space}
              onChange={(e) => setSpace(e.target.value)}
            >
              <option value="">{d.spaceChoose}</option>
              {space !== '' && !items.some((s) => s.id === space) && <option value={space}>{space}</option>}
              {items.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </NativeSelect>
          </div>
          <LangTabs langs={tabs} value={tab} onChange={setTab} label={d.languagesTitle} />
          <p className="text-xs break-words text-muted-foreground">{d.sourceHelp}</p>
          <SourceFields
            draft={draft}
            onChange={(next) =>
              setDrafts((all) => ({ ...all, [tab]: { ...(all[tab] ?? emptyDraft()), ...next, mode: 'own' } }))
            }
          />
          {drafts[tab] && (
            <div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() =>
                  setDrafts((all) => {
                    const next = { ...all }
                    delete next[tab]
                    return next
                  })
                }
              >
                {d.sourceClear}
              </Button>
            </div>
          )}
        </div>
      )}
      <FormMessage>{error}</FormMessage>
      <div>
        <Button onClick={submit} disabled={save.isPending}>
          {save.isPending && <Spinner />}
          {d.save}
        </Button>
      </div>
    </SettingsSection>
  )
}

function LinesSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailyLines()
  const del = useDeleteDailyLines()
  const tabs = useMemo(
    () => [
      '',
      ...data.languages,
      ...Object.keys(data.lines.byLang).filter((l) => l !== '' && !data.languages.includes(l)),
    ],
    [data],
  )
  const [tab, setTab] = useState('')
  const [text, setText] = useState('')
  const [format, setFormat] = useState<LinesFormat>('lines')
  const [error, setError] = useState('')
  // The lines as split, shown to be corrected before they are kept (null: the text is sent as it is).
  const [rows, setRows] = useState<DailyLine[] | null>(null)
  const [busy, setBusy] = useState(false)
  const count = data.lines.byLang[tab] ?? 0
  const send = () => {
    setError('')
    save.mutate(rows ? { lang: tab, lines: rows.filter((r) => r.text.trim() !== '') } : { lang: tab, text, format }, {
      onSuccess: () => {
        setText('')
        setRows(null)
        toast.success(d.linesSaved)
      },
      onError: (e) => setError(message(e)),
    })
  }
  const load = async (what: 'preview' | 'saved') => {
    setError('')
    setBusy(true)
    try {
      const got =
        what === 'preview' ? await adminApi.previewDailyLines({ text, format }) : await adminApi.dailyLines(tab)
      setRows(got.lines)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  const setRow = (i: number, patch: Partial<DailyLine>) =>
    setRows((all) => (all ? all.map((r, at) => (at === i ? { ...r, ...patch } : r)) : all))
  return (
    <SettingsSection title={d.linesTitle} description={d.linesHelp}>
      <p className="text-xs text-muted-foreground">{d.linesTotal(data.lines.total, data.lines.max)}</p>
      <LangTabs langs={tabs} value={tab} onChange={setTab} label={d.linesTitle} />
      <p className="text-sm">{d.linesOfLang(count)}</p>
      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        aria-label={d.linesText}
        placeholder={d.linesPlaceholder}
        className="min-h-28"
      />
      <div className="flex flex-wrap items-center gap-2">
        <NativeSelect
          value={format}
          onChange={(e) => setFormat(e.target.value as LinesFormat)}
          aria-label={d.linesFormat}
          className="w-44"
        >
          <option value="lines">{d.formatLines}</option>
          <option value="markdown">{d.formatMarkdown}</option>
          <option value="csv">{d.formatCsv}</option>
        </NativeSelect>
        <label className="inline-flex h-8 cursor-pointer items-center rounded-lg border px-3 text-sm hover:bg-muted">
          {d.linesUpload}
          <input
            type="file"
            accept=".txt,.md,.csv,text/plain,text/markdown,text/csv"
            className="sr-only"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (!f) return
              if (/\.md$/i.test(f.name)) setFormat('markdown')
              if (/\.csv$/i.test(f.name)) setFormat('csv')
              void f.text().then(setText)
              e.target.value = ''
            }}
          />
        </label>
        <Button variant="outline" onClick={() => void load('preview')} disabled={busy || text.trim() === ''}>
          {d.linesPreview}
        </Button>
        <Button variant="outline" onClick={() => void load('saved')} disabled={busy || count === 0}>
          {d.linesEditSaved}
        </Button>
        <Button onClick={send} disabled={save.isPending || (rows ? rows.length === 0 : text.trim() === '')}>
          {save.isPending && <Spinner />}
          {d.linesSave}
        </Button>
        <Button
          variant="outline"
          disabled={del.isPending || count === 0}
          onClick={() =>
            del.mutate(tab, { onSuccess: () => toast.success(d.linesDeleted), onError: (e) => setError(message(e)) })
          }
        >
          <Trash2 />
          {d.linesDelete}
        </Button>
      </div>
      {rows && (
        <div className="grid gap-2 rounded-lg border p-3" data-slot="lines-preview">
          <p className="text-xs break-words text-muted-foreground">{d.linesPreviewHelp(rows.length)}</p>
          <ol className="grid gap-2">
            {rows.slice(0, 200).map((r, i) => (
              <li key={i} className="grid min-w-0 gap-1.5 rounded-md border p-2" data-slot="lines-row">
                <Textarea
                  value={r.text}
                  onChange={(e) => setRow(i, { text: e.target.value })}
                  aria-label={d.linesRowText(i + 1)}
                  className="min-h-12"
                />
                <div className="flex flex-wrap items-center gap-2">
                  <Input
                    value={r.author ?? ''}
                    onChange={(e) => setRow(i, { author: e.target.value })}
                    aria-label={d.linesRowAuthor(i + 1)}
                    placeholder={d.lineAuthor}
                    className="w-40"
                    data-field="author"
                  />
                  <Input
                    value={r.source ?? ''}
                    onChange={(e) => setRow(i, { source: e.target.value })}
                    aria-label={d.linesRowSource(i + 1)}
                    placeholder={d.lineSource}
                    className="w-48"
                    data-field="source"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label={d.linesRowRemove(i + 1)}
                    onClick={() => setRows((all) => (all ? all.filter((_, at) => at !== i) : all))}
                  >
                    <X />
                  </Button>
                </div>
              </li>
            ))}
          </ol>
          {rows.length > 200 && (
            <p className="text-xs text-muted-foreground">{d.linesPreviewMore(rows.length - 200)}</p>
          )}
          <div>
            <Button variant="ghost" size="sm" onClick={() => setRows(null)}>
              {d.linesPreviewClose}
            </Button>
          </div>
        </div>
      )}
      <FormMessage>{error}</FormMessage>
    </SettingsSection>
  )
}

type HeaderRow = { name: string; set: boolean; value: string; changing: boolean }
type MapRow = { tag: string; code: string }
type Form = Omit<DailyApi, 'headers' | 'note' | 'langMap' | 'builtin' | 'languages'> & {
  headers: HeaderRow[]
  langMap: MapRow[]
}

const blankForm = (): Form => ({
  id: '',
  name: '',
  lang: '',
  url: 'https://',
  headers: [],
  langMap: [],
  text: '',
  source: '',
  author: '',
  link: '',
  refresh: 'daily',
  refreshMinutes: 60,
  skipNew: true,
  rps: 1,
  enabled: true,
})
const formOf = (a: DailyApi): Form => ({
  id: a.id,
  name: a.name,
  lang: a.lang,
  url: a.url,
  text: a.text,
  source: a.source,
  author: a.author,
  link: a.link,
  refresh: a.refresh,
  refreshMinutes: a.refreshMinutes,
  skipNew: a.skipNew,
  rps: a.rps,
  enabled: a.enabled,
  headers: (a.headers ?? []).map((h) => ({ name: h.name, set: h.set === true, value: '', changing: false })),
  langMap: Object.entries(a.langMap ?? {}).map(([tag, code]) => ({ tag, code })),
})
// What is sent: a header whose value was not typed has only its name, which keeps the saved value.
// When the address is not the saved one the server keeps no header value, so every header is typed again.
// A row of the language mapping with a tag and a code is a mapping; the others are left out.
const payloadOf = (f: Form, saved: string): Omit<DailyApi, 'note'> => ({
  ...f,
  refreshMinutes: f.refresh === 'minutes' ? f.refreshMinutes : 0,
  headers: f.headers
    .filter((h) => h.name.trim() !== '')
    .map((h) =>
      h.set && f.url === saved && (!h.changing || h.value === '')
        ? { name: h.name.trim() }
        : { name: h.name.trim(), value: h.value },
    ),
  langMap: Object.fromEntries(
    f.langMap
      .filter((r) => r.tag.trim() !== '' && r.code.trim() !== '')
      .map((r) => [canonicalTag(r.tag) ?? r.tag.trim(), r.code.trim()]),
  ),
})

// The built-in interfaces: services the server knows how to ask, switched on and off. Nothing is asked of one until it
// is on; an address, paths and language codes are not for filling in.
function BuiltinSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailyProvider()
  const [error, setError] = useState('')
  return (
    <SettingsSection title={d.builtinTitle} description={d.builtinHelp}>
      <ul className="grid grid-cols-1 gap-2" data-slot="builtin">
        {data.providers.map((p) => (
          <li
            key={p.id}
            data-provider={p.id}
            className="flex min-w-0 items-start justify-between gap-3 rounded-lg border p-3"
          >
            <div className="grid min-w-0 gap-0.5">
              <span className="text-sm font-medium break-words">{p.name}</span>
              <span className="text-xs break-words text-muted-foreground">
                {d.builtinLanguages(p.languages.join(', '))}
              </span>
              <span className="text-xs break-words text-muted-foreground">{p.note}</span>
              <a
                href={p.site}
                target="_blank"
                rel="noreferrer noopener"
                className="text-xs break-all text-muted-foreground underline"
              >
                {d.builtinSite}: {p.site}
              </a>
            </div>
            <Switch
              checked={p.enabled}
              disabled={save.isPending}
              aria-label={d.builtinOn(p.name)}
              onCheckedChange={(on) => save.mutate({ id: p.id, enabled: on }, { onError: (e) => setError(message(e)) })}
            />
          </li>
        ))}
      </ul>
      <FormMessage>{error}</FormMessage>
    </SettingsSection>
  )
}

function ApisSection({ data }: { data: AdminDaily }) {
  // The interface being made or edited: its id, or `new` with a form to start from.
  const [open, setOpen] = useState<{ id: string; form: Form } | null>(null)
  // The built-in interfaces have their switches above; this list is those the administrators wrote.
  const custom = data.apis.filter((a) => !a.builtin)
  return (
    <SettingsSection title={d.apisTitle} description={d.apisHelp}>
      {custom.length === 0 && !open && <p className="text-sm break-words text-muted-foreground">{d.noApis}</p>}
      <ul className="grid grid-cols-1 gap-2">
        {custom.map((a) => (
          <li key={a.id} data-api={a.id} className="rounded-lg border">
            <button
              type="button"
              aria-expanded={open?.id === a.id}
              onClick={() => setOpen(open?.id === a.id ? null : { id: a.id, form: formOf(a) })}
              className="flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2.5 text-left outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              <span className="min-w-0">
                <span className="block truncate text-sm font-medium">{a.name}</span>
                <span className="block truncate font-mono text-xs text-muted-foreground">
                  {a.id} · {a.lang || d.allLanguages}
                </span>
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">{a.enabled ? d.enabledOn : d.enabledOff}</span>
            </button>
            {open?.id === a.id && (
              <div className="border-t p-3">
                <ApiEditor
                  key={JSON.stringify(a)}
                  initial={open.form}
                  existing
                  languages={data.languages}
                  known={data.knownLanguages}
                  onClose={() => setOpen(null)}
                />
              </div>
            )}
          </li>
        ))}
      </ul>
      {open?.id === 'new' && (
        <div className="rounded-lg border p-3">
          <ApiEditor
            initial={open.form}
            existing={false}
            languages={data.languages}
            known={data.knownLanguages}
            onClose={() => setOpen(null)}
          />
        </div>
      )}
      {open?.id !== 'new' && (
        <div className="grid grid-cols-1 gap-2">
          <div>
            <Button variant="outline" size="sm" onClick={() => setOpen({ id: 'new', form: blankForm() })}>
              <Plus />
              {d.addApi}
            </Button>
          </div>
        </div>
      )}
    </SettingsSection>
  )
}

function ApiEditor({
  initial,
  existing,
  languages,
  known,
  onClose,
}: {
  initial: Form
  existing: boolean
  languages: string[]
  known: readonly KnownLanguage[]
  onClose: () => void
}) {
  const [f, setF] = useState<Form>(initial)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [result, setResult] = useState<DailyTryResult | null>(null)
  const [tryLang, setTryLang] = useState(initial.lang || languages[0] || 'en')
  const save = useSaveDailyApi()
  const del = useDeleteDailyApi()
  const tryIt = useTryDailyApi()
  // The saved values go with the saved address: with another one they are typed again.
  const urlChanged = f.url !== initial.url
  const set = (patch: Partial<Form>) => setF((x) => ({ ...x, ...patch }))
  const setHeader = (i: number, patch: Partial<HeaderRow>) =>
    setF((x) => ({ ...x, headers: x.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) }))
  const langs = ['', ...languages, ...(f.lang !== '' && !languages.includes(f.lang) ? [f.lang] : [])]
  const setMapRow = (i: number, patch: Partial<MapRow>) =>
    setF((x) => ({ ...x, langMap: x.langMap.map((r, j) => (j === i ? { ...r, ...patch } : r)) }))
  // `required` marks the label with a star and the input aria-required; the others say (optional).
  const field = (
    key: 'id' | 'name' | 'url' | 'text' | 'source' | 'author' | 'link',
    label: string,
    required: boolean,
    help?: string,
    extra?: Partial<React.ComponentProps<'input'>>,
  ) => (
    <div className="grid min-w-0 grid-cols-1 gap-1.5">
      <Label htmlFor={`api-${key}`}>
        {label}
        {required ? <RequiredMark /> : <OptionalMark />}
      </Label>
      <Input
        id={`api-${key}`}
        value={f[key]}
        onChange={(e) => set({ [key]: e.target.value })}
        autoComplete="off"
        aria-required={required ? 'true' : undefined}
        className="w-full min-w-0"
        {...extra}
      />
      {help && <p className="text-xs break-words text-muted-foreground">{help}</p>}
    </div>
  )

  return (
    <form
      className="grid min-w-0 grid-cols-1 gap-4"
      data-slot="api-form"
      onSubmit={(e) => {
        e.preventDefault()
        setError('')
        const missing = urlChanged ? f.headers.find((h) => h.set && h.name.trim() !== '' && h.value === '') : undefined
        if (missing) return setError(d.headerNeedsValue(missing.name))
        save.mutate(payloadOf(f, initial.url), {
          onSuccess: () => {
            toast.success(d.saved)
            onClose()
          },
          onError: (err) => setError(message(err)),
        })
      }}
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {field('id', d.fieldId, true, d.fieldIdHelp, { readOnly: existing })}
        {field('name', d.fieldName, true)}
      </div>
      <div className="grid min-w-0 grid-cols-1 gap-1.5" data-slot="api-lang">
        <Label htmlFor="api-lang">
          {d.fieldLang} <OptionalMark />
        </Label>
        <div className="flex flex-wrap items-center gap-2">
          <span className="rounded-full border px-2.5 py-0.5 text-sm break-all" data-slot="api-lang-value">
            {f.lang === '' ? d.allLanguages : f.lang}
          </span>
          {f.lang !== '' && (
            <Button type="button" variant="ghost" size="xs" onClick={() => set({ lang: '' })}>
              {d.allLanguages}
            </Button>
          )}
        </div>
        <LanguagePicker id="api-lang" known={known} onPick={(tag) => set({ lang: tag })} />
        <p className="text-xs break-words text-muted-foreground">{d.fieldLangHelp}</p>
      </div>
      {field('url', d.fieldUrl, true, d.fieldUrlHelp)}
      <fieldset className="grid min-w-0 grid-cols-1 gap-2" data-slot="api-langmap">
        <legend className="text-sm font-medium">
          {d.fieldLangMap} <OptionalMark />
        </legend>
        <p className="text-xs break-words text-muted-foreground">{d.fieldLangMapHelp}</p>
        {f.langMap.map((r, i) => (
          <div key={i} className="flex flex-wrap items-center gap-2" data-slot="api-langmap-row">
            <Input
              aria-label={d.langMapTag}
              placeholder="zh-Hans"
              value={r.tag}
              onChange={(e) => setMapRow(i, { tag: e.target.value })}
              className="w-full min-w-0 sm:w-40"
              autoComplete="off"
            />
            <span aria-hidden="true">→</span>
            <Input
              aria-label={d.langMapCode}
              placeholder="zh"
              value={r.code}
              onChange={(e) => setMapRow(i, { code: e.target.value })}
              className="w-full min-w-0 sm:w-40"
              autoComplete="off"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={d.langMapRemove}
              title={d.langMapRemove}
              onClick={() => setF((x) => ({ ...x, langMap: x.langMap.filter((_, j) => j !== i) }))}
            >
              <Trash2 />
            </Button>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setF((x) => ({ ...x, langMap: [...x.langMap, { tag: '', code: '' }] }))}
          >
            <Plus />
            {d.langMapAdd}
          </Button>
        </div>
      </fieldset>

      <fieldset className="grid min-w-0 grid-cols-1 gap-2">
        <legend className="text-sm font-medium">
          {d.headers} <OptionalMark />
        </legend>
        <p className="text-xs break-words text-muted-foreground">{d.headersHelp}</p>
        {urlChanged && f.headers.some((h) => h.set) && (
          <p className="text-xs break-words text-muted-foreground" data-slot="header-again">
            {d.headerAgain}
          </p>
        )}
        {f.headers.map((h, i) => (
          <div key={i} className="flex flex-wrap items-center gap-2" data-slot="api-header">
            <Input
              aria-label={d.headerName}
              placeholder="X-Api-Key"
              value={h.name}
              readOnly={h.set}
              onChange={(e) => setHeader(i, { name: e.target.value })}
              className="w-full min-w-0 sm:w-44"
              autoComplete="off"
            />
            {h.set && !h.changing && !urlChanged ? (
              <>
                <span className="text-sm text-muted-foreground">{d.headerSet}</span>
                <Button type="button" variant="outline" size="xs" onClick={() => setHeader(i, { changing: true })}>
                  {d.headerChange}
                </Button>
              </>
            ) : (
              <Input
                aria-label={d.headerValue}
                type="password"
                placeholder={h.set && !urlChanged ? d.headerKeep : d.headerValue}
                value={h.value}
                onChange={(e) => setHeader(i, { value: e.target.value })}
                className="w-full min-w-0 sm:w-60"
                autoComplete="off"
              />
            )}
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={d.headerRemove}
              title={d.headerRemove}
              onClick={() => setF((x) => ({ ...x, headers: x.headers.filter((_, j) => j !== i) }))}
            >
              <Trash2 />
            </Button>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              setF((x) => ({ ...x, headers: [...x.headers, { name: '', set: false, value: '', changing: false }] }))
            }
          >
            <Plus />
            {d.headerAdd}
          </Button>
        </div>
      </fieldset>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        {field('text', d.fieldText, true, d.fieldTextHelp)}
        {field('source', d.fieldSource, false)}
        {field('author', d.fieldAuthor, false)}
      </div>
      {field('link', d.fieldLink, false, `${d.fieldLinkHelp} ${d.fieldLinkNoHeaders}`)}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <div className="grid min-w-0 gap-1.5">
          <Label htmlFor="api-refresh">{d.fieldRefresh}</Label>
          <NativeSelect
            id="api-refresh"
            value={f.refresh}
            onChange={(e) => set({ refresh: e.target.value as Form['refresh'] })}
          >
            <option value="daily">{d.refreshDaily}</option>
            <option value="hourly">{d.refreshHourly}</option>
            <option value="minutes">{d.refreshMinutes}</option>
          </NativeSelect>
        </div>
        {f.refresh === 'minutes' && (
          <div className="grid gap-1.5">
            <Label htmlFor="api-minutes">{d.fieldMinutes}</Label>
            <Input
              id="api-minutes"
              type="number"
              min={1}
              max={1440}
              value={f.refreshMinutes}
              onChange={(e) => set({ refreshMinutes: Number(e.target.value) })}
            />
          </div>
        )}
        <div className="grid gap-1.5">
          <Label htmlFor="api-rps">{d.fieldRps}</Label>
          <Input
            id="api-rps"
            type="number"
            min={0.01}
            max={100}
            step="any"
            value={f.rps}
            onChange={(e) => set({ rps: Number(e.target.value) })}
          />
          <p className="text-xs break-words text-muted-foreground">{d.fieldRpsHelp}</p>
        </div>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Switch checked={f.skipNew} onCheckedChange={(on) => set({ skipNew: on })} aria-label={d.fieldSkipNew} />
        <span>
          {d.fieldSkipNew}
          <span className="block text-xs break-words text-muted-foreground">{d.fieldSkipNewHelp}</span>
        </span>
      </label>
      <label className="flex items-center gap-2 text-sm">
        <Switch checked={f.enabled} onCheckedChange={(on) => set({ enabled: on })} aria-label={d.fieldEnabled} />
        {d.fieldEnabled}
      </label>

      <div className="grid min-w-0 grid-cols-1 gap-2 rounded-lg border bg-muted/30 p-3" data-slot="api-try">
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect
            aria-label={d.tryLang}
            value={tryLang}
            onChange={(e) => setTryLang(e.target.value)}
            className="w-32"
          >
            {langs
              .filter((l) => l !== '')
              .map((l) => (
                <option key={l} value={l}>
                  {l}
                </option>
              ))}
          </NativeSelect>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={tryIt.isPending}
            onClick={() =>
              tryIt.mutate(
                { api: payloadOf(f, initial.url), lang: tryLang, date: dayOf(new Date()).date },
                { onSuccess: setResult, onError: (e) => setResult({ ok: false, message: message(e) }) },
              )
            }
          >
            {tryIt.isPending && <Spinner />}
            {tryIt.isPending ? d.trying : d.tryButton}
          </Button>
        </div>
        {result && <TryResult result={result} />}
      </div>

      <FormMessage>{error}</FormMessage>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={save.isPending}>
          {save.isPending && <Spinner />}
          {d.save}
        </Button>
        <Button type="button" variant="outline" onClick={onClose}>
          {d.cancel}
        </Button>
        {existing &&
          (confirming ? (
            <Button
              type="button"
              variant="destructive"
              disabled={del.isPending}
              onClick={() =>
                del.mutate(f.id, {
                  onSuccess: () => {
                    toast.success(d.deleted)
                    onClose()
                  },
                  onError: (e) => setError(message(e)),
                })
              }
            >
              {d.deleteConfirm}
            </Button>
          ) : (
            <Button
              type="button"
              variant="ghost"
              className="ml-auto text-destructive"
              onClick={() => setConfirming(true)}
            >
              <Trash2 />
              {d.delete}
            </Button>
          ))}
      </div>
    </form>
  )
}

// What the interface answered, as it came and as the paths read it; a path that found nothing in the text says so.
export function TryResult({ result }: { result: DailyTryResult }) {
  const q = result.quote
  const rows: [string, string | undefined, string][] = [
    [d.fieldText, q?.text, 'text'],
    [d.fieldSource, q?.source, 'source'],
    [d.fieldAuthor, q?.author, 'author'],
    [d.fieldLink, q?.link, 'link'],
  ]
  const errors = result.pathErrors ?? []
  const ok = result.ok
  return (
    <div className="grid min-w-0 grid-cols-1 gap-2 text-sm" data-slot="try-result" role="status">
      <p className={`break-words ${ok ? 'text-green-700 dark:text-green-400' : 'text-destructive'}`}>
        {ok
          ? d.tryOk(result.status ?? 200)
          : (localised(result.error ? { ...result, code: result.error } : undefined) ?? result.message ?? d.tryFailed)}
      </p>
      {result.url && <p className="font-mono text-xs break-all text-muted-foreground">{result.url}</p>}
      {result.raw !== undefined && (
        <div className="min-w-0">
          <p className="text-xs font-medium">{d.tryRaw}</p>
          <pre className="max-h-48 max-w-full overflow-auto rounded-md border bg-background p-2 text-xs whitespace-pre">
            {result.raw}
            {result.rawTruncated ? ' …' : ''}
          </pre>
        </div>
      )}
      {(q || errors.length > 0) && (
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
          {rows.map(([label, value, key]) => {
            const pe = errors.find((x) => x.field === key)
            const err = pe ? (localised(pe) ?? pe.message) : undefined
            if (value === undefined && !err) return null
            return (
              <div key={key} className="contents">
                <dt className="text-muted-foreground">{label}</dt>
                <dd className={`break-words ${err ? 'text-destructive' : ''}`}>{err ?? value}</dd>
              </div>
            )
          })}
        </dl>
      )}
    </div>
  )
}
