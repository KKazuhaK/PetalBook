import { useMemo, useState } from 'react'
import { Plus, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAdminDaily, useAdminSpaces, useDeleteDailyApi, useDeleteDailyLines, useSaveDailyApi, useSaveDailyLines, useSaveDailySite, useTryDailyApi } from '@/api/admin'
import type { AdminDaily, DailyApi, DailyDefault, DailySettings, DailyTryResult, LinesFormat } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsRow, SettingsRows, SettingsSection } from '@/components/settings/SettingsParts'
import { SourceFields } from '@/components/settings/SourceFields'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { draftsFrom, emptyDraft, settingsFrom, tabsOf, type Drafts } from '@/lib/dailySettings'
import { dayOf } from '@/api/hooks'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'

const d = ta.daily
const message = (e: unknown) => (e instanceof ApiError && (e.code === 'invalid' || e.code === 'not_found' || e.code === 'rate_limited') ? e.message : t.fmt.labelled(d.failed, e instanceof Error ? e.message : ''))

// What administrators decide about the daily text of every space: what spaces may do, the languages, the default,
// the pasted lines, the hosts the server may fetch from, and the external interfaces (any number, with a button to
// try each). It is a chunk of its own, loaded by the administration.
export function DailyPanel() {
  const q = useAdminDaily()
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={t.fmt.labelled(d.loadFailed, q.error.message)} onRetry={() => void q.refetch()} />
  const data = q.data
  return (
    <div className="space-y-10" data-slot="admin-daily">
      <p className="text-sm text-muted-foreground">{d.help}</p>
      <AllowSection data={data} />
      <LanguagesSection data={data} />
      <DefaultSection data={data} />
      <LinesSection data={data} />
      <HostsSection key={data.hosts.join("\n")} data={data} />
      <ApisSection data={data} />
    </div>
  )
}

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
          <Switch id="daily-allow-own" checked={allow?.own ?? true} disabled={!allow || save.isPending} onCheckedChange={(on) => set({ own: on })} />
        </SettingsRow>
        <SettingsRow title={d.allowApi} help={d.allowApiHelp} controlId="daily-allow-api">
          <Switch id="daily-allow-api" checked={allow?.api ?? true} disabled={!allow || save.isPending} onCheckedChange={(on) => set({ api: on })} />
        </SettingsRow>
      </SettingsRows>
    </SettingsSection>
  )
}

function LanguagesSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailySite()
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const write = (languages: string[]) => save.mutate({ languages }, { onSuccess: () => setError(''), onError: (e) => setError(message(e)) })
  return (
    <SettingsSection title={d.languagesTitle} description={d.languagesHelp}>
      <ul className="flex flex-wrap gap-1.5" aria-label={d.languagesTitle}>
        <li className="rounded-full border px-2.5 py-0.5 text-sm text-muted-foreground">{d.allLanguages}</li>
        {data.languages.map((l) => (
          <li key={l} className="flex items-center gap-1 rounded-full border py-0.5 pr-1 pl-2.5 text-sm">
            {l}
            <button type="button" aria-label={d.removeLanguage(l)} title={d.removeLanguage(l)} onClick={() => write(data.languages.filter((x) => x !== l))} className="rounded-full p-0.5 hover:bg-muted">
              <X className="size-3" />
            </button>
          </li>
        ))}
      </ul>
      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          const c = code.trim().toLowerCase()
          if (c === '' || data.languages.includes(c)) return
          write([...data.languages, c])
          setCode('')
        }}
      >
        <div className="grid gap-1.5">
          <Label htmlFor="daily-lang">{d.languageCode}</Label>
          <Input id="daily-lang" value={code} onChange={(e) => setCode(e.target.value)} placeholder="pt-br" className="w-40" autoComplete="off" />
        </div>
        <Button type="submit" variant="outline" size="sm" disabled={save.isPending}>
          <Plus />
          {d.addLanguage}
        </Button>
      </form>
      <FormMessage>{error}</FormMessage>
    </SettingsSection>
  )
}

// A tab per language, "all languages" first.
function LangTabs({ langs, value, onChange, label }: { langs: string[]; value: string; onChange: (l: string) => void; label: string }) {
  return (
    <div role="tablist" aria-label={label} className="flex flex-wrap gap-1">
      {langs.map((l) => (
        <button key={l || 'all'} type="button" role="tab" aria-selected={value === l} data-lang={l} onClick={() => onChange(l)} className={`inline-flex h-8 items-center rounded-lg px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${value === l ? 'bg-muted font-medium' : 'text-muted-foreground hover:text-foreground'}`}>
          {l === '' ? d.allLanguages : l}
        </button>
      ))}
    </div>
  )
}

function DefaultSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailySite()
  const spaces = useAdminSpaces('')
  const initial = data.default
  const [kind, setKind] = useState<'none' | 'list' | 'space'>(initial === null ? 'none' : initial.kind)
  const [space, setSpace] = useState(initial?.kind === 'space' ? initial.space : '')
  const [drafts, setDrafts] = useState<Drafts>(() => (initial?.kind === 'space' ? draftsFrom(initial.source) : {}))
  const [tab, setTab] = useState('')
  const [error, setError] = useState('')
  const tabs = useMemo(() => tabsOf(data.languages, initial?.kind === 'space' ? initial.source : null, null), [data.languages, initial])
  const draft = drafts[tab] ?? { ...emptyDraft(), mode: 'own' as const }
  const items = spaces.data?.pages.flatMap((p) => p.items) ?? []

  const submit = () => {
    setError('')
    let value: DailyDefault = null
    if (kind === 'list') value = { kind: 'list' }
    if (kind === 'space') {
      const source = settingsFrom(Object.fromEntries(Object.entries(drafts).map(([k, v]) => [k, { ...v, mode: 'own' as const }])))
      if (space === '' || !source) return setError(d.defaultSpaceIncomplete)
      value = { kind: 'space', space, source: source as DailySettings }
    }
    save.mutate({ default: value }, { onSuccess: () => toast.success(d.saved), onError: (e) => setError(message(e)) })
  }
  return (
    <SettingsSection title={d.defaultTitle} description={d.defaultHelp}>
      <div role="radiogroup" aria-label={d.defaultTitle} className="grid gap-2">
        {(['none', 'list', 'space'] as const).map((k) => (
          <label key={k} className="flex items-center gap-2 text-sm">
            <input type="radio" name="daily-default" checked={kind === k} onChange={() => setKind(k)} />
            {d.defaultKinds[k]}
          </label>
        ))}
      </div>
      {kind === 'list' && <p className="text-xs text-muted-foreground">{d.defaultListHelp}</p>}
      {kind === 'space' && (
        <div className="grid gap-4 rounded-lg border p-4">
          <FormMessage tone="info">{d.spaceWarning}</FormMessage>
          <div className="grid gap-1.5">
            <Label htmlFor="daily-default-space">{d.spaceLabel}</Label>
            <NativeSelect id="daily-default-space" value={space} onChange={(e) => setSpace(e.target.value)}>
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
          <p className="text-xs text-muted-foreground">{d.sourceHelp}</p>
          <SourceFields draft={draft} onChange={(next) => setDrafts((all) => ({ ...all, [tab]: { ...(all[tab] ?? emptyDraft()), ...next, mode: 'own' } }))} />
          {drafts[tab] && (
            <div>
              <Button type="button" variant="ghost" size="sm" onClick={() =>
                  setDrafts((all) => {
                    const next = { ...all }
                    delete next[tab]
                    return next
                  })
                }>
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
  const tabs = useMemo(() => ['', ...data.languages, ...Object.keys(data.lines.byLang).filter((l) => l !== '' && !data.languages.includes(l))], [data])
  const [tab, setTab] = useState('')
  const [text, setText] = useState('')
  const [format, setFormat] = useState<LinesFormat>('lines')
  const [error, setError] = useState('')
  const count = data.lines.byLang[tab] ?? 0
  const send = () => {
    setError('')
    save.mutate({ lang: tab, text, format }, { onSuccess: () => { setText(''); toast.success(d.linesSaved) }, onError: (e) => setError(message(e)) })
  }
  return (
    <SettingsSection title={d.linesTitle} description={d.linesHelp}>
      <p className="text-xs text-muted-foreground">{d.linesTotal(data.lines.total, data.lines.max)}</p>
      <LangTabs langs={tabs} value={tab} onChange={setTab} label={d.linesTitle} />
      <p className="text-sm">{d.linesOfLang(count)}</p>
      <Textarea value={text} onChange={(e) => setText(e.target.value)} aria-label={d.linesText} placeholder={d.linesPlaceholder} className="min-h-28" />
      <div className="flex flex-wrap items-center gap-2">
        <NativeSelect value={format} onChange={(e) => setFormat(e.target.value as LinesFormat)} aria-label={d.linesFormat} className="w-44">
          <option value="lines">{d.formatLines}</option>
          <option value="markdown">{d.formatMarkdown}</option>
        </NativeSelect>
        <label className="inline-flex h-8 cursor-pointer items-center rounded-lg border px-3 text-sm hover:bg-muted">
          {d.linesUpload}
          <input
            type="file"
            accept=".txt,.md,text/plain,text/markdown"
            className="sr-only"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (!f) return
              if (/\.md$/i.test(f.name)) setFormat('markdown')
              void f.text().then(setText)
              e.target.value = ''
            }}
          />
        </label>
        <Button onClick={send} disabled={save.isPending || text.trim() === ''}>
          {save.isPending && <Spinner />}
          {d.linesSave}
        </Button>
        <Button variant="outline" disabled={del.isPending || count === 0} onClick={() => del.mutate(tab, { onSuccess: () => toast.success(d.linesDeleted), onError: (e) => setError(message(e)) })}>
          <Trash2 />
          {d.linesDelete}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </SettingsSection>
  )
}

function HostsSection({ data }: { data: AdminDaily }) {
  const save = useSaveDailySite()
  const [text, setText] = useState(data.hosts.join('\n'))
  const [error, setError] = useState('')
  return (
    <SettingsSection title={d.hostsTitle} description={d.hostsHelp}>
      <Textarea value={text} onChange={(e) => setText(e.target.value)} aria-label={d.hostsTitle} className="min-h-20 font-mono" />
      <FormMessage>{error}</FormMessage>
      <div>
        <Button
          disabled={save.isPending}
          onClick={() => {
            setError('')
            save.mutate({ hosts: text.split('\n').map((h) => h.trim()).filter(Boolean) }, { onSuccess: () => toast.success(d.saved), onError: (e) => setError(message(e)) })
          }}
        >
          {d.save}
        </Button>
      </div>
    </SettingsSection>
  )
}

type HeaderRow = { name: string; set: boolean; value: string; changing: boolean }
type Form = Omit<DailyApi, 'headers' | 'note'> & { headers: HeaderRow[] }

const blankForm = (): Form => ({ id: '', name: '', lang: '', url: 'https://', headers: [], text: '', source: '', author: '', link: '', refresh: 'daily', refreshMinutes: 60, skipNew: true, rps: 1, enabled: true })
const formOf = (a: DailyApi): Form => ({ ...a, headers: (a.headers ?? []).map((h) => ({ name: h.name, set: h.set === true, value: '', changing: false })) })
// What is sent: a header whose value was not touched has only its name, which keeps the saved value.
const payloadOf = (f: Form): Omit<DailyApi, 'note'> => ({
  ...f,
  refreshMinutes: f.refresh === 'minutes' ? f.refreshMinutes : 0,
  headers: f.headers.filter((h) => h.name.trim() !== '').map((h) => (h.set && !h.changing ? { name: h.name.trim() } : { name: h.name.trim(), value: h.value })),
})

function ApisSection({ data }: { data: AdminDaily }) {
  // The interface being made or edited: its id, or `new` with a form to start from.
  const [open, setOpen] = useState<{ id: string; form: Form } | null>(null)
  return (
    <SettingsSection title={d.apisTitle} description={d.apisHelp}>
      {data.apis.length === 0 && !open && <p className="text-sm text-muted-foreground">{d.noApis}</p>}
      <ul className="grid gap-2">
        {data.apis.map((a) => (
          <li key={a.id} data-api={a.id} className="rounded-lg border">
            <button type="button" aria-expanded={open?.id === a.id} onClick={() => setOpen(open?.id === a.id ? null : { id: a.id, form: formOf(a) })} className="flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2.5 text-left outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/50">
              <span className="min-w-0">
                <span className="block truncate text-sm font-medium">{a.name}</span>
                <span className="block truncate font-mono text-xs text-muted-foreground">{a.id} · {a.lang || d.allLanguages}</span>
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">{a.enabled ? d.enabledOn : d.enabledOff}</span>
            </button>
            {open?.id === a.id && (
              <div className="border-t p-3">
                <ApiEditor key={JSON.stringify(a)} initial={open.form} existing languages={data.languages} onClose={() => setOpen(null)} />
              </div>
            )}
          </li>
        ))}
      </ul>
      {open?.id === 'new' && (
        <div className="rounded-lg border p-3">
          <ApiEditor initial={open.form} existing={false} languages={data.languages} onClose={() => setOpen(null)} />
        </div>
      )}
      {open?.id !== 'new' && (
        <div className="grid gap-2">
          <div>
            <Button variant="outline" size="sm" onClick={() => setOpen({ id: 'new', form: blankForm() })}>
              <Plus />
              {d.addApi}
            </Button>
          </div>
          {data.presets.length > 0 && (
            <div className="grid gap-1.5" data-slot="presets">
              <p className="text-xs text-muted-foreground">{d.presetHelp}</p>
              <div className="flex flex-wrap gap-2">
                {data.presets.map((p) => (
                  <Button key={p.id} variant="outline" size="sm" onClick={() => setOpen({ id: 'new', form: formOf({ ...p, enabled: true }) })}>
                    {d.usePreset(p.name)}
                  </Button>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </SettingsSection>
  )
}

function ApiEditor({ initial, existing, languages, onClose }: { initial: Form; existing: boolean; languages: string[]; onClose: () => void }) {
  const [f, setF] = useState<Form>(initial)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [result, setResult] = useState<DailyTryResult | null>(null)
  const [tryLang, setTryLang] = useState(initial.lang || languages[0] || 'en')
  const save = useSaveDailyApi()
  const del = useDeleteDailyApi()
  const tryIt = useTryDailyApi()
  const set = (patch: Partial<Form>) => setF((x) => ({ ...x, ...patch }))
  const setHeader = (i: number, patch: Partial<HeaderRow>) => setF((x) => ({ ...x, headers: x.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) }))
  const langs = ['', ...languages, ...(f.lang !== '' && !languages.includes(f.lang) ? [f.lang] : [])]
  const field = (key: 'id' | 'name' | 'url' | 'text' | 'source' | 'author' | 'link', label: string, help?: string, extra?: Partial<React.ComponentProps<'input'>>) => (
    <div className="grid gap-1.5">
      <Label htmlFor={`api-${key}`}>{label}</Label>
      <Input id={`api-${key}`} value={f[key]} onChange={(e) => set({ [key]: e.target.value })} autoComplete="off" {...extra} />
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  )

  return (
    <form
      className="grid gap-4"
      data-slot="api-form"
      onSubmit={(e) => {
        e.preventDefault()
        setError('')
        save.mutate(payloadOf(f), { onSuccess: () => { toast.success(d.saved); onClose() }, onError: (err) => setError(message(err)) })
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        {field('id', d.fieldId, d.fieldIdHelp, { readOnly: existing })}
        {field('name', d.fieldName)}
      </div>
      <div className="grid gap-1.5 sm:max-w-xs">
        <Label htmlFor="api-lang">{d.fieldLang}</Label>
        <NativeSelect id="api-lang" value={f.lang} onChange={(e) => set({ lang: e.target.value })}>
          {langs.map((l) => (
            <option key={l} value={l}>
              {l === '' ? d.allLanguages : l}
            </option>
          ))}
        </NativeSelect>
      </div>
      {field('url', d.fieldUrl, d.fieldUrlHelp)}

      <fieldset className="grid gap-2">
        <legend className="text-sm font-medium">{d.headers}</legend>
        <p className="text-xs text-muted-foreground">{d.headersHelp}</p>
        {f.headers.map((h, i) => (
          <div key={i} className="flex flex-wrap items-center gap-2" data-slot="api-header">
            <Input aria-label={d.headerName} placeholder="X-Api-Key" value={h.name} readOnly={h.set} onChange={(e) => setHeader(i, { name: e.target.value })} className="w-44" autoComplete="off" />
            {h.set && !h.changing ? (
              <>
                <span className="text-sm text-muted-foreground">{d.headerSet}</span>
                <Button type="button" variant="outline" size="xs" onClick={() => setHeader(i, { changing: true })}>
                  {d.headerChange}
                </Button>
              </>
            ) : (
              <Input aria-label={d.headerValue} type="password" placeholder={d.headerValue} value={h.value} onChange={(e) => setHeader(i, { value: e.target.value })} className="w-60" autoComplete="off" />
            )}
            <Button type="button" variant="ghost" size="icon-sm" aria-label={d.headerRemove} title={d.headerRemove} onClick={() => setF((x) => ({ ...x, headers: x.headers.filter((_, j) => j !== i) }))}>
              <Trash2 />
            </Button>
          </div>
        ))}
        <div>
          <Button type="button" variant="outline" size="sm" onClick={() => setF((x) => ({ ...x, headers: [...x.headers, { name: '', set: false, value: '', changing: false }] }))}>
            <Plus />
            {d.headerAdd}
          </Button>
        </div>
      </fieldset>

      <div className="grid gap-3 sm:grid-cols-3">
        {field('text', d.fieldText, d.fieldTextHelp)}
        {field('source', d.fieldSource)}
        {field('author', d.fieldAuthor)}
      </div>
      {field('link', d.fieldLink, d.fieldLinkHelp)}

      <div className="grid gap-3 sm:grid-cols-3">
        <div className="grid gap-1.5">
          <Label htmlFor="api-refresh">{d.fieldRefresh}</Label>
          <NativeSelect id="api-refresh" value={f.refresh} onChange={(e) => set({ refresh: e.target.value as Form['refresh'] })}>
            <option value="daily">{d.refreshDaily}</option>
            <option value="hourly">{d.refreshHourly}</option>
            <option value="minutes">{d.refreshMinutes}</option>
          </NativeSelect>
        </div>
        {f.refresh === 'minutes' && (
          <div className="grid gap-1.5">
            <Label htmlFor="api-minutes">{d.fieldMinutes}</Label>
            <Input id="api-minutes" type="number" min={1} max={1440} value={f.refreshMinutes} onChange={(e) => set({ refreshMinutes: Number(e.target.value) })} />
          </div>
        )}
        <div className="grid gap-1.5">
          <Label htmlFor="api-rps">{d.fieldRps}</Label>
          <Input id="api-rps" type="number" min={0.01} max={100} step="any" value={f.rps} onChange={(e) => set({ rps: Number(e.target.value) })} />
          <p className="text-xs text-muted-foreground">{d.fieldRpsHelp}</p>
        </div>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Switch checked={f.skipNew} onCheckedChange={(on) => set({ skipNew: on })} aria-label={d.fieldSkipNew} />
        <span>
          {d.fieldSkipNew}
          <span className="block text-xs text-muted-foreground">{d.fieldSkipNewHelp}</span>
        </span>
      </label>
      <label className="flex items-center gap-2 text-sm">
        <Switch checked={f.enabled} onCheckedChange={(on) => set({ enabled: on })} aria-label={d.fieldEnabled} />
        {d.fieldEnabled}
      </label>

      <div className="grid gap-2 rounded-lg border bg-muted/30 p-3" data-slot="api-try">
        <div className="flex flex-wrap items-center gap-2">
          <NativeSelect aria-label={d.tryLang} value={tryLang} onChange={(e) => setTryLang(e.target.value)} className="w-32">
            {langs.filter((l) => l !== '').map((l) => (
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
                { api: payloadOf(f), lang: tryLang, date: dayOf(new Date()).date },
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
            <Button type="button" variant="destructive" disabled={del.isPending} onClick={() => del.mutate(f.id, { onSuccess: () => { toast.success(d.deleted); onClose() }, onError: (e) => setError(message(e)) })}>
              {d.deleteConfirm}
            </Button>
          ) : (
            <Button type="button" variant="ghost" className="ml-auto text-destructive" onClick={() => setConfirming(true)}>
              <Trash2 />
              {d.delete}
            </Button>
          ))}
      </div>
    </form>
  )
}

// What the interface answered, as it came and as the paths read it; a path that found nothing says so.
function TryResult({ result }: { result: DailyTryResult }) {
  const q = result.quote
  const rows: [string, string | undefined, string][] = [
    [d.fieldText, q?.text, 'text'],
    [d.fieldSource, q?.source, 'source'],
    [d.fieldAuthor, q?.author, 'author'],
    [d.fieldLink, q?.link, 'link'],
  ]
  return (
    <div className="grid gap-2 text-sm" data-slot="try-result" role="status">
      <p className={result.ok ? 'text-green-700 dark:text-green-400' : 'text-destructive'}>{result.ok ? d.tryOk(result.status ?? 200) : (result.message ?? d.tryFailed)}</p>
      {result.url && <p className="truncate font-mono text-xs text-muted-foreground">{result.url}</p>}
      {result.raw !== undefined && (
        <div>
          <p className="text-xs font-medium">{d.tryRaw}</p>
          <pre className="max-h-48 overflow-auto rounded-md border bg-background p-2 text-xs whitespace-pre-wrap">{result.raw}{result.rawTruncated ? ' …' : ''}</pre>
        </div>
      )}
      {(q || result.pathErrors) && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
          {rows.map(([label, value, key]) => {
            const err = result.pathErrors?.[key]
            if (value === undefined && !err) return null
            return (
              <div key={key} className="contents">
                <dt className="text-muted-foreground">{label}</dt>
                <dd className={err ? 'text-destructive' : ''}>{err ?? value}</dd>
              </div>
            )
          })}
        </dl>
      )}
    </div>
  )
}
