import { useState, type FormEvent } from 'react'
import { CalendarClock, Pencil, Save, Trash2 } from 'lucide-react'
import { ApiError } from '@/api/client'
import { usePutTenantLimits, useTenantLimits } from '@/api/admin'
import type { LimitEntry, LimitSource, LimitValue, LimitWrite, Tenant } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DataTable, RowAction, type Column } from '@/components/ui/data-table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { SettingsRow, SettingsRows } from '@/components/settings/SettingsParts'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { useTablePrefs } from '@/lib/tablePrefs'
import { tt } from '@/i18n/tenants'
import { tu } from '@/i18n/usage'

const tx = tt.limits
const GB = 1024 ** 3
const MODES = ['server', 'own', 'tenant']

// A key counted in bytes is typed in GB; the API counts bytes.
const inBytes = (key: string) => /bytes$/i.test(key)

export function showValue(e: Pick<LimitEntry, 'key' | 'kind' | 'value'>, value: LimitValue = e.value): string {
  if (typeof value === 'boolean') return value ? tx.on : tx.off
  if (Array.isArray(value))
    return value.length ? value.map((m) => tx.modes[m as keyof typeof tx.modes] ?? m).join('、') : tx.none
  if (value === null) return tu.noLimit
  return inBytes(e.key) ? formatBytes(value) : value.toLocaleString()
}

// "from 1 Dec to 1 Jan", "until 1 Jan", "from 1 Dec"; empty for no window.
function windowText(from?: string | null, until?: string | null): string {
  if (from && until) return tx.window.both(absoluteTime(from), absoluteTime(until))
  if (from) return tx.window.from(absoluteTime(from))
  if (until) return tx.window.until(absoluteTime(until))
  return ''
}

const SOURCE_VARIANT: Record<LimitSource, 'default' | 'secondary' | 'outline'> = {
  manual: 'default',
  billing: 'default',
  plan: 'secondary',
  site: 'outline',
}

function errorText(e: unknown): string {
  if (e instanceof ApiError && e.code === 'invalid') return e.message || tx.invalid
  return tx.failed
}

// What is in effect when, under a value: its window, what it falls back to, and a manual value that waits.
function WindowLines({ e }: { e: LimitEntry }) {
  const now = windowText(e.from, e.until)
  const later = e.manual ? windowText(e.manual.from, e.manual.until) : ''
  if (!now && !e.underlying && !e.manual) return null
  return (
    <div className="mt-1 min-w-0 text-xs text-muted-foreground">
      {now && <div>{now}</div>}
      {e.underlying && <div>{tx.then(showValue(e, e.underlying.value), tx.sources[e.underlying.source])}</div>}
      {e.manual && (
        <div className="flex items-center gap-1">
          <CalendarClock aria-hidden="true" className="size-3 shrink-0" />
          <span>{tx.pending(showValue(e, e.manual.value), later)}</span>
        </div>
      )}
    </div>
  )
}

function EditButton({ e, onEdit }: { e: LimitEntry; onEdit: (e: LimitEntry) => void }) {
  return (
    <RowAction
      icon={<Pencil aria-hidden="true" />}
      label={tx.edit}
      ariaLabel={tx.editFor(e.key)}
      onClick={() => onEdit(e)}
    />
  )
}

