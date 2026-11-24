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
import { ListButtons, moved, OptionChoices } from './DailyChoices'

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
    for (const tab of tabsOf([], saved, prefs))
      if (!d[tab] && apisOfTab(prefs, tab).some((a) => prefs?.apis.includes(a.id)))
        d[tab] = { ...emptyDraft(), mode: 'api' }
    return d
  })
  // The chosen interfaces, in order (the order of one language's are the order they are tried in).
  const [apis, setApis] = useState<string[]>(() => [...(prefs?.apis ?? [])])
  const [options, setOptions] = useState<DailyOptionChoice>(() => structuredClone(prefs?.options ?? {}))
  const [inherit, setInherit] = useState(prefs?.inherit ?? true)
  const known = useMemo(() => prefs?.languages ?? DEFAULT_LANGUAGES, [prefs])
  const tabs = useMemo(() => tabsOf(known, saved, prefs), [known, saved, prefs])
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
        // An interface only counts on a language whose choice is "an interface".
        const keep = apis.filter((id) => {
          const owner = tabs.find((c) => apisOfTab(prefs, c).some((a) => a.id === id))
          return owner === undefined || (drafts[owner]?.mode ?? 'default') === 'api'
        })
        const keptOptions = Object.fromEntries(Object.entries(options).filter(([id]) => keep.includes(id)))
        if (
          inherit !== prefs.inherit ||
          JSON.stringify(keep) !== JSON.stringify(prefs.apis) ||
          JSON.stringify(keptOptions) !== JSON.stringify(prefs.options ?? {})
        )
          await savePrefs.mutateAsync({
            inherit,
            apis: keep,
            ...(Object.keys(keptOptions).length > 0 ? { options: keptOptions } : {}),
          })
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
              {tabApis.map((a) => {
                const chosen = apis.includes(a.id)
                // The order of the chosen ones of this tab.
                const mine = apis.filter((id) => tabApis.some((x) => x.id === id))
                const at = mine.indexOf(a.id)
                const shift = (by: -1 | 1) => {
                  const next = moved(mine, at, by)
                  let k = 0
                  setApis((all) => all.map((id) => (mine.includes(id) ? next[k++] : id)))
                }
                return (
                  <div key={a.id} className="grid gap-1.5" data-api-item={a.id}>
                    <div className="flex items-center justify-between gap-2">
                      <label className="flex items-center gap-2 text-sm">
                        <input
                          type="checkbox"
                          checked={chosen}
                          disabled={busy}
                          onChange={(e) =>
                            setApis((prev) => (e.target.checked ? [...prev, a.id] : prev.filter((id) => id !== a.id)))
                          }
                        />
                        {th.settings.apiFor(a.name, a.lang)}
                      </label>
                      {chosen && mine.length > 1 && (
                        <ListButtons
                          name={a.name}
                          first={at === 0}
                          last={at === mine.length - 1}
                          onUp={() => shift(-1)}
                          onDown={() => shift(1)}
                          words={th.settings.choice}
                          disabled={busy}
                        />
                      )}
                    </div>
                    {chosen && (a.options?.length ?? 0) > 0 && (
                      <div className="ml-6">
                        <OptionChoices
                          options={a.options!}
                          chosen={options[a.id]}
                          owner={a.id}
                          words={th.settings.choice}
                          disabled={busy}
                          onChange={(next) =>
                            setOptions((all) => {
                              const out = { ...all }
                              if (Object.keys(next).length === 0) delete out[a.id]
                              else out[a.id] = next
                              return out
                            })
                          }
                        />
                      </div>
                    )}
                  </div>
                )
              })}
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
