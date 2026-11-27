import { useState } from 'react'
import { CalendarClock, Pencil } from 'lucide-react'
import { usePutSetting, useTenantSettings } from '@/api/admin'
import type { RuleWrite, SettingLower, SettingRule, Tenant, TenantSetting } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { Badge } from '@/components/ui/badge'
import { DataTable, RowAction, type Column } from '@/components/ui/data-table'
import { RuleDialog, inBytes, nameOf } from './RuleDialog'
import { settingError } from './SettingsPanel'
import { needsCapabilityConfirm, useCapabilityConfirm } from './CapabilityConfirm'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { useTablePrefs } from '@/lib/tablePrefs'
import { tt } from '@/i18n/tenants'
import { tu } from '@/i18n/usage'

const tx = tt.rules
type Source = 'site' | 'tenant' | SettingRule['source']

// What the tenant's setting is made of right now: the rule in effect, the one under it, a manual rule that waits.
interface Made {
  source: Source
  inEffect?: SettingRule
  underlying?: SettingRule
  waiting?: SettingRule
}

function made(s: TenantSetting, now = Date.now()): Made {
  const rules = s.tenant?.rules ?? []
  const inEffect = rules.find((r) => r.inEffect)
  const underlying = rules.find((r) => r.underlying)
  const waiting = rules.find((r) => !r.inEffect && !r.underlying && !!r.from && Date.parse(r.from) > now)
  const own = s.tenant && s.tenant.value != null && s.tenant.inEffect
  return { source: inEffect ? inEffect.source : own ? 'tenant' : 'site', inEffect, underlying, waiting }
}

export function showValue(s: Pick<TenantSetting, 'key' | 'kind'>, value: unknown): string {
  if (typeof value === 'boolean') return value ? tx.on : tx.off
  if (Array.isArray(value)) return value.length ? value.join('、') : tx.none
  if (value === null || value === undefined) return s.kind === 'limit' ? tu.noLimit : tx.none
  if (typeof value === 'number') return inBytes(s.key) ? formatBytes(value) : value.toLocaleString()
  return tt.settings.choices[s.key]?.[String(value)] ?? String(value)
}

// "from 1 Dec to 1 Jan", "until 1 Jan", "from 1 Dec"; empty for no window.
export function windowText(from?: string | null, until?: string | null): string {
  if (from && until) return tx.window.both(absoluteTime(from), absoluteTime(until))
  if (from) return tx.window.from(absoluteTime(from))
  if (until) return tx.window.until(absoluteTime(until))
  return ''
}

const SOURCE_VARIANT: Record<Source, 'default' | 'secondary' | 'outline'> = {
  manual: 'default',
  billing: 'default',
  plan: 'secondary',
  tenant: 'outline',
  site: 'outline',
}

// What a rule says besides its value: a lock, or a restriction.
export function lowerText(s: Pick<TenantSetting, 'key' | 'kind'>, l: SettingLower | null): string {
  if (!l || l.mode === 'allow') return ''
  if (l.mode === 'lock') return tx.locked
  if (l.max !== undefined) return tx.atMost(showValue(s, l.max))
  return tx.restricted((l.options ?? []).map(String).join(', '))
}

// What is in effect when, under a value: its window, what it falls back to, and a manual rule that waits.
function WindowLines({ s, m, next }: { s: TenantSetting; m: Made; next?: string }) {
  const now = m.inEffect ? windowText(m.inEffect.from, m.inEffect.until) : ''
  const rule = m.inEffect ? lowerText(s, m.inEffect.lower) : ''
  if (!now && !rule && !m.underlying && !m.waiting && !next) return null
  return (
    <div className="mt-1 min-w-0 text-xs text-muted-foreground">
      {now && <div>{now}</div>}
      {rule && <div>{rule}</div>}
      {m.underlying && (
        <div>
          {tx.then(
            m.underlying.value === null || m.underlying.value === undefined
              ? tx.followsAbove
              : showValue(s, m.underlying.value),
            tx.sources[m.underlying.source],
          )}
        </div>
      )}
      {m.waiting && (
        <div className="flex items-center gap-1">
          <CalendarClock aria-hidden="true" className="size-3 shrink-0" />
          <span>
            {tx.waiting(
              m.waiting.value === null || m.waiting.value === undefined
                ? lowerText(s, m.waiting.lower)
                : showValue(s, m.waiting.value),
              windowText(m.waiting.from, m.waiting.until),
            )}
          </span>
        </div>
      )}
      {next && <div>{tx.next(absoluteTime(next))}</div>}
    </div>
  )
}

// A setting whose value is a list or a document has no editor here: its layers are edited as settings.
const editable = (s: TenantSetting) => s.kind !== 'list' && s.kind !== 'doc'

function EditButton({ s, onEdit }: { s: TenantSetting; onEdit: (s: TenantSetting) => void }) {
  if (!editable(s)) return null
  return (
    <RowAction
      icon={<Pencil aria-hidden="true" />}
      label={tx.edit}
      ariaLabel={tx.editFor(s.key)}
      onClick={() => onEdit(s)}
    />
  )
}

