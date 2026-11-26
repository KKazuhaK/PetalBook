import { useState } from 'react'
import { Pencil } from 'lucide-react'
import { useAdminUsage, useTenants } from '@/api/admin'
import { useAcrossSite } from './TenantScope'
import type { PersonUsage, Tenant, TenantQuotaKey } from '@/api/types'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { formatBytes } from '@/lib/bytes'
import { formatLimit, formatUsed, share } from '@/lib/usage'
import { absoluteTime } from '@/lib/time'
import { tu } from '@/i18n/usage'
import { OwnLimits } from './OwnLimits'

// Usage, for administrators: what each tenant uses (a site administrator sees them all) and what each person uses, the
// most disk first. A tenant's row narrows the people to it. Every number is a stored measurement: nothing here measures.
export function UsagePanel() {
  const site = useAcrossSite()
  const [tenant, setTenant] = useState('')
  const [overOnly, setOverOnly] = useState(false)
  const tenants = useTenants(site)
  // Only a site administrator has tenants to choose between, and only when there is more than the default one.
  const many = site && (tenants.data?.length ?? 0) > 1
  return (
    <div className="grid min-w-0 gap-8">
      {site && tenants.data && (many || tenants.data.some((t) => t.usage.over.length > 0)) && (
        <TenantUsageList
          tenants={tenants.data}
          selected={tenant}
          onSelect={(id) => setTenant(id === tenant ? '' : id)}
        />
      )}
      {site && tenants.isPending && <PageFallback />}
      <PeopleUsage
        tenant={many ? tenant : ''}
        tenants={many ? tenants.data! : []}
        onTenant={setTenant}
        overOnly={overOnly}
        onOverOnly={setOverOnly}
      />
    </div>
  )
}

function TenantUsageList({
  tenants,
  selected,
  onSelect,
}: {
  tenants: Tenant[]
  selected: string
  onSelect: (id: string) => void
}) {
  return (
    <SettingsSection title={tu.admin.tenants} id="usage-tenants-heading" description={tu.admin.help}>
      <ul className="divide-y rounded-lg border" data-slot="tenant-usage">
        {tenants.map((tn) => (
          <li
            key={tn.id}
            className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2.5"
            data-tenant={tn.id}
          >
            <div className="min-w-0 flex-1 basis-48">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate text-sm font-medium">{tn.name}</span>
                {tn.usage.over.length > 0 && (
                  <Badge variant="destructive" data-slot="over">
                    {tu.admin.overFor(tn.usage.over.map((k) => tu.quotas[k]).join('、'))}
                  </Badge>
                )}
              </div>
              <div className="text-xs break-words text-muted-foreground">
                {tu.admin.tenantPeople(tn.usage.people)} ·{' '}
                {tu.admin.tenantLine(tn.usage.spaces, formatBytes(tn.usage.bytes), tn.usage.pages)}
              </div>
              <TenantTotals tenant={tn} />
            </div>
            <Button
              type="button"
              variant={selected === tn.id ? 'secondary' : 'ghost'}
              size="sm"
              aria-pressed={selected === tn.id}
              aria-label={`${tu.admin.people}: ${tn.name}`}
              onClick={() => onSelect(tn.id)}
            >
              {tu.admin.people}
            </Button>
          </li>
        ))}
      </ul>
    </SettingsSection>
  )
}

// The totals a tenant is held to, when it has any.
function TenantTotals({ tenant }: { tenant: Tenant }) {
  const set = (['people', 'spaces', 'bytes', 'pages'] as TenantQuotaKey[]).filter((k) => tenant.quota[k] !== null)
  if (set.length === 0) return null
  return (
    <div className="text-xs break-words text-muted-foreground" data-slot="tenant-totals">
      {tu.admin.tenantTotals}:{' '}
      {set
        .map((k) => `${tu.quotas[k]} ${formatUsed(k, tenant.usage[k])} / ${formatLimit(k, tenant.quota[k])}`)
        .join(' · ')}
    </div>
  )
}

