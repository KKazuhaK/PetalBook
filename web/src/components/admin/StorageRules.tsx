import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Save } from 'lucide-react'
import { toast } from 'sonner'
import { useSaveAdminStorage, useSiteSettings, useStoragePolicy } from '@/api/admin'
import { K } from '@/api/settingsFold'
import { useConnections } from '@/api/hooks'
import { ApiError } from '@/api/client'
import type { AdminStorage, PutAdminStorage } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsRow, SettingsRows, SettingsSection } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { absoluteTime } from '@/lib/time'
import { SiteRow } from './SettingsPanel'
import { t } from '@/i18n'
import { ts } from '@/i18n/storage'

const MIB = 1024 ** 2
const GIB = 1024 ** 3

// The site's storage policy (docs/api.md, "Server storage"): whether spaces must
// follow a remote, the hosts they may be on, the limit on spaces that have none,
// and when the copy of an idle space is removed from the disk. It is the top part
// of the page "存储与同步" (StoragePanel), above the connections.
export function StorageRules() {
  const storage = useStoragePolicy()
  // A refusal in the middle of several writes leaves the ones before it written: the page reads again and the form is
  // made again from what the server has, with what was typed still in it and the refusal above the button.
  const [failure, setFailure] = useState<Failure | null>(null)
  if (storage.isPending) return <PageFallback />
  if (storage.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(ts.admin.loadFailed, storage.error.message)}
        onRetry={() => void storage.refetch()}
      />
    )
  return (
    <StorageForm
      key={`${JSON.stringify(storage.data)}${failure?.id ?? ''}`}
      data={storage.data}
      failure={failure}
      onFailure={setFailure}
    />
  )
}

// What is typed in the form; numbers stay text, so a field can be half typed.
interface Values {
  require: string
  scope: string
  grace: string
  hosts: string
  maxMiB: string
  partial: boolean
  evictDays: string
  idle: string
  clones: string
  free: string
  assetCache: string
}
interface Failure {
  id: number
  message: string
  values: Values
}

// A number field's text, and back: empty means "none" where a setting may be null.
const text = (n: number | null, unit: number) => (n === null ? '' : String(n / unit))
function parse(v: string, unit: number, min: number, nullable: boolean): number | null | undefined {
  const s = v.trim()
  if (s === '') return nullable ? null : undefined
  if (!/^\d+$/.test(s)) return undefined
  const n = Number(s) * unit
  return n >= min ? n : undefined
}

function explain(e: unknown): string {
  if (!(e instanceof ApiError)) return ts.admin.errors.failed
  const known = ts.admin.errors.codes as Record<string, string>
  return Object.hasOwn(known, e.code) ? known[e.code] : e.message || ts.admin.errors.failed
}

// Whether spaces on their own storage are exempt from the site's rule for where attachments go: a layered setting
// like any other, so it keeps its own value and rule (allow, restrict, lock) and its own save, apart from this form.
function OwnExemptRow() {
  const q = useSiteSettings()
  const s = q.data?.settings.find((x) => x.key === K.assetsOwnExempt)
  if (!s) return null
  const a = ts.admin.assets
  return (
    <ul className="divide-y overflow-hidden rounded-lg border">
      <SiteRow
        key={JSON.stringify(s.site)}
        s={s}
        title={a.ownExemptTitle}
        help={a.ownExemptHelp}
        label={a.ownExemptLabel}
      />
    </ul>
  )
}

