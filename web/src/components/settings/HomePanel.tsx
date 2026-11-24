import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import {
  useDailyPrefs,
  useDailySettings,
  useSaveDailyPrefs,
  useSaveDailySettings,
  useTags,
  useTree,
  useTypes,
} from '@/api/hooks'
import type { DailyOptionChoice, DailyPrefs, DailySettings } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import {
  apisOfTab,
  listsFromPrefs,
  prefsPayload,
  type SpaceItem,
  draftsFrom,
  emptyDraft,
  settingsFrom,
  tabsOf,
  type Drafts,
  type LangDraft,
  type Mode,
} from '@/lib/dailySettings'
import { th } from '@/i18n/home'
import { SettingsNote, SettingsRow, SettingsRows, SettingsSection } from './SettingsParts'
import { SourceFields, type SourceChoices } from './SourceFields'
import { SourceList } from './SourceList'

// The "Home" section: where the text of the day at the top of the space's home page comes from, one page per
// language (the first is "all languages", which every language without a page of its own falls back to). For each:
// pages of this space (a folder, a tag or a type, with filters), an interface the administrators set up, or nothing,
// in which case the server's default is used when the space is set to take it. For those who manage the space; it is
// a chunk of its own, loaded when the section is opened.
// The languages with a tab when the server gives none (single-user mode): canonical language tags.
const DEFAULT_LANGUAGES = ['zh-Hans', 'en']

export default function HomePanel({ canManage }: { canManage: boolean }) {
  const settings = useDailySettings()
  const prefs = useDailyPrefs()
  const tree = useTree()
  const tags = useTags()
  const types = useTypes()
  if (!canManage) return <SettingsNote>{th.settings.noManage}</SettingsNote>
  if (settings.isPending || prefs.isPending) return <PageFallback />
  if (settings.isError || prefs.isError)
    return (
      <ErrorState
        message={th.settings.loadFailed}
        onRetry={() => void (settings.isError ? settings.refetch() : prefs.refetch())}
      />
    )
  const choices: SourceChoices = {
    folders: (tree.data?.folders ?? []).map((f) => f.path).filter((p) => p !== ''),
    tags: (tags.data?.tags ?? []).map((x) => x.tag),
    types: (types.data?.types ?? []).filter((x) => !x.error).map((x) => ({ name: x.name, title: x.title })),
  }
  // Made once, from what the server had on first load: a save goes in two requests, and starting the editor again
  // between them would lose the choices not yet saved, the tab and the error of the second.
  return <Editor saved={settings.data.daily} prefs={prefs.data} choices={choices} />
}

