import { Navigate, useLocation } from 'react-router'
import { ApiError } from '@/api/client'
import { useTenant } from '@/api/admin'
import { useSession } from '@/api/hooks'
import { PageFallback } from '@/components/common'
import { NotFoundPage } from '@/pages/NotFoundPage'
import { acrossSite } from '@/lib/access'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { AdminLayout } from './AdminLayout'
import { AdminPageHeader } from './AdminPageHeader'
import { AdminTabs } from './AdminTabs'
import { navFor } from './adminNav'

// The tenant made with the server: its people are no organisation's (their spaces are their own).
const DEFAULT_TENANT_ID = '0000000000'
import { ScopeBanner, TenantScope, useAdminPaths } from './TenantScope'
import { TenantPage } from './TenantPage'
import { PersonPage } from './users/PersonPage'

// The administration: the sidebar's sections, each with the pages along its top, and a page of its own for each person.
// It is a route of its own, loaded only when an administrator goes there (see the gate in App.tsx), and everything on
// it is an endpoint only administrators may call. The address is `/admin/<section>/<tab>` or `/admin/user/<id>/<tab>`;
// the older `/admin/manage`, `/admin/recycle`, `/admin/access` and `/admin/usage` open in their new place.
//
// A site administrator may manage one tenant as its administrator: the same console under `/admin/t/<id>/...`, in that
// tenant's scope (docs/design/3-6-c.md section 5): the same pages a tenant's administrator has, every request carrying
// the scope header, and a bar on top that says so and leads out. A tenant's own page for the site is
// `/admin/tenants/<id>/<tab>`.
export default function AdminRoute() {
  const { pathname } = useLocation()
  const parts = pathname
    .split('/')
    .filter(Boolean)
    .slice(1)
    .map((s) => decodeURIComponent(s))
  const scoped = parts[0] === 't' && parts[1] !== undefined
  const scope = scoped ? parts[1] : null
  const rest = scoped ? parts.slice(2) : parts
  const siteUser = acrossSite(useSession().data?.user)
  // A tenant's own administrator has no scope to enter: their console is their tenant's already.
  if (scope !== null && !siteUser) return <Navigate to={['/admin', ...rest].join('/')} replace />
  return (
    <TenantScope tenant={scope}>
      <Console scope={scope} segments={rest} />
    </TenantScope>
  )
}

function Console({ scope, segments }: { scope: string | null; segments: string[] }) {
  const [first, second, third] = segments
  const { path } = useAdminPaths()
  const site = acrossSite(useSession().data?.user) && scope === null
  const tenant = useTenant(scope)
  const ownTenant = useSession().data?.user?.tenant
  const ownTenantName = ownTenant?.name
  // An organisation's console: a tenant's (its own administrator's, or a site administrator's in its scope), not the
  // default tenant's, which is no organisation.
  const org = !site && (scope !== null || (ownTenant !== undefined && ownTenant.id !== DEFAULT_TENANT_ID))
  const sections = navFor(site, org)
  const person = first === 'user' && second !== undefined
  // A tenant's own page: `/admin/tenants/<id>/<tab>`; the list's two pages are tenants and plans.
  const ofTenant = site && first === 'tenants' && second !== undefined && second !== 'tenants' && second !== 'plans'
  const section = person ? sections.find((s) => s.key === 'users') : sections.find((s) => s.key === first)
  const tabs = section?.tabs ?? []
  const tab = person || ofTenant ? undefined : (tabs.find((x) => x.key === second) ?? tabs[0])
  useDocumentTitle(
    `${(person || ofTenant ? section?.label : tab && tabs.length > 1 ? `${tab.label} · ${section?.label}` : section?.label) ?? tc.title} · ${tc.title} · ${t.appName}`,
  )

  // A tenant that is not there (or not this person's to manage) is an address that does not exist.
  if (scope !== null && tenant.isError && tenant.error instanceof ApiError && tenant.error.status === 404)
    return <NotFoundPage />
  if (scope !== null && tenant.isPending) return <PageFallback />
  if (!section) return <Navigate to={path('users')} replace />
  if (!person && !ofTenant && second !== undefined && tab?.key !== second)
    return <Navigate to={path(section.key)} replace />
  if (!person && !ofTenant && third !== undefined) return <Navigate to={path(section.key, tab?.key)} replace />

  return (
    <AdminLayout
      sections={sections}
      active={section.key}
      banner={scope !== null ? <ScopeBanner tenantId={scope} name={tenant.data?.name} /> : undefined}
    >
      {person ? (
        <PersonPage id={second} tab={third} />
      ) : ofTenant ? (
        <TenantPage id={second} tab={third} />
      ) : (
        <AdminPageHeader
          title={section.label}
          description={pageHelp(`${section.key}/${tab?.key}`, site, scope !== null ? tenant.data?.name : ownTenantName)}
        >
          {tabs.length > 1 && (
            <AdminTabs
              tabs={tabs.map((x) => ({ key: x.key, label: x.label, to: path(section.key, x.key) }))}
              active={tab?.key}
            />
          )}
          <div className="min-w-0 animate-in duration-150 fade-in-0" key={`${section.key}/${tab?.key}`}>
            {tab?.render()}
          </div>
        </AdminPageHeader>
      )}
    </AdminLayout>
  )
}

// A page's line of help: in a tenant (its own administrator, or the site's in its scope) the pages that speak of the
// whole server speak of the tenant.
function pageHelp(key: string, site: boolean, tenantName: string | undefined): string | undefined {
  const inTenant = site ? undefined : tc.pageHelpTenant[key]
  return inTenant ? inTenant(tenantName) : (tc.pageHelp as Record<string, string>)[key]
}
