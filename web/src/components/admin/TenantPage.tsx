import { lazy, Suspense, type ReactNode } from 'react'
import { Link, Navigate, useNavigate } from 'react-router'
import { ArrowLeft } from 'lucide-react'
import { useTenantOverview } from '@/api/admin'
import type { TenantOverview } from '@/api/types'
import { ErrorState, PageFallback } from '@/components/common'
import { Badge } from '@/components/ui/badge'
import { featureName } from '@/i18n/featureNames'
import { tsec } from '@/i18n/security'
import { tt } from '@/i18n/tenants'
import { adminKindOf } from '@/lib/access'
import { formatBytes } from '@/lib/bytes'
import { relativeTime } from '@/lib/time'
import { adminPath, adminTenantPath } from '@/lib/urls'
import { formatUsed, share } from '@/lib/usage'
import { AdminPageHeader } from './AdminPageHeader'
import { AdminTabs } from './AdminTabs'
import { SecretsSection } from './SecretsSection'
import { TenantLayer } from './SettingsPanel'
import { SpacesPanel } from './SpacesPanel'
import { ManageAsButton, TenantScope, useAdminPaths } from './TenantScope'
import { TenantEditor, TenantPlanBlock, TenantUsageBlock } from './TenantsPanel'
import { UsersList } from './users/UsersList'

const AuditSection = lazy(() => import('./AuditSection').then((m) => ({ default: m.AuditSection })))

const td = tt.detail
const to = td.overview

export const TENANT_TABS = ['overview', 'members', 'spaces', 'settings', 'plan', 'audit', 'danger'] as const
type TenantTab = (typeof TENANT_TABS)[number]
// The tabs that show the tenant's own people, spaces and log: they read in the tenant's scope, as its administrator
// would, so they are the pages of the console and are never repeated here.
const SCOPED: readonly TenantTab[] = ['members', 'spaces', 'audit']

// A tenant as the site sees it: `/admin/tenants/<id>/<tab>`. The overview is one call; the people, spaces and audit tabs
// are the console's own pages in the tenant's scope; the plan, the rules, the settings and the danger tab act on the
// tenant from the site. "Manage as tenant admin" opens the console in its scope.
export function TenantPage({ id, tab }: { id: string; tab?: string }) {
  const overview = useTenantOverview(id)
  const navigate = useNavigate()
  const active = TENANT_TABS.find((x) => x === tab)
  if (tab !== undefined && !active) return <Navigate to={adminTenantPath(id)} replace />
  if (overview.isPending) return <PageFallback />
  if (overview.isError) return <ErrorState message={td.loadFailed} onRetry={() => void overview.refetch()} />
  const o = overview.data
  const tenant = o.tenant
  const current: TenantTab = active ?? 'overview'
  const body: Record<TenantTab, () => ReactNode> = {
    overview: () => <Overview o={o} />,
    members: () => <UsersList />,
    spaces: () => <SpacesPanel />,
    settings: () => (
      <div className="grid gap-8">
        <p className="text-sm text-muted-foreground">{td.settingsHelp}</p>
        <TenantLayer tenant={tenant.id} name={tenant.name} isSite />
        <SecretsSection tenant={tenant.id} name={tenant.name} />
      </div>
    ),
    plan: () => (
      <div className="grid gap-4 [&>*:first-child]:border-t-0 [&>*:first-child]:pt-0">
        <TenantPlanBlock tenant={tenant} />
        <TenantUsageBlock tenant={tenant} />
      </div>
    ),
    audit: () => (
      <Suspense fallback={<PageFallback />}>
        <AuditSection />
      </Suspense>
    ),
    danger: () => <TenantEditor tenant={tenant} onGone={() => navigate(adminPath('tenants', 'tenants'))} />,
  }
  return (
    <div className="grid min-w-0 gap-3">
      <Link
        to={adminPath('tenants', 'tenants')}
        className="inline-flex h-10 w-fit items-center gap-1.5 rounded-md text-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 lg:h-8"
      >
        <ArrowLeft className="size-4" aria-hidden="true" />
        {td.back}
      </Link>
      <AdminPageHeader
        title={tenant.name}
        description={tenant.slug}
        meta={
          <>
            {tenant.default && <Badge variant="secondary">{tt.list.default}</Badge>}
            {tenant.disabled && <Badge variant="secondary">{tt.list.disabled}</Badge>}
            {tenant.origin === 'self' && <Badge variant="outline">{tt.list.originSelf}</Badge>}
          </>
        }
        actions={<ManageAsButton tenant={tenant.id} />}
      >
        <AdminTabs
          label={td.tabsLabel}
          active={current}
          tabs={TENANT_TABS.map((x) => ({ key: x, label: td.tabs[x], to: adminTenantPath(id, x) }))}
        />
        <div className="min-w-0 animate-in duration-150 fade-in-0" key={current}>
          <TenantScope tenant={SCOPED.includes(current) ? id : null}>{body[current]()}</TenantScope>
        </div>
      </AdminPageHeader>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0 rounded-lg border px-3 py-2.5" data-stat={label}>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-lg font-semibold tabular-nums">{value}</dd>
    </div>
  )
}