function Editor({
  saved,
  prefs,
  choices,
}: {
  saved: DailySettings | null
  prefs: DailyPrefs | null
  choices: SourceChoices
}) {
  const saveSettings = useSaveDailySettings()
  const savePrefs = useSaveDailyPrefs()
  const [drafts, setDrafts] = useState<Drafts>(() => {
    const d = draftsFrom(saved)
    // A language whose interface is chosen shows as such.
    const chosen = listsFromPrefs(prefs, tabsOf(prefs?.languages ?? DEFAULT_LANGUAGES, saved, prefs))
    for (const [tab, list] of Object.entries(chosen))
      if (!d[tab] && list.length > 0) d[tab] = { ...emptyDraft(), mode: 'api' }
    return d
  })
  const known = useMemo(() => prefs?.languages ?? DEFAULT_LANGUAGES, [prefs])
  const tabs = useMemo(() => tabsOf(known, saved, prefs), [known, saved, prefs])
  // The sources of each language in order: interfaces, the administrators' lines, pages of this space. With more than
  // one they take turns by day, and one that gives nothing is passed over for the next.
  const [lists, setLists] = useState<Record<string, SpaceItem[]>>(() => listsFromPrefs(prefs, tabs))
  const [options, setOptions] = useState<DailyOptionChoice>(() => {
    const o: DailyOptionChoice = structuredClone(prefs?.options ?? {})
    for (const list of Object.values(prefs?.sources ?? {}))
      for (const src of list) if (src.kind === 'api' && src.options) o[src.id] = structuredClone(src.options)
    return o
  })
  const [inherit, setInherit] = useState(prefs?.inherit ?? true)
  const [tab, setTab] = useState('')
  const [error, setError] = useState('')
  const busy = saveSettings.isPending || savePrefs.isPending
  const draft: LangDraft = drafts[tab] ?? emptyDraft()
  const set = (patch: Partial<LangDraft>) =>
    setDrafts((d) => ({ ...d, [tab]: { ...(d[tab] ?? emptyDraft()), ...patch } }))
  const tabName = (code: string) => (code === '' ? th.settings.all : code)

  const save = async () => {
    if (busy) return
    setError('')
    for (const [lang, d] of Object.entries(drafts)) {
      if (d.mode === 'own' && d.value === '') {
        setTab(lang)
        return setError(`${tabName(lang)}: ${th.settings.chooseFirst}`)
      }
    }
    try {
      const next = settingsFrom(drafts)
      if (JSON.stringify(next) !== JSON.stringify(saved)) await saveSettings.mutateAsync(next)
      if (prefs) {
        // A list only counts on a language whose choice is "an interface".
        const taken = tabs.filter((t) => (drafts[t]?.mode ?? 'default') === 'api')
        for (const t of taken) {
          const bad = (lists[t] ?? []).some((i) => i.kind === 'own' && i.draft.value === '')
          if (bad) {
            setTab(t)
            return setError(`${tabName(t)}: ${th.settings.chooseFirst}`)
          }
        }
        const payload = prefsPayload(lists, options, taken)
        const before = prefsPayload(
          listsFromPrefs(prefs, tabs),
          Object.fromEntries(Object.entries(prefs.options ?? {})),
          tabs,
        )
        if (inherit !== prefs.inherit || JSON.stringify(payload) !== JSON.stringify(before))
          await savePrefs.mutateAsync({ inherit, ...payload })
      }
      toast.success(th.settings.saved)
    } catch (e) {
      setError(
        e instanceof ApiError && (e.code === 'invalid' || e.code === 'not_found')
          ? e.message
          : `${th.settings.failed}${e instanceof Error && e.message ? `: ${e.message}` : ''}`,
      )
    }
  }

  const modes: Mode[] = prefs ? ['default', 'own', 'api'] : ['default', 'own']
  // Set for the whole server by the administrators: the choice is shown, not changeable.
  const locked = (m: Mode) =>
    m === 'own' ? prefs?.allowed?.own === false : m === 'api' ? prefs?.allowed?.api === false : false
  const tabApis = apisOfTab(prefs, tab)
  return (
    <div className="space-y-8">
      <SettingsSection title={th.title} description={th.settings.help}>
        <div role="tablist" aria-label={th.settings.tabs} className="flex flex-wrap gap-1">
          {tabs.map((code) => (
            <button
              key={code || 'all'}
              type="button"
              role="tab"
              aria-selected={tab === code}
              data-lang={code}
              onClick={() => setTab(code)}
              className={`inline-flex h-8 items-center rounded-lg px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${tab === code ? 'bg-muted font-medium' : 'text-muted-foreground hover:text-foreground'}`}
            >
              {tabName(code)}
              {(drafts[code]?.mode ?? 'default') !== 'default' && (
                <span aria-hidden="true" className="ml-1.5 size-1.5 rounded-full bg-brand" />
              )}
            </button>
          ))}
        </div>

        <div role="tabpanel" aria-label={tabName(tab)} className="grid gap-4 rounded-lg border p-4">
          <div role="radiogroup" aria-label={th.settings.sourceLabel} className="grid gap-2">
            {modes.map((m) => (
              <label key={m} className="flex items-center gap-2 text-sm">
                <input
                  type="radio"
                  name="daily-mode"
                  checked={draft.mode === m}
                  onChange={() => set({ mode: m })}
                  disabled={busy || locked(m)}
                />
                {th.settings.modes[m]}
                {locked(m) && <span className="text-xs text-muted-foreground">{th.settings.lockedByAdmin}</span>}
              </label>
            ))}
          </div>
          {draft.mode === 'default' && (
            <p className="text-xs text-muted-foreground">{prefs ? th.settings.defaultHelp : ''}</p>
          )}
          {draft.mode === 'own' && (
            <>
              <p className="text-xs text-muted-foreground">{th.settings.ownHelp}</p>
              <SourceFields draft={draft} onChange={(d) => set(d)} choices={choices} disabled={busy} />
            </>
          )}
          {draft.mode === 'api' && (
            <div className="grid gap-2" data-slot="daily-apis">
              <p className="text-xs text-muted-foreground">{th.settings.apisHelp}</p>
              {tabApis.length > 1 && <p className="text-xs text-muted-foreground">{th.settings.apisOrderHelp}</p>}
              {tabApis.length === 0 && <p className="text-sm text-muted-foreground">{th.settings.noApis}</p>}
              <SourceList
                items={lists[tab] ?? []}
                onItems={(next) => setLists((all) => ({ ...all, [tab]: next }))}
                available={tabApis}
                options={options}
                onOptions={setOptions}
                choices={choices}
                canOwn={prefs?.allowed?.own !== false}
                apiLocked={prefs?.allowed?.api === false}
                disabled={busy}
                owner={tab || 'all'}
              />
            </div>
          )}
        </div>
      </SettingsSection>

      {prefs && (
        <SettingsSection title={th.settings.inheritTitle}>
          <SettingsRows>
            <SettingsRow
              title={th.settings.inherit}
              help={prefs.default ? th.settings.inheritHelp : th.settings.noDefault}
              controlId="daily-inherit"
            >
              <Switch id="daily-inherit" checked={inherit} disabled={busy} onCheckedChange={setInherit} />
            </SettingsRow>
          </SettingsRows>
        </SettingsSection>
      )}

      <FormMessage>{error}</FormMessage>
      <div>
        <Button onClick={() => void save()} disabled={busy}>
          {busy && <Spinner />}
          {busy ? th.settings.saving : th.settings.save}
        </Button>
      </div>
    </div>
  )
}