function StorageForm({
  data,
  failure,
  onFailure,
}: {
  data: AdminStorage
  failure: Failure | null
  onFailure: (f: Failure | null) => void
}) {
  const save = useSaveAdminStorage()
  // A remote through one platform can be required: every provider but a plain address (2.7b's connections), and
  // the stored one even when the server no longer lists it.
  const connections = useConnections()
  const listed = (connections.data?.providers ?? [])
    .filter((p) => p.type !== 'git')
    .map((p) => ({ id: p.id, name: p.name }))
  const stored = data.remotes.require
  const platforms =
    stored && stored !== 'any' && !listed.some((p) => p.id === stored)
      ? [...listed, { id: stored, name: stored }]
      : listed
  const a = ts.admin
  const [v, setV] = useState<Values>(
    () =>
      failure?.values ?? {
        require: data.remotes.require ?? '',
        scope: data.remotes.scope,
        grace: String(data.remotes.graceDays),
        hosts: data.hosts.allowed.join('\n'),
        maxMiB: text(data.localSpaces.maxBytes, MIB),
        partial: data.cache.partial,
        evictDays: text(data.cache.evictAfterDays, 1),
        idle: String(data.cache.minIdleHours),
        clones: text(data.cache.clonesBytes, GIB),
        free: text(data.cache.minFreeBytes, GIB),
        assetCache: text(data.cache.assetsBytes, MIB),
      },
  )
  const set = (p: Partial<Values>) => setV((x) => ({ ...x, ...p }))
  const { require, scope, grace, hosts, maxMiB, partial, evictDays, idle, clones, free, assetCache } = v
  const [error, setError] = useState(failure?.message ?? '')
  const messageRef = useRef<HTMLParagraphElement>(null)
  useEffect(() => {
    if (failure) messageRef.current?.focus()
  }, [failure])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending) return
    setError('')
    onFailure(null)
    const body: PutAdminStorage = {}
    const g = parse(grace, 1, 0, false)
    const days = parse(evictDays, 1, 1, true)
    const idleH = parse(idle, 1, 1, false)
    const max = parse(maxMiB, MIB, MIB, true)
    const budget = parse(clones, GIB, GIB, true)
    const floor = parse(free, GIB, 0, true)
    const assetsBudget = parse(assetCache, MIB, 64 * MIB, false)
    if (
      g === undefined ||
      g === null ||
      g > 365 ||
      idleH === undefined ||
      idleH === null ||
      idleH > 720 ||
      days === undefined ||
      (days !== null && days > 365) ||
      max === undefined ||
      budget === undefined ||
      floor === undefined ||
      assetsBudget === undefined ||
      assetsBudget === null
    ) {
      return setError(a.cache.invalid)
    }
    const list = hosts
      .split('\n')
      .map((h) => h.trim())
      .filter(Boolean)
    if (!data.hosts.fromFlag && (list.length === 0 || list.some((h) => /[\s/:@]/.test(h))))
      return setError(a.hosts.invalid)

    const remotes: NonNullable<PutAdminStorage['remotes']> = {}
    if ((data.remotes.require ?? '') !== require) remotes.require = require === '' ? null : require
    if (data.remotes.scope !== scope) remotes.scope = scope as 'new' | 'all'
    if (data.remotes.graceDays !== g) remotes.graceDays = g
    if (Object.keys(remotes).length > 0) body.remotes = remotes
    if (!data.hosts.fromFlag && list.join('\n') !== data.hosts.allowed.join('\n')) body.hosts = { allowed: list }
    if (data.localSpaces.maxBytes !== max) body.localSpaces = { maxBytes: max }
    const cache: NonNullable<PutAdminStorage['cache']> = {}
    if (data.cache.partial !== partial) cache.partial = partial
    if (data.cache.evictAfterDays !== days) cache.evictAfterDays = days
    if (data.cache.minIdleHours !== idleH) cache.minIdleHours = idleH
    if (data.cache.clonesBytes !== budget) cache.clonesBytes = budget
    if (data.cache.minFreeBytes !== floor) cache.minFreeBytes = floor
    if (data.cache.assetsBytes !== assetsBudget) cache.assetsBytes = assetsBudget
    if (Object.keys(cache).length > 0) body.cache = cache
    if (Object.keys(body).length === 0) return toast(a.unchanged)
    try {
      await save.mutateAsync(body)
      toast.success(a.saved)
    } catch (err) {
      setError(explain(err))
      onFailure({ id: Date.now(), message: explain(err), values: v })
    }
  }

  const aff = data.affected
  return (
    <form className="grid gap-8" onSubmit={(e) => void submit(e)} noValidate>
      <p className="text-sm text-muted-foreground">{a.intro}</p>
      <p className="text-xs text-muted-foreground">{a.git(data.git.version ?? '', data.git.partialClone)}</p>

      <SettingsSection title={a.remotes.title} description={a.remotes.help}>
        <SettingsRows>
          <SettingsRow title={a.remotes.require} help={a.remotes.requireHelp}>
            <NativeSelect
              aria-label={a.remotes.require}
              value={require}
              onChange={(e) => set({ require: e.target.value })}
            >
              <option value="">{a.remotes.requireOptions.none}</option>
              <option value="any">{a.remotes.requireOptions.any}</option>
              {platforms.map((p) => (
                <option key={p.id} value={p.id}>
                  {a.remotes.requireOptions.provider(p.name)}
                </option>
              ))}
            </NativeSelect>
          </SettingsRow>
          <SettingsRow title={a.remotes.scope} help={a.remotes.scopeHelp}>
            <NativeSelect
              aria-label={a.remotes.scope}
              value={scope}
              onChange={(e) => set({ scope: e.target.value })}
              disabled={require === ''}
            >
              <option value="new">{a.remotes.scopeOptions.new}</option>
              <option value="all">{a.remotes.scopeOptions.all}</option>
            </NativeSelect>
          </SettingsRow>
          <SettingsRow title={a.remotes.graceDays} help={a.remotes.graceHelp}>
            <Input
              aria-label={a.remotes.graceDays}
              inputMode="numeric"
              value={grace}
              onChange={(e) => set({ grace: e.target.value })}
              className="w-28"
              disabled={require === ''}
            />
          </SettingsRow>
        </SettingsRows>
        {data.remotes.since && (
          <p className="text-xs text-muted-foreground">{a.remotes.since(absoluteTime(data.remotes.since))}</p>
        )}
        <p className="text-xs text-muted-foreground" data-slot="storage-affected">
          {a.remotes.affected(aff.inGrace, aff.overdue, aff.exempt ?? 0)}
        </p>
      </SettingsSection>

      <SettingsSection title={a.hosts.title} description={a.hosts.help}>
        <label className="grid gap-1.5 text-sm">
          <span className="font-medium">{a.hosts.label}</span>
          <Textarea
            rows={4}
            value={hosts}
            onChange={(e) => set({ hosts: e.target.value })}
            disabled={data.hosts.fromFlag}
            className="w-full max-w-md font-mono text-xs"
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        {data.hosts.fromFlag && <p className="text-xs text-muted-foreground">{a.hosts.fromFlag}</p>}
      </SettingsSection>

      <SettingsSection title={a.local.title} description={a.local.help}>
        <SettingsRows>
          <SettingsRow title={a.local.maxBytes} help={a.local.maxHelp}>
            <Input
              aria-label={a.local.maxBytes}
              inputMode="numeric"
              value={maxMiB}
              onChange={(e) => set({ maxMiB: e.target.value })}
              className="w-28"
            />
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <SettingsSection title={a.cache.title} description={a.cache.help}>
        <SettingsRows>
          <SettingsRow
            title={a.cache.partial}
            help={data.git.partialClone ? a.cache.partialHelp : `${a.cache.partialHelp} ${a.cache.partialOff}`}
          >
            <Switch
              aria-label={a.cache.partial}
              checked={partial}
              disabled={!data.git.partialClone}
              onCheckedChange={(on) => set({ partial: on })}
            />
          </SettingsRow>
          <SettingsRow title={a.cache.evictAfter} help={a.cache.evictAfterHelp}>
            <Input
              aria-label={a.cache.evictAfter}
              inputMode="numeric"
              value={evictDays}
              onChange={(e) => set({ evictDays: e.target.value })}
              className="w-28"
            />
          </SettingsRow>
          <SettingsRow title={a.cache.minIdle} help={a.cache.minIdleHelp}>
            <Input
              aria-label={a.cache.minIdle}
              inputMode="numeric"
              value={idle}
              onChange={(e) => set({ idle: e.target.value })}
              className="w-28"
            />
          </SettingsRow>
          <SettingsRow title={a.cache.clones} help={a.cache.clonesHelp}>
            <Input
              aria-label={a.cache.clones}
              inputMode="numeric"
              value={clones}
              onChange={(e) => set({ clones: e.target.value })}
              className="w-28"
            />
          </SettingsRow>
          <SettingsRow title={a.cache.minFree} help={a.cache.minFreeHelp}>
            <Input
              aria-label={a.cache.minFree}
              inputMode="numeric"
              value={free}
              onChange={(e) => set({ free: e.target.value })}
              className="w-28"
            />
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <SettingsSection title={a.assets.title} description={a.assets.help}>
        <SettingsRows>
          <SettingsRow title={a.cache.assetsBytes} help={a.cache.assetsBytesHelp}>
            <Input
              aria-label={a.cache.assetsBytes}
              inputMode="numeric"
              value={assetCache}
              onChange={(e) => set({ assetCache: e.target.value })}
              className="w-28"
            />
          </SettingsRow>
        </SettingsRows>
        <OwnExemptRow />
      </SettingsSection>

      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <div>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? <Spinner /> : <Save aria-hidden="true" />}
          {a.save}
        </Button>
      </div>
    </form>
  )
}