// The tenant's limits sheet: every capability and limit with where its value comes from and its window. Site
// administrators change the manual values; a tenant's own administrators only read them.
export function TenantLimitsBlock({ tenant }: { tenant: Tenant }) {
  const sheet = useTenantLimits(tenant.id)
  const prefs = useTablePrefs('tenant-limits')
  const [editing, setEditing] = useState<LimitEntry | null>(null)
  const [notice, setNotice] = useState('')
  const rows = sheet.data?.limits ?? []
  const edit = (e: LimitEntry) => {
    setNotice('')
    setEditing(e)
  }
  const columns: Column<LimitEntry>[] = [
    {
      id: 'key',
      header: tx.columns.key,
      required: true,
      weight: 4,
      min: 180,
      cell: (e) => (
        <div className="min-w-0">
          <div className="font-mono text-xs [overflow-wrap:anywhere]">{e.key}</div>
          <div className="line-clamp-2 text-xs text-muted-foreground" title={e.description}>
            {e.description}
          </div>
        </div>
      ),
    },
    {
      id: 'value',
      header: tx.columns.value,
      weight: 1.5,
      min: 110,
      priority: 2,
      cell: (e) => (
        <div className="min-w-0 tabular-nums">
          <span className="break-words">{showValue(e)}</span>
          {e.used !== undefined && (
            <div className="text-xs text-muted-foreground">{tx.used(e.used.toLocaleString())}</div>
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
      cell: (e) => <Badge variant={SOURCE_VARIANT[e.source]}>{tx.sources[e.source]}</Badge>,
    },
    {
      id: 'window',
      header: tx.columns.window,
      weight: 2,
      min: 150,
      priority: 4,
      cell: (e) => <WindowLines e={e} />,
    },
  ]
  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3 border-t pt-3" data-slot="tenant-limits">
      <div className="grid gap-0.5">
        <h4 className="text-sm font-medium">{tx.title}</h4>
        <p className="text-xs text-muted-foreground">{tx.help}</p>
        {sheet.data?.plan && <p className="text-xs text-muted-foreground">{tx.plan(sheet.data.plan.name)}</p>}
      </div>
      {notice && <FormMessage tone="info">{notice}</FormMessage>}
      {/* On a phone the table has room for the key and little else: each key is a row of its own there. */}
      <ul className="divide-y rounded-lg border sm:hidden" data-slot="tenant-limits-list">
        {sheet.isPending && <li className="px-3 py-3 text-sm text-muted-foreground">…</li>}
        {sheet.isError && <li className="px-3 py-3 text-sm text-destructive">{tx.loadFailed}</li>}
        {rows.map((e) => (
          <li key={e.key} className="flex items-start gap-2 px-3 py-2.5" data-key={e.key} data-source={e.source}>
            <div className="min-w-0 flex-1">
              <div className="font-mono text-xs [overflow-wrap:anywhere]">{e.key}</div>
              <div className="text-xs text-muted-foreground">{e.description}</div>
              <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                <span className="font-medium tabular-nums">{showValue(e)}</span>
                <Badge variant={SOURCE_VARIANT[e.source]}>{tx.sources[e.source]}</Badge>
                {e.used !== undefined && (
                  <span className="text-muted-foreground">{tx.used(e.used.toLocaleString())}</span>
                )}
              </div>
              <WindowLines e={e} />
            </div>
            <EditButton e={e} onEdit={edit} />
          </li>
        ))}
      </ul>
      <div className="max-sm:hidden">
        <DataTable
          prefs={prefs}
          columns={columns}
          rows={rows}
          rowId={(e) => e.key}
          rowLabel={(e) => e.key}
          rowAttrs={(e) => ({ 'data-key': e.key, 'data-source': e.source })}
          loading={sheet.isPending}
          error={sheet.isError ? tx.loadFailed : undefined}
          onRetry={() => void sheet.refetch()}
          empty={tx.empty}
          actions={(e) => <EditButton e={e} onEdit={edit} />}
        />
      </div>
      {editing && (
        <LimitDialog
          tenant={tenant}
          entry={editing}
          onClose={() => setEditing(null)}
          onSaved={(warning) => {
            setEditing(null)
            setNotice(warning ? tx.replaced(warning.keys.join('、')) : '')
          }}
        />
      )}
    </div>
  )
}

const toLocal = (iso?: string | null) => {
  if (!iso) return ''
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
const fromLocal = (v: string) => (v ? new Date(v).toISOString().replace('.000Z', 'Z') : undefined)

// The manual value of one key, with the window it counts in.
function LimitDialog({
  tenant,
  entry,
  onClose,
  onSaved,
}: {
  tenant: Tenant
  entry: LimitEntry
  onClose: () => void
  onSaved: (warning?: { keys: string[] }) => void
}) {
  const put = usePutTenantLimits(tenant.id)
  // What the form starts with: the manual value, whether in force or waiting, else the value in force.
  const manual = entry.source === 'manual' ? { value: entry.value, from: entry.from, until: entry.until } : entry.manual
  const start = manual?.value ?? entry.value
  const [flag, setFlag] = useState(start === true)
  const [none, setNone] = useState(start === null)
  const [text, setText] = useState(
    typeof start === 'number'
      ? inBytes(entry.key)
        ? String(Math.round((start / GB) * 100) / 100)
        : String(start)
      : '',
  )
  const [modes, setModes] = useState<string[]>(Array.isArray(start) ? start : MODES)
  const [from, setFrom] = useState(toLocal(manual?.from))
  const [until, setUntil] = useState(toLocal(manual?.until))
  const [error, setError] = useState('')
  const nullable = entry.kind === 'limit' && entry.key !== 'storage.pinnedBytes'

  const value = (): LimitValue | undefined => {
    if (entry.kind === 'boolean') return flag
    if (entry.kind === 'list') return MODES.filter((m) => modes.includes(m))
    if (none && nullable) return null
    const n = Number(text.trim())
    const ok = /^\d+(\.\d{1,2})?$/.test(text.trim()) && Number.isFinite(n)
    if (!ok || (!inBytes(entry.key) && !Number.isInteger(n))) return undefined
    return inBytes(entry.key) ? Math.round(n * GB) : n
  }
  const send = (write: LimitWrite) =>
    put.mutate({ [entry.key]: write }, { onSuccess: (r) => onSaved(r.warning), onError: (e) => setError(errorText(e)) })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    const v = value()
    if (v === undefined) return setError(tx.invalidValue)
    if (from && until && new Date(until) <= new Date(from)) return setError(tx.untilBeforeFrom)
    send({ value: v, ...(from ? { from: fromLocal(from) } : {}), ...(until ? { until: fromLocal(until) } : {}) })
  }
  const hasManual = entry.source === 'manual' || entry.manual !== undefined
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4" noValidate data-slot="limit-form">
          <DialogHeader>
            <DialogTitle className="font-mono text-base">{entry.key}</DialogTitle>
            <DialogDescription>{entry.description}</DialogDescription>
          </DialogHeader>
          <FormMessage>{error}</FormMessage>
          <SettingsRows>
            <SettingsRow title={tx.valueLabel} controlId="limit-value">
              {entry.kind === 'boolean' && (
                <Switch id="limit-value" checked={flag} onCheckedChange={setFlag} aria-label={tx.valueLabel} />
              )}
              {entry.kind === 'limit' && (
                <div className="grid gap-2">
                  <Input
                    id="limit-value"
                    value={text}
                    inputMode="decimal"
                    placeholder={inBytes(entry.key) ? tx.gb : ''}
                    aria-label={tx.valueLabel}
                    onChange={(e) => {
                      setText(e.target.value)
                      setNone(false)
                    }}
                  />
                  {nullable && (
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={none}
                        onChange={(e) => {
                          setNone(e.target.checked)
                          if (e.target.checked) setText('')
                        }}
                      />
                      {tx.noLimit}
                    </label>
                  )}
                </div>
              )}
              {entry.kind === 'list' && (
                <div className="grid gap-1" role="group" aria-label={tx.valueLabel}>
                  {MODES.map((m) => (
                    <label key={m} className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={modes.includes(m)}
                        onChange={(e) => setModes(e.target.checked ? [...modes, m] : modes.filter((x) => x !== m))}
                      />
                      {tx.modes[m as keyof typeof tx.modes]}
                    </label>
                  ))}
                </div>
              )}
            </SettingsRow>
            <SettingsRow title={tx.fromLabel} help={tx.windowHelp} controlId="limit-from">
              <Input id="limit-from" type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
            </SettingsRow>
            <SettingsRow title={tx.untilLabel} controlId="limit-until">
              <Input id="limit-until" type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value)} />
            </SettingsRow>
          </SettingsRows>
          {(from || until) && <p className="text-xs text-muted-foreground">{tx.replaceNote}</p>}
          <DialogFooter className="max-sm:flex-col-reverse">
            {hasManual && (
              <Button
                type="button"
                variant="outline"
                className="sm:mr-auto"
                disabled={put.isPending}
                onClick={() => send(null)}
              >
                <Trash2 aria-hidden="true" />
                {tx.remove}
              </Button>
            )}
            <Button type="button" variant="outline" onClick={onClose}>
              {tt.list.cancel}
            </Button>
            <Button type="submit" disabled={put.isPending}>
              <Save aria-hidden="true" />
              {put.isPending ? tx.saving : tx.save}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