function Card({ title, children, className = '' }: { title: string; children: ReactNode; className?: string }) {
  return (
    <section className={`min-w-0 rounded-lg border p-4 ${className}`}>
      <h2 className="mb-3 text-sm font-medium">{title}</h2>
      {children}
    </section>
  )
}

function Overview({ o }: { o: TenantOverview }) {
  const { tenant } = o
  const u = tenant.usage
  const limit = tenant.quota.bytes ?? null
  const used = share(u.bytes, limit)
  const { userPath } = useAdminPaths()
  const more = o.adminCount - o.admins.length
  return (
    <div className="grid min-w-0 gap-4 lg:grid-cols-2" data-slot="tenant-overview">
      <Card title={to.identity}>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">{to.slug}</dt>
          <dd className="[overflow-wrap:anywhere]">{tenant.slug}</dd>
          <dt className="text-muted-foreground">{to.state}</dt>
          <dd>{tenant.disabled ? tt.list.disabled : to.active}</dd>
          <dt className="text-muted-foreground">{to.plan}</dt>
          <dd>{tenant.plan?.name ?? to.noPlan}</dd>
          <dt className="text-muted-foreground">{to.created}</dt>
          <dd>
            {to.origin[tenant.origin]}
            {o.createdByName ? ` · ${o.createdByName}` : ''} · {relativeTime(tenant.created)}
          </dd>
        </dl>
      </Card>
      <Card title={to.counts}>
        <dl className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          <Stat label={to.people} value={formatUsed('people', u.people)} />
          <Stat label={to.admins} value={o.adminCount.toLocaleString()} />
          <Stat label={to.spaces} value={formatUsed('spaces', u.spaces)} />
          <Stat label={to.groups} value={o.groups.toLocaleString()} />
          <Stat label={to.pages} value={formatUsed('pages', u.pages)} />
        </dl>
      </Card>
      <Card title={to.storage}>
        <p className="text-sm tabular-nums" data-slot="storage-used">
          {limit === null ? formatBytes(u.bytes) : to.storageOf(formatBytes(u.bytes), formatBytes(limit))}
        </p>
        {used !== null && (
          <div
            role="progressbar"
            aria-label={to.storage}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.min(100, Math.round(used * 100))}
            className="mt-2 h-2 overflow-hidden rounded-full bg-muted"
          >
            <div
              className={`h-full rounded-full ${used > 1 ? 'bg-destructive' : 'bg-brand'}`}
              style={{ width: `${Math.min(100, used * 100)}%` }}
            />
          </div>
        )}
        <p className="mt-2 text-xs text-muted-foreground">
          {u.measuredAt ? to.measured(relativeTime(u.measuredAt)) : to.notMeasured}
        </p>
      </Card>
      <Card title={to.featuresOff}>
        {o.featuresOff.length === 0 ? (
          <p className="text-sm text-muted-foreground">{to.allOn}</p>
        ) : (
          <ul className="flex flex-wrap gap-1.5" data-slot="features-off">
            {o.featuresOff.map((f) => (
              <li key={f}>
                <Badge variant="secondary">{featureName(f)}</Badge>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Card title={to.adminsTitle}>
        {o.admins.length === 0 ? (
          <p className="text-sm text-muted-foreground">{to.noAdmins}</p>
        ) : (
          <ul className="divide-y text-sm" data-slot="tenant-admins">
            {o.admins.map((a) => (
              <li key={a.id} className="flex min-w-0 items-center gap-2 py-1.5">
                <Link
                  to={userPath(a.id)}
                  className="min-w-0 truncate rounded-sm outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
                >
                  {a.name}
                </Link>
                <span className="min-w-0 truncate text-xs text-muted-foreground">{a.username}</span>
                {adminKindOf({ role: 'admin', siteAdmin: a.siteAdmin }) === 'site' && (
                  <Badge variant="outline">{to.siteAdmin}</Badge>
                )}
                {a.disabled && <Badge variant="secondary">{tt.list.disabled}</Badge>}
              </li>
            ))}
          </ul>
        )}
        {more > 0 && <p className="mt-2 text-xs text-muted-foreground">{to.adminsMore(more)}</p>}
      </Card>
      <Card title={to.activityTitle}>
        {o.recentAdminActivity.length === 0 ? (
          <p className="text-sm text-muted-foreground">{to.noActivity}</p>
        ) : (
          <ul className="divide-y text-sm" data-slot="tenant-activity">
            {o.recentAdminActivity.map((r) => (
              <li key={r.id} className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 py-1.5">
                <span className="min-w-0 [overflow-wrap:anywhere]">{tsec.audit.kinds[r.kind] ?? r.kind}</span>
                {r.capacity === 'site_admin' && <Badge variant="outline">{to.byAdmin}</Badge>}
                {r.username && <span className="text-xs text-muted-foreground">{to.by(r.username)}</span>}
                <span className="ml-auto text-xs text-muted-foreground">{relativeTime(r.at)}</span>
              </li>
            ))}
          </ul>
        )}
        <Link
          to={adminTenantPath(tenant.id, 'audit')}
          className="mt-2 inline-flex h-10 items-center text-xs text-brand underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50 lg:h-8"
        >
          {to.viewAll}
        </Link>
      </Card>
    </div>
  )
}