// The tenant's rules: every setting a site administrator may set for one tenant (features, quotas, limits, rates and
// the rest), with where its value comes from, the rule's window and what lies under it. Site administrators change
// the manual rule; a tenant's own administrators only read them.
export function TenantRulesBlock({ tenant }: { tenant: Tenant }) {
  const sheet = useTenantSettings(tenant.id)
  const prefs = useTablePrefs('tenant-rules')
  const [editing, setEditing] = useState<TenantSetting | null>(null)
  const [notice, setNotice] = useState('')
  const rows = sheet.data?.settings ?? []
  const used: Record<string, number> = {
    'quota.people': tenant.usage.people,
    'quota.spaces': tenant.usage.spaces,
    'quota.bytes': tenant.usage.bytes,
    'quota.pages': tenant.usage.pages,
  }
  const edit = (s: TenantSetting) => {
    setNotice('')
    setEditing(s)
  }
  const columns: Column<TenantSetting>[] = [
    {
      id: 'key',
      header: tx.columns.key,
      required: true,
      weight: 4,
      min: 180,
      cell: (s) => (
        <div className="min-w-0">
          <div className="text-sm [overflow-wrap:anywhere]">{nameOf(s.key)}</div>
          <div className="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]">{s.key}</div>
        </div>
      ),
    },
    {
      id: 'value',
      header: tx.columns.value,
      weight: 1.5,
      min: 110,
      priority: 2,
      cell: (s) => (
        <div className="min-w-0 tabular-nums">
          <span className="break-words">{showValue(s, s.effective)}</span>
          {s.key in used && (
            <div className="text-xs text-muted-foreground">{tx.used(used[s.key].toLocaleString())}</div>
          )}
        </div>
      ),
    },
    {
      id: 'source',
      header: tx.columns.source,
      weight: 1,
      min: 120,
      priority: 3,
      cell: (s) => {
        const m = made(s)
        return <Badge variant={SOURCE_VARIANT[m.source]}>{tx.sources[m.source]}</Badge>
      },
    },
    {
      id: 'window',
      header: tx.columns.window,
      weight: 2,
      min: 150,
      priority: 4,
      cell: (s) => <WindowLines s={s} m={made(s)} next={s.tenant?.next} />,
    },
  ]
  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3 border-t pt-3" data-slot="tenant-rules">
      <div className="grid gap-0.5">
        <h4 className="text-sm font-medium">{tx.title}</h4>
        <p className="text-xs text-muted-foreground">{tx.help}</p>
      </div>
      {notice && <FormMessage tone="info">{notice}</FormMessage>}
      {/* On a phone the table has room for the key and little else: each setting is a row of its own there. */}
      <ul className="divide-y rounded-lg border sm:hidden" data-slot="tenant-rules-list">
        {sheet.isPending && <li className="px-3 py-3 text-sm text-muted-foreground">…</li>}
        {sheet.isError && <li className="px-3 py-3 text-sm text-destructive">{tx.loadFailed}</li>}
        {rows.map((s) => {
          const m = made(s)
          return (
            <li key={s.key} className="flex items-start gap-2 px-3 py-2.5" data-key={s.key} data-source={m.source}>
              <div className="min-w-0 flex-1">
                <div className="text-sm [overflow-wrap:anywhere]">{nameOf(s.key)}</div>
                <div className="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]">{s.key}</div>
                <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                  <span className="font-medium tabular-nums">{showValue(s, s.effective)}</span>
                  <Badge variant={SOURCE_VARIANT[m.source]}>{tx.sources[m.source]}</Badge>
                  {s.key in used && (
                    <span className="text-muted-foreground">{tx.used(used[s.key].toLocaleString())}</span>
                  )}
                </div>
                <WindowLines s={s} m={m} next={s.tenant?.next} />
              </div>
              <EditButton s={s} onEdit={edit} />
            </li>
          )
        })}
      </ul>
      <div className="max-sm:hidden">
        <DataTable
          prefs={prefs}
          columns={columns}
          rows={rows}
          rowId={(s) => s.key}
          rowLabel={(s) => s.key}
          rowAttrs={(s) => ({ 'data-key': s.key, 'data-source': made(s).source })}
          loading={sheet.isPending}
          error={sheet.isError ? tx.loadFailed : undefined}
          onRetry={() => void sheet.refetch()}
          empty={tx.empty}
          actions={(s) => <EditButton s={s} onEdit={edit} />}
        />
      </div>
      {editing && (
        <TenantRuleDialog
          tenant={tenant}
          s={editing}
          onClose={() => setEditing(null)}
          onSaved={(warning) => {
            setEditing(null)
            setNotice(warning ? tx.replaced(warning.keys.map(nameOf).join('、')) : '')
          }}
        />
      )}
    </div>
  )
}

// The manual rule of one setting of the tenant, with a window.
function TenantRuleDialog({
  tenant,
  s,
  onClose,
  onSaved,
}: {
  tenant: Tenant
  s: TenantSetting
  onClose: () => void
  onSaved: (warning?: { keys: string[] }) => void
}) {
  const put = usePutSetting()
  const [error, setError] = useState('')
  const manual = s.tenant?.rules?.find((r) => r.source === 'manual')
  // A rule that switches off a feature the tenant uses goes through its preview and a second confirmation.
  const capability = useCapabilityConfirm(() => onSaved())
  const send = (siteRule: RuleWrite | null) => {
    setError('')
    const change = { tenant: tenant.id, key: s.key, siteRule }
    put.mutate(change, {
      onSuccess: (r) => onSaved(r.warning),
      onError: (e) => (needsCapabilityConfirm(e) ? capability.ask(change) : setError(settingError(e))),
    })
  }
  return (
    <>
      {capability.dialog}
      <RuleDialog
        setting={s}
        rule={manual ?? null}
        start={s.effective}
        windows
        pending={put.isPending}
        error={error}
        onRemove={manual ? () => send(null) : undefined}
        onSubmit={send}
        onClose={onClose}
      />
    </>
  )
}
