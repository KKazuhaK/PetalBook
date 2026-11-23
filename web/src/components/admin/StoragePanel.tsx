import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { useAdminStorage, useSaveAdminStorage } from '@/api/admin'
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
import { t } from '@/i18n'
import { ts } from '@/i18n/storage'

const MIB = 1024 ** 2
const GIB = 1024 ** 3

// The site's storage policy (docs/api.md, "Server storage"): whether spaces must
// follow a remote, the hosts they may be on, the limit on spaces that have none,
// and when the copy of an idle space is removed from the disk.
export function StoragePanel() {
  const storage = useAdminStorage()
  if (storage.isPending) return <PageFallback />
  if (storage.isError) return <ErrorState message={t.fmt.labelled(ts.admin.loadFailed, storage.error.message)} onRetry={() => void storage.refetch()} />
  return <StorageForm key={JSON.stringify(storage.data)} data={storage.data} />
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

function StorageForm({ data }: { data: AdminStorage }) {
  const save = useSaveAdminStorage()
  const a = ts.admin
  const [require, setRequire] = useState<string>(data.remotes.require ?? '')
  const [scope, setScope] = useState<string>(data.remotes.scope)
  const [grace, setGrace] = useState(String(data.remotes.graceDays))
  const [hosts, setHosts] = useState(data.hosts.allowed.join('\n'))
  const [maxMiB, setMaxMiB] = useState(text(data.localSpaces.maxBytes, MIB))
  const [partial, setPartial] = useState(data.cache.partial)
  const [evictDays, setEvictDays] = useState(text(data.cache.evictAfterDays, 1))
  const [idle, setIdle] = useState(String(data.cache.minIdleHours))
  const [clones, setClones] = useState(text(data.cache.clonesBytes, GIB))
  const [free, setFree] = useState(text(data.cache.minFreeBytes, GIB))
  const [assetCache, setAssetCache] = useState(text(data.cache.assetsBytes, MIB))
  const [newAssets, setNewAssets] = useState(data.assets.newSpaces)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (save.isPending) return
    setError('')
    const body: PutAdminStorage = {}
    const g = parse(grace, 1, 0, false)
    const days = parse(evictDays, 1, 1, true)
    const idleH = parse(idle, 1, 1, false)
    const max = parse(maxMiB, MIB, MIB, true)
    const budget = parse(clones, GIB, GIB, true)
    const floor = parse(free, GIB, 0, true)
    const assetsBudget = parse(assetCache, MIB, 64 * MIB, false)
    if (g === undefined || g === null || g > 365 || idleH === undefined || idleH === null || idleH > 720 || days === undefined || (days !== null && days > 365) || max === undefined || budget === undefined || floor === undefined || assetsBudget === undefined || assetsBudget === null) {
      return setError(a.cache.invalid)
    }
    const list = hosts.split('\n').map((h) => h.trim()).filter(Boolean)
    if (!data.hosts.fromFlag && (list.length === 0 || list.some((h) => /[\s/:@]/.test(h)))) return setError(a.hosts.invalid)

    const remotes: NonNullable<PutAdminStorage['remotes']> = {}
    if ((data.remotes.require ?? '') !== require) remotes.require = require === '' ? null : (require as 'any' | 'github')
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
    if (data.assets.newSpaces !== newAssets) body.assets = { newSpaces: newAssets }
    if (Object.keys(body).length === 0) return toast(a.unchanged)
    try {
      await save.mutateAsync(body)
      toast.success(a.saved)
    } catch (err) {
      setError(explain(err))
    }
  }

  const aff = data.affected
  return (
    <form className="grid gap-8" onSubmit={(e) => void submit(e)} noValidate>
      <p className="text-sm text-muted-foreground">{a.intro}</p>
      <p className="text-xs text-muted-foreground">{a.git(data.git.version, data.git.partialClone)}</p>

      <SettingsSection title={a.remotes.title} description={a.remotes.help}>
        <SettingsRows>
          <SettingsRow title={a.remotes.require} help={a.remotes.requireHelp}>
            <NativeSelect aria-label={a.remotes.require} value={require} onChange={(e) => setRequire(e.target.value)}>
              <option value="">{a.remotes.requireOptions.none}</option>
              <option value="any">{a.remotes.requireOptions.any}</option>
              <option value="github">{a.remotes.requireOptions.github}</option>
            </NativeSelect>
          </SettingsRow>
          <SettingsRow title={a.remotes.scope} help={a.remotes.scopeHelp}>
            <NativeSelect aria-label={a.remotes.scope} value={scope} onChange={(e) => setScope(e.target.value)} disabled={require === ''}>
              <option value="new">{a.remotes.scopeOptions.new}</option>
              <option value="all">{a.remotes.scopeOptions.all}</option>
            </NativeSelect>
          </SettingsRow>
          <SettingsRow title={a.remotes.graceDays} help={a.remotes.graceHelp}>
            <Input aria-label={a.remotes.graceDays} inputMode="numeric" value={grace} onChange={(e) => setGrace(e.target.value)} className="w-28" disabled={require === ''} />
          </SettingsRow>
        </SettingsRows>
        {data.remotes.since && <p className="text-xs text-muted-foreground">{a.remotes.since(absoluteTime(data.remotes.since))}</p>}
        <p className="text-xs text-muted-foreground" data-slot="storage-affected">
          {a.remotes.affected(aff.inGrace, aff.overdue, aff.exempt)}
        </p>
      </SettingsSection>

      <SettingsSection title={a.hosts.title} description={a.hosts.help}>
        <label className="grid gap-1.5 text-sm">
          <span className="font-medium">{a.hosts.label}</span>
          <Textarea rows={4} value={hosts} onChange={(e) => setHosts(e.target.value)} disabled={data.hosts.fromFlag} className="w-full max-w-md font-mono text-xs" autoComplete="off" spellCheck={false} />
        </label>
        {data.hosts.fromFlag && <p className="text-xs text-muted-foreground">{a.hosts.fromFlag}</p>}
      </SettingsSection>

      <SettingsSection title={a.local.title} description={a.local.help}>
        <SettingsRows>
          <SettingsRow title={a.local.maxBytes} help={a.local.maxHelp}>
            <Input aria-label={a.local.maxBytes} inputMode="numeric" value={maxMiB} onChange={(e) => setMaxMiB(e.target.value)} className="w-28" />
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <SettingsSection title={a.cache.title} description={a.cache.help}>
        <SettingsRows>
          <SettingsRow title={a.cache.partial} help={data.git.partialClone ? a.cache.partialHelp : `${a.cache.partialHelp} ${a.cache.partialOff}`}>
            <Switch aria-label={a.cache.partial} checked={partial} disabled={!data.git.partialClone} onCheckedChange={setPartial} />
          </SettingsRow>
          <SettingsRow title={a.cache.evictAfter} help={a.cache.evictAfterHelp}>
            <Input aria-label={a.cache.evictAfter} inputMode="numeric" value={evictDays} onChange={(e) => setEvictDays(e.target.value)} className="w-28" />
          </SettingsRow>
          <SettingsRow title={a.cache.minIdle} help={a.cache.minIdleHelp}>
            <Input aria-label={a.cache.minIdle} inputMode="numeric" value={idle} onChange={(e) => setIdle(e.target.value)} className="w-28" />
          </SettingsRow>
          <SettingsRow title={a.cache.clones} help={a.cache.clonesHelp}>
            <Input aria-label={a.cache.clones} inputMode="numeric" value={clones} onChange={(e) => setClones(e.target.value)} className="w-28" />
          </SettingsRow>
          <SettingsRow title={a.cache.minFree} help={a.cache.minFreeHelp}>
            <Input aria-label={a.cache.minFree} inputMode="numeric" value={free} onChange={(e) => setFree(e.target.value)} className="w-28" />
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <SettingsSection title={a.assets.title} description={a.assets.help}>
        <SettingsRows>
          <SettingsRow title={a.cache.assetsBytes} help={a.cache.assetsBytesHelp}>
            <Input aria-label={a.cache.assetsBytes} inputMode="numeric" value={assetCache} onChange={(e) => setAssetCache(e.target.value)} className="w-28" />
          </SettingsRow>
          <SettingsRow title={a.assets.newSpaces} help={a.assets.newSpacesHelp}>
            <Switch aria-label={a.assets.newSpaces} checked={newAssets} onCheckedChange={setNewAssets} />
          </SettingsRow>
        </SettingsRows>
      </SettingsSection>

      <FormMessage>{error}</FormMessage>
      <div>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending && <Spinner />}
          {a.save}
        </Button>
      </div>
    </form>
  )
}
