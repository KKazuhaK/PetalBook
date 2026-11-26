import { useMemo, useRef, useState, useSyncExternalStore, type FormEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { Download, RefreshCw, SlidersHorizontal, X } from 'lucide-react'
import { adminApi, auditQuery, useAuditKinds, useAuditLog, useTenants } from '@/api/admin'
import { useSession } from '@/api/hooks'
import type { AuditCapacity, AuditCategory, AuditFilters, AuditItem } from '@/api/types'
import { ErrorState } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button, buttonVariants } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import { acrossSite } from '@/lib/access'
import { useFeature } from '@/lib/features'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'

const ta = tsec.audit

const CATEGORIES: AuditCategory[] = ['security', 'admin', 'data', 'system', 'signins', 'reads', 'downloads', 'api']
const CAPACITIES: AuditCapacity[] = ['site_admin', 'tenant_admin']
const DAY = /^\d{4}-\d{2}-\d{2}$/

const show = (v: unknown): string => (typeof v === 'string' ? v : JSON.stringify(v))

// A row's detail as short `key=value` pairs; a nested object (what a change was from and to) is flattened one level.
export function detailText(detail: Record<string, unknown>): string {
  const parts: string[] = []
  for (const [k, v] of Object.entries(detail ?? {})) {
    if (v !== null && typeof v === 'object' && !Array.isArray(v))
      for (const [k2, v2] of Object.entries(v as Record<string, unknown>)) parts.push(`${k}.${k2}=${show(v2)}`)
    else parts.push(`${k}=${show(v)}`)
  }
  return parts.join(' ')
}

// The filters an address says: the same names as the API's query, so a link reproduces the view. Anything that is not
// a category, a capacity or a day is left out.
export function auditFiltersOf(p: URLSearchParams): AuditFilters {
  const day = (k: string) => (DAY.test(p.get(k) ?? '') ? p.get(k)! : '')
  const capacity = p.get('capacity') as AuditCapacity
  return {
    category: (p.get('category') ?? '').split(',').filter((c): c is AuditCategory => CATEGORIES.includes(c as never)),
    kind: p.get('kind') ?? '',
    user: p.get('user') ?? '',
    actor: p.get('actor') ?? '',
    capacity: CAPACITIES.includes(capacity) ? capacity : '',
    from: day('from'),
    to: day('to'),
    tenant: p.get('tenant') ?? '',
  }
}

// How many of the filters below the categories are set (what the phone's button says).
const activeCount = (f: AuditFilters) => [f.kind, f.user, f.actor, f.from, f.to, f.tenant].filter(Boolean).length

// Where the filters collapse into one button: a phone.
const PHONE = '(max-width: 639px)'
function usePhone(): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const mql = window.matchMedia(PHONE)
      mql.addEventListener('change', onChange)
      return () => mql.removeEventListener('change', onChange)
    },
    () => window.matchMedia(PHONE).matches,
    () => false,
  )
}

