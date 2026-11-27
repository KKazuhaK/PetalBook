import { useId, useRef, useState, type ReactNode } from 'react'
import { Save } from 'lucide-react'
import { ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import { useAcrossSite, useScope } from './TenantScope'
import { usePutSetting, useSiteSettings, useTenancy, useTenant, useTenants, useTenantSettings } from '@/api/admin'
import type { SettingLower, SiteSetting, TenantSetting } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { tt } from '@/i18n/tenants'
import { SecretsSection } from './SecretsSection'
import { needsCapabilityConfirm, useCapabilityConfirm } from './CapabilityConfirm'

const ts = tt.settings

// A switch is 20px tall; on a phone or tablet its touch area is stretched to 40px without moving anything.
const HIT = "after:absolute after:inset-x-0 after:-inset-y-2.5 after:content-[''] lg:after:hidden"

export function settingError(e: unknown): string {
  if (!(e instanceof ApiError)) return ts.errors.other
  const known = (ts.errors as Record<string, string>)[e.code]
  return known ?? (e.message || ts.errors.other)
}

const nameOf = (key: string) => ts.names[key] ?? key

// What a lock or a restriction says, in words: the site's rule for the layer below it, as that layer sees it.
function RuleText({ lower, kind }: { lower: SettingLower; kind: string }) {
  if (lower.mode === 'allow') return null
  if (lower.mode === 'lock') return <Badge variant="secondary">{ts.lockedBy.site}</Badge>
  const what =
    kind === 'limit' && lower.max !== undefined
      ? ts.atMost(lower.max)
      : kind === 'boolean'
        ? ts.onlyOff
        : (lower.options ?? []).join(', ')
  return <Badge variant="secondary">{ts.restrictedBy(what)}</Badge>
}

// The value of one layer. `null` is "follows the layer above" (tenant layer only).
function ValueEditor({
  kind,
  value,
  disabled,
  label,
  choices,
  words,
  onChange,
}: {
  kind: string
  value: unknown
  disabled: boolean
  label: string
  choices?: string[]
  words?: Record<string, string>
  onChange: (v: unknown) => void
}) {
  switch (kind) {
    case 'choice':
      return (
        <NativeSelect
          className="h-10 w-full lg:h-8"
          aria-label={label}
          disabled={disabled}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        >
          {(choices ?? []).map((c) => (
            <option key={c} value={c}>
              {words?.[c] ?? c}
            </option>
          ))}
        </NativeSelect>
      )
    case 'boolean':
      return (
        <Switch
          className={HIT}
          checked={value === true}
          disabled={disabled}
          aria-label={label}
          onCheckedChange={onChange}
        />
      )
    case 'limit':
      return (
        <Input
          type="number"
          inputMode="numeric"
          className="h-10 w-full lg:h-8"
          aria-label={label}
          disabled={disabled}
          value={typeof value === 'number' ? String(value) : ''}
          onChange={(e) => onChange(e.target.value === '' ? null : Number(e.target.value))}
        />
      )
    case 'list':
      return (
        <Textarea
          rows={3}
          className="w-full"
          aria-label={label}
          placeholder={ts.listHint}
          disabled={disabled}
          value={Array.isArray(value) ? (value as string[]).join('\n') : ''}
          onChange={(e) =>
            onChange(
              e.target.value
                .split('\n')
                .map((x) => x.trim())
                .filter(Boolean),
            )
          }
        />
      )
    case 'text':
      return (
        <Input
          className="h-10 w-full lg:h-8"
          aria-label={label}
          disabled={disabled}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        />
      )
    default:
      return null
  }
}

// What a layer lets the layers below it do.
export function RuleEditor({
  kind,
  lower,
  disabled,
  label,
  choices,
  words,
  onChange,
}: {
  kind: string
  lower: SettingLower
  disabled: boolean
  label: string
  choices?: string[]
  words?: Record<string, string>
  onChange: (l: SettingLower) => void
}) {
  const canRestrict = kind === 'boolean' || kind === 'limit' || kind === 'choice'
  const set = (mode: string) => {
    if (mode === 'restrict')
      onChange(
        kind === 'boolean'
          ? { mode: 'restrict', options: [false] }
          : kind === 'limit'
            ? { mode: 'restrict', max: lower.max ?? 1 }
            : { mode: 'restrict', options: lower.options?.length ? lower.options : (choices ?? []).slice(0, 1) },
      )
    else onChange({ mode: mode as SettingLower['mode'] })
  }
  return (
    <div className="grid gap-2">
      <NativeSelect
        className="h-10 w-full lg:h-8"
        aria-label={label}
        disabled={disabled}
        value={lower.mode}
        onChange={(e) => set(e.target.value)}
      >
        <option value="allow">{ts.modes.allow}</option>
        {canRestrict && <option value="restrict">{ts.modes.restrict}</option>}
        <option value="lock">{ts.modes.lock}</option>
      </NativeSelect>
      {lower.mode === 'restrict' && kind === 'limit' && (
        <div className="flex items-center gap-2">
          <Label className="shrink-0 text-xs text-muted-foreground">{ts.maxLabel}</Label>
          <Input
            type="number"
            className="h-10 w-full lg:h-8"
            aria-label={`${label}: ${ts.maxLabel}`}
            disabled={disabled}
            value={lower.max ?? ''}
            onChange={(e) => onChange({ mode: 'restrict', max: Number(e.target.value) })}
          />
        </div>
      )}
      {lower.mode === 'restrict' && kind === 'choice' && (
        <div className="grid gap-1" role="group" aria-label={`${label}: ${ts.optionsLabel}`}>
          {(choices ?? []).map((c) => (
            <label key={c} className="flex min-h-10 items-center gap-2 text-xs text-muted-foreground lg:min-h-0">
              <input
                type="checkbox"
                className="size-4"
                disabled={disabled}
                checked={(lower.options ?? []).includes(c)}
                onChange={(e) => {
                  const on = e.target.checked
                  const next = (choices ?? []).filter((x) => (x === c ? on : (lower.options ?? []).includes(x)))
                  if (next.length > 0) onChange({ mode: 'restrict', options: next })
                }}
              />
              {words?.[c] ?? c}
            </label>
          ))}
        </div>
      )}
      {lower.mode === 'restrict' && kind === 'boolean' && (
        <span className="text-xs text-muted-foreground">{ts.onlyOff}</span>
      )}
    </div>
  )
}

// One setting is a row of a fixed grid: the name and its words on the left; on the right the value, the rule for the
// layers below and the save button, each in a column of its own width, so they line up from row to row. Under a wide
// screen the right side goes below the left, and every control fills the width.
const ROW = 'grid gap-4 px-4 py-4 transition-colors hover:bg-muted/30 xl:grid-cols-[minmax(0,1fr)_32rem] xl:gap-8'
const CONTROLS = 'grid min-w-0 content-start items-start gap-4 sm:grid-cols-[13rem_10rem_6.5rem]'

function Cell({ label, children }: { label: string; children?: ReactNode }) {
  return (
    <div className="grid min-w-0 content-start gap-1.5">
      <span className="hidden min-h-4 text-xs text-muted-foreground sm:block">{label}</span>
      {label && <span className="text-xs text-muted-foreground sm:hidden">{label}</span>}
      {children}
    </div>
  )
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

// One setting at the site layer: its value and the rule for everything below. Another page may show one with its own
// words (title, help, the label of the value); with lockWhenOff, switching a feature off makes the rule a lock (off for
// every organization) until the administrator picks another rule.
export function SiteRow({
  s,
  title,
  help,
  label,
  lockWhenOff = false,
}: {
  s: SiteSetting
  title?: string
  help?: string
  label?: string
  lockWhenOff?: boolean
}) {
  const put = usePutSetting()
  const [value, setValue] = useState<unknown>(s.site.value)
  const [lower, setLower] = useState<SettingLower>(s.site.lower)
  // autoLock: the lock is the one switching off proposed; ruleChosen: the administrator picked the rule themselves.
  const [autoLock, setAutoLock] = useState(false)
  const [ruleChosen, setRuleChosen] = useState(false)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const id = useId()
  const name = title ?? nameOf(s.key)
  const changeValue = (v: unknown) => {
    setValue(v)
    if (!lockWhenOff || s.kind !== 'boolean' || ruleChosen) return
    if (v === false && lower.mode === 'allow') {
      setLower({ mode: 'lock' })
      setAutoLock(true)
    } else if (v === true && autoLock) {
      setLower(s.site.lower)
      setAutoLock(false)
    }
  }
  const changeLower = (l: SettingLower) => {
    setLower(l)
    setAutoLock(false)
    setRuleChosen(true)
  }
  const doc = s.kind === 'doc'
  const dirtyValue = !doc && !s.site.fromFlag && !same(value, s.site.value)
  const dirtyLower = !same(lower, s.site.lower)
  // A feature in use is switched off only after its preview and a second confirmation.
  const capability = useCapabilityConfirm(() => setMessage({ tone: 'info', text: ts.saved }))
  const save = () => {
    setMessage(null)
    const change = { key: s.key, ...(dirtyValue ? { value } : {}), ...(dirtyLower ? { lower } : {}) }
    put.mutate(change, {
      onSuccess: (r) => setMessage({ tone: 'info', text: r.affected ? ts.affected(r.affected) : ts.saved }),
      onError: (e) =>
        needsCapabilityConfirm(e) ? capability.ask(change) : setMessage({ tone: 'error', text: settingError(e) }),
    })
  }
  return (
    <li className={ROW} data-slot="setting-row" data-key={s.key}>
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span id={id} className="text-sm font-medium">
            {name}
          </span>
          {s.site.fromFlag && <Badge variant="secondary">{ts.fromFlag}</Badge>}
        </div>
        {(help ?? ts.helps[s.key]) && <p className="text-xs text-muted-foreground">{help ?? ts.helps[s.key]}</p>}
        <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
        {capability.dialog}
      </div>
      <div className={CONTROLS}>
        <Cell label={doc ? '' : ts.value}>
          {!doc && (
            <ValueEditor
              kind={s.kind}
              value={value}
              disabled={!!s.site.fromFlag || put.isPending}
              label={label ?? name}
              choices={s.choices}
              words={ts.choices[s.key]}
              onChange={changeValue}
            />
          )}
        </Cell>
        <Cell label={ts.rule}>
          <RuleEditor
            kind={s.kind}
            lower={lower}
            disabled={put.isPending}
            label={`${name}: ${ts.rule}`}
            choices={s.choices}
            words={ts.choices[s.key]}
            onChange={changeLower}
          />
        </Cell>
        <Cell label="">
          <Button
            type="button"
            size="sm"
            className="h-10 w-full lg:h-8"
            disabled={put.isPending || (!dirtyValue && !dirtyLower)}
            onClick={save}
          >
            <Save aria-hidden="true" />
            {put.isPending ? ts.saving : ts.save}
          </Button>
        </Cell>
      </div>
    </li>
  )
}

// One setting at a tenant's layer, with what the site above says about it.
function TenantRow({ tenant, s, isSite }: { tenant: string; s: TenantSetting; isSite: boolean }) {
  const put = usePutSetting()
  const mine = s.tenant
  // A value the site locked for this tenant lives in the site's rule for it; the tenant's own is kept beside it.
  const rule = mine?.rules?.find((r) => r.inEffect) ?? mine?.rules?.find((r) => r.source === 'manual')
  const shown = mine?.siteLocked ? (rule?.value ?? null) : (mine?.value ?? null)
  // A stored value a rule above beats is kept, not in effect: the editor starts from what the tenant gets.
  const inEffect = mine?.siteLocked ? rule?.inEffect !== false : mine?.inEffect !== false
  const start = shown != null && !inEffect ? s.effective : shown
  const [follow, setFollow] = useState(start == null)
  const [value, setValue] = useState<unknown>(start ?? s.site.value)
  const [lower, setLower] = useState<SettingLower>(mine?.lower ?? { mode: 'allow' })
  const [siteLock, setSiteLock] = useState(mine?.siteLocked === true)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const name = nameOf(s.key)
  const doc = s.kind === 'doc'
  // The site's lock over everyone, or its lock for this tenant when the person is the tenant's own administrator.
  const forcedForTenant = mine?.siteLocked === true
  const locked = s.site.lower.mode === 'lock' && !(isSite && forcedForTenant)
  const frozen = forcedForTenant && !isSite
  const canForce = isSite && !doc
  const dirtySiteLock = canForce && siteLock !== forcedForTenant
  const dirtyValue = !doc && !same(follow ? null : value, start)
  const dirtyLower = !same(lower, mine?.lower ?? { mode: 'allow' })
  // A lock always names a value: following, while forced, forces the value in effect, never null.
  const forcing = canForce && siteLock
  const sent = follow ? (forcing ? s.effective : null) : value
  const capability = useCapabilityConfirm(() => setMessage({ tone: 'info', text: ts.saved }))
  const save = () => {
    setMessage(null)
    const change = {
      tenant,
      key: s.key,
      // Forcing always names the value it forces (what the row shows), so a lock never changes what the tenant gets.
      ...(dirtyValue || (forcing && dirtySiteLock) ? { value: sent } : {}),
      ...(dirtyLower ? { lower } : {}),
      // While locked for the tenant, a new value is the locked one: it goes with siteLock.
      ...(dirtySiteLock || (forcing && dirtyValue) ? { siteLock } : {}),
    }
    put.mutate(change, {
      onSuccess: () => setMessage({ tone: 'info', text: ts.saved }),
      onError: (e) =>
        needsCapabilityConfirm(e) ? capability.ask(change) : setMessage({ tone: 'error', text: settingError(e) }),
    })
  }
  const kept = mine && mine.value != null && !mine.inEffect
  return (
    <li className={ROW} data-slot="setting-row" data-key={s.key}>
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">{name}</span>
          <RuleText lower={s.site.lower} kind={s.kind} />
          {forcedForTenant && <Badge variant="secondary">{ts.siteLockedByYou}</Badge>}
          {kept && (
            <Badge variant="outline" data-slot="not-in-effect" title={ts.keptHint}>
              {ts.notInEffect}
            </Badge>
          )}
        </div>
        {ts.helps[s.key] && <p className="text-xs text-muted-foreground">{ts.helps[s.key]}</p>}
        {kept && <p className="text-xs text-muted-foreground">{ts.keptHint}</p>}
        {capability.dialog}
        {canForce && (
          <div className="pt-1">
            <label className="flex min-h-10 items-center gap-2 text-sm lg:min-h-0">
              <Switch
                className={HIT}
                checked={siteLock}
                disabled={put.isPending || (follow && !siteLock)}
                aria-label={`${name}: ${ts.siteLock}`}
                onCheckedChange={setSiteLock}
              />
              {ts.siteLock}
            </label>
            <p className="text-xs text-muted-foreground">{ts.siteLockHelp}</p>
          </div>
        )}
        <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      </div>
      <div className={CONTROLS}>
        <Cell label={doc ? '' : ts.value}>
          {!doc && (
            <div className="grid gap-2">
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <Switch
                  className={HIT}
                  checked={follow}
                  disabled={locked || frozen || put.isPending}
                  aria-label={`${name}: ${ts.follow}`}
                  onCheckedChange={setFollow}
                />
                {ts.follow}
              </label>
              {!follow && (
                <ValueEditor
                  kind={s.kind}
                  value={value}
                  disabled={locked || frozen || put.isPending}
                  label={name}
                  choices={s.choices}
                  words={ts.choices[s.key]}
                  onChange={setValue}
                />
              )}
            </div>
          )}
        </Cell>
        <Cell label={ts.rule}>
          <RuleEditor
            kind={s.kind}
            lower={lower}
            disabled={put.isPending || frozen}
            label={`${name}: ${ts.rule}`}
            choices={s.choices}
            words={ts.choices[s.key]}
            onChange={setLower}
          />
        </Cell>
        <Cell label="">
          <Button
            type="button"
            size="sm"
            className="h-10 w-full lg:h-8"
            disabled={put.isPending || frozen || (!dirtyValue && !dirtyLower && !dirtySiteLock)}
            onClick={save}
          >
            <Save aria-hidden="true" />
            {put.isPending ? ts.saving : ts.save}
          </Button>
        </Cell>
      </div>
    </li>
  )
}

// The order the groups come in: what an administrator reaches for first. Any other prefix follows, by name.
const GROUP_ORDER = [
  'features',
  'quota',
  'rate',
  'signin',
  'sessions',
  'captcha',
  'storage',
  'cache',
  'files',
  'registration',
  'orgCreation',
  'tenants',
  'legal',
  'daily',
  'mail',
  'recycle',
  'audit',
  'exports',
  'jobs',
  'users',
  'accounts',
  'offline',
]
const groupOf = (key: string) => key.split('.')[0]

// Settings gathered by the first word of their key, in the order above: a strip of the groups to jump to (it scrolls
// sideways without a bar, its edges fading where there is more), then each group with its rows.
function Grouped<T extends { key: string }>({
  items,
  idPrefix,
  render,
}: {
  items: T[]
  idPrefix: string
  render: (item: T) => ReactNode
}) {
  const strip = useRef<HTMLDivElement>(null)
  useScrollFade(strip)
  const groups = new Map<string, T[]>()
  for (const it of items) groups.set(groupOf(it.key), [...(groups.get(groupOf(it.key)) ?? []), it])
  const order = [...groups.keys()].sort((a, b) => {
    const ia = GROUP_ORDER.indexOf(a)
    const ib = GROUP_ORDER.indexOf(b)
    return (ia < 0 ? GROUP_ORDER.length : ia) - (ib < 0 ? GROUP_ORDER.length : ib) || a.localeCompare(b)
  })
  const label = (g: string) => ts.groups[g] ?? g
  const jump = (g: string) =>
    document.getElementById(`${idPrefix}-${g}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-6">
      <nav aria-label={ts.groupsLabel} data-slot="setting-groups" className="min-w-0">
        <div ref={strip} className={`${scrollFadeClass} -mx-1 flex gap-2 px-1 py-1`}>
          {order.map((g) => (
            <Button
              key={g}
              type="button"
              variant="outline"
              size="sm"
              className="h-10 shrink-0 lg:h-8"
              data-group={g}
              onClick={() => jump(g)}
            >
              {label(g)}
            </Button>
          ))}
        </div>
      </nav>
      {order.map((g) => (
        <section
          key={g}
          id={`${idPrefix}-${g}`}
          aria-labelledby={`${idPrefix}-${g}-h`}
          data-slot="setting-group"
          data-group={g}
          className="grid min-w-0 scroll-mt-4 gap-2"
        >
          <div className="flex items-baseline gap-2">
            <h3 id={`${idPrefix}-${g}-h`} className="text-sm font-semibold">
              {label(g)}
            </h3>
            <span className="text-xs text-muted-foreground">{ts.groupCount(groups.get(g)!.length)}</span>
          </div>
          <ul className="divide-y overflow-hidden rounded-lg border">{groups.get(g)!.map(render)}</ul>
        </section>
      ))}
    </div>
  )
}

function SiteLayer() {
  const q = useSiteSettings()
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={ts.loadFailed} onRetry={() => void q.refetch()} />
  return (
    <SettingsSection title={ts.siteLayer} id="settings-site-heading" description={ts.siteHelp}>
      <Grouped
        items={q.data.settings}
        idPrefix="site"
        render={(s) => <SiteRow key={`${s.key}:${JSON.stringify(s.site)}`} s={s} />}
      />
    </SettingsSection>
  )
}

export function TenantLayer({ tenant, name, isSite }: { tenant: string; name: string; isSite: boolean }) {
  const q = useTenantSettings(tenant)
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={ts.loadFailed} onRetry={() => void q.refetch()} />
  return (
    <SettingsSection title={ts.tenantLayer(name)} id="settings-tenant-heading" description={ts.tenantHelp}>
      {/* A site-only setting the site may set for one tenant is in the list too; its own editor comes later. */}
      <Grouped
        items={q.data.settings.filter((s) => s.layers.includes('tenant'))}
        idPrefix="tenant"
        render={(s) => (
          <TenantRow
            key={`${tenant}:${s.key}:${JSON.stringify([s.site, s.tenant])}`}
            tenant={tenant}
            s={s}
            isSite={isSite}
          />
        )}
      />
    </SettingsSection>
  )
}

// The settings, layer by layer. A site administrator sees the site's layer and, with tenants on, the layer of
// a tenant they pick; a tenant administrator sees their own tenant's layer, with what the site above has locked
// or restricted marked on each setting.
export function SettingsPanel() {
  const me = useSession().data?.user
  const isSite = useAcrossSite()
  const tenancy = useTenancy(isSite)
  const tenants = useTenants(isSite && !!tenancy.data && (tenancy.data.enabled || tenancy.data.tenants > 1))
  const [picked, setPicked] = useState('')
  // In a tenant's scope the tenant is that one, whoever the person is.
  const scope = useScope()
  const own = scope ? undefined : me?.tenant
  const scoped = useTenant(scope)
  const list = tenants.data ?? []
  const tenantId = scope ?? (isSite ? picked || own?.id || '' : (own?.id ?? ''))
  const tenantName =
    (scope
      ? scoped.data?.name
      : (list.find((x) => x.id === tenantId)?.name ?? (tenantId === own?.id ? own?.name : ''))) ?? ''
  return (
    <div className="grid gap-8">
      <p className="rounded-lg border bg-muted/40 px-4 py-3 text-sm text-muted-foreground">{ts.help}</p>
      {isSite && <SiteLayer />}
      {isSite && <SecretsSection />}
      {isSite && list.length > 1 && (
        <div className="flex items-center gap-2">
          <Label htmlFor="settings-tenant-pick">{ts.tenantPick}</Label>
          <NativeSelect
            id="settings-tenant-pick"
            className="h-10 w-56 max-w-full lg:h-8"
            value={tenantId}
            onChange={(e) => setPicked(e.target.value)}
          >
            {list.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </NativeSelect>
        </div>
      )}
      {tenantId && <TenantLayer tenant={tenantId} name={tenantName ?? ''} isSite={isSite} />}
      {tenantId && <SecretsSection tenant={tenantId} name={tenantName ?? ''} />}
    </div>
  )
}
