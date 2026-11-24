import { useId, useState } from 'react'
import { ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import { usePutSetting, useSiteSettings, useTenancy, useTenants, useTenantSettings } from '@/api/admin'
import type { SettingLower, SiteSetting, TenantSetting } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { tt } from '@/i18n/tenants'

const ts = tt.settings

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
  onChange,
}: {
  kind: string
  value: unknown
  disabled: boolean
  label: string
  onChange: (v: unknown) => void
}) {
  switch (kind) {
    case 'boolean':
      return <Switch checked={value === true} disabled={disabled} aria-label={label} onCheckedChange={onChange} />
    case 'limit':
      return (
        <Input
          type="number"
          inputMode="numeric"
          className="h-8 w-28"
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
          className="w-full max-w-sm"
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
          className="h-8 w-full max-w-sm"
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
function RuleEditor({
  kind,
  lower,
  disabled,
  label,
  onChange,
}: {
  kind: string
  lower: SettingLower
  disabled: boolean
  label: string
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
            : lower,
      )
    else onChange({ mode: mode as SettingLower['mode'] })
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <NativeSelect
        className="h-8 w-28"
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
        <>
          <Label className="text-xs text-muted-foreground">{ts.maxLabel}</Label>
          <Input
            type="number"
            className="h-8 w-24"
            aria-label={`${label}: ${ts.maxLabel}`}
            disabled={disabled}
            value={lower.max ?? ''}
            onChange={(e) => onChange({ mode: 'restrict', max: Number(e.target.value) })}
          />
        </>
      )}
      {lower.mode === 'restrict' && kind === 'boolean' && (
        <span className="text-xs text-muted-foreground">{ts.onlyOff}</span>
      )}
    </div>
  )
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

// One setting at the site layer: its value and the rule for everything below.
function SiteRow({ s }: { s: SiteSetting }) {
  const put = usePutSetting()
  const [value, setValue] = useState<unknown>(s.site.value)
  const [lower, setLower] = useState<SettingLower>(s.site.lower)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const id = useId()
  const name = nameOf(s.key)
  const doc = s.kind === 'doc'
  const dirtyValue = !doc && !s.site.fromFlag && !same(value, s.site.value)
  const dirtyLower = !same(lower, s.site.lower)
  const save = () => {
    setMessage(null)
    put.mutate(
      { key: s.key, ...(dirtyValue ? { value } : {}), ...(dirtyLower ? { lower } : {}) },
      {
        onSuccess: (r) => setMessage({ tone: 'info', text: r.affected ? ts.affected(r.affected) : ts.saved }),
        onError: (e) => setMessage({ tone: 'error', text: settingError(e) }),
      },
    )
  }
  return (
    <li className="grid gap-2 px-3 py-3" data-slot="setting-row" data-key={s.key}>
      <div className="flex flex-wrap items-center gap-2">
        <span id={id} className="text-sm font-medium">
          {name}
        </span>
        {s.site.fromFlag && <Badge variant="secondary">{ts.fromFlag}</Badge>}
      </div>
      {ts.helps[s.key] && <p className="text-xs text-muted-foreground">{ts.helps[s.key]}</p>}
      <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-2">
        {!doc && (
          <div className="grid gap-1">
            <span className="text-xs text-muted-foreground">{ts.value}</span>
            <ValueEditor
              kind={s.kind}
              value={value}
              disabled={!!s.site.fromFlag || put.isPending}
              label={name}
              onChange={setValue}
            />
          </div>
        )}
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{ts.rule}</span>
          <RuleEditor
            kind={s.kind}
            lower={lower}
            disabled={put.isPending}
            label={`${name}: ${ts.rule}`}
            onChange={setLower}
          />
        </div>
        <Button
          type="button"
          size="sm"
          className="self-end"
          disabled={put.isPending || (!dirtyValue && !dirtyLower)}
          onClick={save}
        >
          {put.isPending ? ts.saving : ts.save}
        </Button>
      </div>
    </li>
  )
}

// One setting at a tenant's layer, with what the site above says about it.
function TenantRow({ tenant, s }: { tenant: string; s: TenantSetting }) {
  const put = usePutSetting()
  const mine = s.tenant
  const [follow, setFollow] = useState(mine?.value == null)
  const [value, setValue] = useState<unknown>(mine?.value ?? s.site.value)
  const [lower, setLower] = useState<SettingLower>(mine?.lower ?? { mode: 'allow' })
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const name = nameOf(s.key)
  const doc = s.kind === 'doc'
  const locked = s.site.lower.mode === 'lock'
  const dirtyValue = !doc && !same(follow ? null : value, mine?.value ?? null)
  const dirtyLower = !same(lower, mine?.lower ?? { mode: 'allow' })
  const save = () => {
    setMessage(null)
    put.mutate(
      { tenant, key: s.key, ...(dirtyValue ? { value: follow ? null : value } : {}), ...(dirtyLower ? { lower } : {}) },
      {
        onSuccess: () => setMessage({ tone: 'info', text: ts.saved }),
        onError: (e) => setMessage({ tone: 'error', text: settingError(e) }),
      },
    )
  }
  const kept = mine && mine.value != null && !mine.inEffect
  return (
    <li className="grid gap-2 px-3 py-3" data-slot="setting-row" data-key={s.key}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{name}</span>
        <RuleText lower={s.site.lower} kind={s.kind} />
        {kept && (
          <Badge variant="outline" data-slot="not-in-effect" title={ts.keptHint}>
            {ts.notInEffect}
          </Badge>
        )}
      </div>
      {ts.helps[s.key] && <p className="text-xs text-muted-foreground">{ts.helps[s.key]}</p>}
      {kept && <p className="text-xs text-muted-foreground">{ts.keptHint}</p>}
      <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-2">
        {!doc && (
          <div className="grid gap-1">
            <span className="text-xs text-muted-foreground">{ts.value}</span>
            <div className="flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-2 text-xs text-muted-foreground">
                <Switch
                  checked={follow}
                  disabled={locked || put.isPending}
                  aria-label={`${name}: ${ts.follow}`}
                  onCheckedChange={setFollow}
                />
                {ts.follow}
              </label>
              {!follow && (
                <ValueEditor
                  kind={s.kind}
                  value={value}
                  disabled={locked || put.isPending}
                  label={name}
                  onChange={setValue}
                />
              )}
            </div>
          </div>
        )}
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">{ts.rule}</span>
          <RuleEditor
            kind={s.kind}
            lower={lower}
            disabled={put.isPending}
            label={`${name}: ${ts.rule}`}
            onChange={setLower}
          />
        </div>
        <Button
          type="button"
          size="sm"
          className="self-end"
          disabled={put.isPending || (!dirtyValue && !dirtyLower)}
          onClick={save}
        >
          {put.isPending ? ts.saving : ts.save}
        </Button>
      </div>
    </li>
  )
}

function SiteLayer() {
  const q = useSiteSettings()
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={ts.loadFailed} onRetry={() => void q.refetch()} />
  return (
    <SettingsSection title={ts.siteLayer} id="settings-site-heading" description={ts.siteHelp}>
      <ul className="divide-y rounded-lg border">
        {q.data.settings.map((s) => (
          <SiteRow key={`${s.key}:${JSON.stringify(s.site)}`} s={s} />
        ))}
      </ul>
    </SettingsSection>
  )
}

function TenantLayer({ tenant, name }: { tenant: string; name: string }) {
  const q = useTenantSettings(tenant)
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={ts.loadFailed} onRetry={() => void q.refetch()} />
  return (
    <SettingsSection title={ts.tenantLayer(name)} id="settings-tenant-heading" description={ts.tenantHelp}>
      <ul className="divide-y rounded-lg border">
        {q.data.settings.map((s) => (
          <TenantRow key={`${tenant}:${s.key}:${JSON.stringify([s.site, s.tenant])}`} tenant={tenant} s={s} />
        ))}
      </ul>
    </SettingsSection>
  )
}

// The settings, layer by layer. A site administrator sees the site's layer and, with tenants on, the layer of
// a tenant they pick; a tenant administrator sees their own tenant's layer, with what the site above has locked
// or restricted marked on each setting.
export function SettingsPanel() {
  const me = useSession().data?.user
  const isSite = me?.siteAdmin !== false
  const tenancy = useTenancy(isSite)
  const tenants = useTenants(isSite && !!tenancy.data && (tenancy.data.enabled || tenancy.data.tenants > 1))
  const [picked, setPicked] = useState('')
  const own = me?.tenant
  const list = tenants.data ?? []
  const tenantId = isSite ? picked || own?.id || '' : (own?.id ?? '')
  const tenantName = list.find((x) => x.id === tenantId)?.name ?? (tenantId === own?.id ? own?.name : '') ?? ''
  return (
    <div className="grid gap-8">
      <p className="text-sm text-muted-foreground">{ts.help}</p>
      {isSite && <SiteLayer />}
      {isSite && list.length > 1 && (
        <div className="flex items-center gap-2">
          <Label htmlFor="settings-tenant-pick">{ts.tenantPick}</Label>
          <NativeSelect
            id="settings-tenant-pick"
            className="h-8 w-56"
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
      {tenantId && <TenantLayer tenant={tenantId} name={tenantName ?? ''} />}
    </div>
  )
}