// The audit log, newest first: category chips, filters (kind, person, actor, dates, and the tenant for a site
// administrator) kept in the address, and a CSV download of what they show.
export function AuditSection() {
  const [params, setParams] = useSearchParams()
  const filters = useMemo(() => auditFiltersOf(params), [params])
  const set = (patch: Partial<AuditFilters>) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        const merged = { ...auditFiltersOf(prev), ...patch }
        for (const k of ['category', 'kind', 'user', 'actor', 'capacity', 'from', 'to', 'tenant']) next.delete(k)
        for (const [k, v] of auditQuery(merged)) next.set(k, v)
        return next
      },
      { replace: true },
    )
  const log = useAuditLog(filters)
  const kinds = useAuditKinds()
  const rows = log.data?.pages.flatMap((p) => p.items) ?? []
  const phone = usePhone()
  const downloadOn = useFeature('features.auditDownload') !== false
  const count = activeCount(filters)
  const fields = <Fields filters={filters} set={set} />
  return (
    <SettingsSection title={ta.title} description={ta.help} id="security-audit">
      <Chips filters={filters} set={set} infos={kinds.data?.categories} />
      <div className="flex flex-wrap items-end gap-3" data-slot="audit-filters">
        {phone ? (
          <Popover>
            <PopoverTrigger render={<Button type="button" variant="outline" className="h-10" />}>
              <SlidersHorizontal aria-hidden="true" />
              {count > 0 ? ta.filtersCount(count) : ta.filters}
            </PopoverTrigger>
            <PopoverContent
              align="start"
              className="w-[min(22rem,calc(100vw-2rem))] gap-3 p-3"
              aria-label={ta.filtersLabel}
            >
              {fields}
              {count + filters.category.length > 0 && <ClearButton set={set} />}
            </PopoverContent>
          </Popover>
        ) : (
          <>
            {fields}
            {count + filters.category.length > 0 && <ClearButton set={set} />}
          </>
        )}
        <div className="ml-auto flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            className={phone ? 'h-10' : undefined}
            onClick={() => void log.refetch()}
          >
            <RefreshCw aria-hidden="true" />
            {ta.refresh}
          </Button>
          {downloadOn && (
            <a
              href={adminApi.auditDownloadUrl(filters)}
              download
              title={ta.downloadHelp}
              className={buttonVariants({ variant: 'outline', className: phone ? 'h-10' : undefined })}
              data-slot="audit-download"
            >
              <Download aria-hidden="true" />
              {ta.download}
            </a>
          )}
        </div>
      </div>
      {log.isPending ? (
        <div aria-busy="true" className="h-16 animate-pulse rounded-lg bg-muted" />
      ) : log.isError ? (
        <ErrorState message={t.fmt.labelled(ta.loadFailed, log.error.message)} onRetry={() => void log.refetch()} />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {count + filters.category.length > 0 ? ta.emptyFiltered : ta.empty}
        </p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="audit-rows">
          {rows.map((r) => (
            <Row key={r.id} r={r} />
          ))}
        </ul>
      )}
      {log.hasNextPage && (
        <div>
          <Button
            type="button"
            variant="outline"
            disabled={log.isFetchingNextPage}
            onClick={() => void log.fetchNextPage()}
          >
            {log.isFetchingNextPage ? ta.loading : ta.loadMore}
          </Button>
        </div>
      )}
    </SettingsSection>
  )
}

function ClearButton({ set }: { set: (p: Partial<AuditFilters>) => void }) {
  return (
    <Button
      type="button"
      variant="ghost"
      onClick={() => set({ category: [], kind: '', user: '', actor: '', capacity: '', from: '', to: '', tenant: '' })}
    >
      <X aria-hidden="true" />
      {ta.clear}
    </Button>
  )
}

// The categories as a strip of toggles, scrolling sideways without a bar where it does not fit (its edges fade). An
// optional category that is off says so: nothing is being written to it.
function Chips({
  filters,
  set,
  infos,
}: {
  filters: AuditFilters
  set: (p: Partial<AuditFilters>) => void
  infos: { id: AuditCategory; on: boolean; setting: string | null }[] | undefined
}) {
  const strip = useRef<HTMLDivElement>(null)
  useScrollFade(strip)
  const toggle = (c: AuditCategory) =>
    set({ category: filters.category.includes(c) ? filters.category.filter((x) => x !== c) : [...filters.category, c] })
  return (
    <div className="min-w-0" role="group" aria-label={ta.categoriesLabel} data-slot="audit-categories">
      <div ref={strip} className={`${scrollFadeClass} -mx-1 flex gap-2 px-1 py-1`}>
        <Button
          type="button"
          size="sm"
          variant={filters.category.length === 0 ? 'default' : 'outline'}
          aria-pressed={filters.category.length === 0}
          className="h-10 shrink-0 lg:h-8"
          data-category=""
          onClick={() => set({ category: [] })}
        >
          {ta.allCategories}
        </Button>
        {CATEGORIES.map((c) => {
          const info = infos?.find((i) => i.id === c)
          const off = info !== undefined && !info.on
          const pressed = filters.category.includes(c)
          return (
            <Button
              key={c}
              type="button"
              size="sm"
              variant={pressed ? 'default' : 'outline'}
              aria-pressed={pressed}
              title={off && info.setting ? ta.categoryOffHint(info.setting) : undefined}
              className="h-10 shrink-0 lg:h-8"
              data-category={c}
              data-off={off ? '' : undefined}
              onClick={() => toggle(c)}
            >
              {ta.categories[c]}
              {off && <span className="text-xs font-normal opacity-70">{ta.categoryOff}</span>}
            </Button>
          )
        })}
      </div>
    </div>
  )
}