function PeopleUsage({
  tenant,
  tenants,
  onTenant,
  overOnly,
  onOverOnly,
}: {
  tenant: string
  tenants: Tenant[]
  onTenant: (id: string) => void
  overOnly: boolean
  onOverOnly: (on: boolean) => void
}) {
  const list = useAdminUsage({ tenant, over: overOnly })
  const [editing, setEditing] = useState<PersonUsage | null>(null)
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  return (
    <SettingsSection title={tu.admin.people} id="usage-people-heading" description={tu.admin.peopleHelp}>
      <div className="flex flex-wrap items-end gap-x-6 gap-y-3">
        {tenants.length > 1 && (
          <div className="grid gap-1.5">
            <Label htmlFor="usage-tenant">{tu.admin.tenantFilter}</Label>
            <div className="w-56 max-w-full">
              <NativeSelect id="usage-tenant" value={tenant} onChange={(e) => onTenant(e.target.value)}>
                <option value="">{tu.admin.allTenants}</option>
                {tenants.map((tn) => (
                  <option key={tn.id} value={tn.id}>
                    {tn.name}
                  </option>
                ))}
              </NativeSelect>
            </div>
          </div>
        )}
        <label className="flex items-center gap-2 pb-1.5 text-sm">
          <Switch checked={overOnly} onCheckedChange={onOverOnly} aria-label={tu.admin.overOnly} />
          {tu.admin.overOnly}
        </label>
      </div>
      {list.isPending ? (
        <PageFallback />
      ) : list.isError ? (
        <ErrorState message={tu.admin.loadFailed} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{overOnly ? tu.admin.noneOver : tu.admin.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="people-usage">
          {items.map((p) => (
            <PersonRow key={p.user.id} person={p} onEdit={() => setEditing(p)} />
          ))}
        </ul>
      )}
      {list.hasNextPage && (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="w-fit"
          disabled={list.isFetchingNextPage}
          onClick={() => void list.fetchNextPage()}
        >
          {list.isFetchingNextPage ? tu.admin.loading : tu.admin.more}
        </Button>
      )}
      {editing && (
        <Dialog open onOpenChange={(o) => !o && setEditing(null)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{tu.admin.editLimitsFor(editing.user.name)}</DialogTitle>
              <DialogDescription>{editing.user.username}</DialogDescription>
            </DialogHeader>
            <OwnLimits userId={editing.user.id} own={editing.own} />
          </DialogContent>
        </Dialog>
      )}
    </SettingsSection>
  )
}

function PersonRow({ person: p, onEdit }: { person: PersonUsage; onEdit: () => void }) {
  return (
    <li className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1.5 px-3 py-2.5" data-person={p.user.id}>
      <div className="min-w-0 flex-1 basis-48">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium">{p.user.name}</span>
          {p.over.length > 0 && (
            <Badge variant="destructive" data-slot="over">
              {tu.admin.overFor(p.over.map((k) => tu.quotas[k]).join('、'))}
            </Badge>
          )}
        </div>
        <div className="truncate text-xs text-muted-foreground">{p.user.username}</div>
      </div>
      <dl className="grid min-w-0 grid-cols-1 gap-x-4 gap-y-0.5 text-xs sm:grid-cols-3" data-slot="person-numbers">
        {(['spaces', 'bytes', 'pages'] as const).map((k) => {
          const part = share(p[k], p.limits[k])
          return (
            <div
              key={k}
              className={`min-w-0 break-words ${part !== null && part > 1 ? 'text-destructive' : 'text-muted-foreground'}`}
              data-quota={k}
            >
              <dt className="inline">{tu.quotas[k]} </dt>
              <dd className="inline tabular-nums">
                {formatUsed(k, p[k])}
                {p.limits[k] !== null && ` / ${formatLimit(k, p.limits[k])}`}
              </dd>
            </div>
          )
        })}
      </dl>
      <span className="text-xs text-muted-foreground">
        {p.measuredAt ? absoluteTime(p.measuredAt) : tu.neverMeasured}
      </span>
      <Button type="button" variant="ghost" size="sm" aria-label={tu.admin.editLimitsFor(p.user.name)} onClick={onEdit}>
        <Pencil aria-hidden="true" />
        {tu.admin.editLimits}
      </Button>
    </li>
  )
}