// The fields under the categories: laid out in a row that wraps on a wide screen, stacked in the phone's popover.
function Fields({ filters, set }: { filters: AuditFilters; set: (p: Partial<AuditFilters>) => void }) {
  const kinds = useAuditKinds()
  const user = useSession().data?.user
  const site = acrossSite(user)
  const tenants = useTenants(site).data ?? []
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const data = new FormData(e.currentTarget)
    set({ user: String(data.get('user') ?? '').trim(), actor: String(data.get('actor') ?? '').trim() })
  }
  const field = (label: string, id: string, control: ReactNode) => (
    <div className="grid min-w-0 gap-1.5 sm:w-44">
      <Label htmlFor={id}>{label}</Label>
      {control}
    </div>
  )
  // The kinds of the chosen categories (all of them when none is), grouped by category.
  const groups = (kinds.data?.categories ?? []).filter(
    (c) => filters.category.length === 0 || filters.category.includes(c.id),
  )
  return (
    <form className="contents" onSubmit={submit}>
      {field(
        ta.kind,
        'audit-kind',
        <NativeSelect id="audit-kind" value={filters.kind} onChange={(e) => set({ kind: e.target.value })}>
          <option value="">{ta.allKinds}</option>
          {filters.kind !== '' && !groups.some((g) => g.kinds.some((k) => k.name === filters.kind)) && (
            <option value={filters.kind}>{ta.kinds[filters.kind] ?? filters.kind}</option>
          )}
          {groups
            .filter((g) => g.kinds.length > 0)
            .map((g) => (
              <optgroup key={g.id} label={ta.categories[g.id]}>
                {g.kinds.map((k) => (
                  <option key={k.name} value={k.name}>
                    {ta.kinds[k.name] ?? k.name}
                  </option>
                ))}
              </optgroup>
            ))}
        </NativeSelect>,
      )}
      {field(
        ta.user,
        'audit-user',
        <Input
          id="audit-user"
          name="user"
          key={filters.user}
          defaultValue={filters.user}
          placeholder={ta.userPlaceholder}
          onBlur={(e) => set({ user: e.target.value.trim() })}
          autoComplete="off"
          spellCheck={false}
        />,
      )}
      {field(
        ta.actorLabel,
        'audit-actor',
        <Input
          id="audit-actor"
          name="actor"
          key={filters.actor}
          defaultValue={filters.actor}
          placeholder={ta.actorPlaceholder}
          onBlur={(e) => set({ actor: e.target.value.trim() })}
          autoComplete="off"
          spellCheck={false}
        />,
      )}
      {field(
        ta.from,
        'audit-from',
        <Input
          id="audit-from"
          type="date"
          value={filters.from}
          max={filters.to || undefined}
          onChange={(e) => set({ from: e.target.value })}
        />,
      )}
      {field(
        ta.to,
        'audit-to',
        <Input
          id="audit-to"
          type="date"
          value={filters.to}
          min={filters.from || undefined}
          onChange={(e) => set({ to: e.target.value })}
        />,
      )}
      {site &&
        tenants.length > 1 &&
        field(
          ta.tenant,
          'audit-tenant',
          <NativeSelect id="audit-tenant" value={filters.tenant} onChange={(e) => set({ tenant: e.target.value })}>
            <option value="">{ta.allTenants}</option>
            {tenants.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </NativeSelect>,
        )}
      <button type="submit" hidden />
    </form>
  )
}

function Row({ r }: { r: AuditItem }) {
  const [open, setOpen] = useState(false)
  const who = [
    r.username,
    r.user && r.user !== r.username ? r.user : '',
    r.actor ? ta.actor(r.actor) : r.detail?.by === 'cli' ? ta.cli : '',
  ]
    .filter(Boolean)
    .join(' · ')
  const detail = detailText(r.detail)
  return (
    <li
      className="grid min-w-0 grid-cols-1 gap-1 px-3 py-2.5 text-sm transition-colors hover:bg-muted/40"
      data-audit={r.id}
      data-category={r.category}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <Badge variant="secondary">{ta.kinds[r.kind] ?? r.kind}</Badge>
        <span className="text-xs text-muted-foreground">{ta.categories[r.category] ?? r.category}</span>
        {r.capacity && <Badge variant="outline">{ta.capacities[r.capacity] ?? r.capacity}</Badge>}
        <time dateTime={r.at} className="text-xs text-muted-foreground">
          {absoluteTime(r.at)}
        </time>
        {r.ip && <span className="text-xs break-all text-muted-foreground">{r.ip}</span>}
      </div>
      {who && <div className="text-xs break-all">{who}</div>}
      {detail !== '' && (
        <div>
          <div className={`font-mono text-xs break-all text-muted-foreground ${open ? '' : 'line-clamp-2'}`}>
            {detail}
          </div>
          {detail.length > 140 && (
            <button
              type="button"
              className="text-xs text-primary underline-offset-4 hover:underline"
              onClick={() => setOpen(!open)}
            >
              {open ? ta.showLess : ta.showMore}
            </button>
          )}
        </div>
      )}
    </li>
  )
}
