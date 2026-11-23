import { Link, Navigate, useParams } from 'react-router'
import { lazy, Suspense } from 'react'
import { ArrowLeft, Building2, HardDrive, KeyRound, Layers, Quote, ShieldCheck, SlidersHorizontal, Users, UsersRound } from 'lucide-react'
import { TopBar } from '@/components/layout/TopBar'
import { useActiveSpace, useSession } from '@/api/hooks'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { adminPath, isAdminSection, spaceUrls, type AdminSection } from '@/lib/urls'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'
import { PageFallback } from '@/components/common'
import { GroupsPanel } from './GroupsPanel'
import { SettingsPanel } from './SettingsPanel'
import { TenantsPanel } from './TenantsPanel'
import { SpacesPanel } from './SpacesPanel'
import { StatusPanel } from './StatusPanel'
import { UsersPanel } from './UsersPanel'

// The daily text's page is a chunk of its own: it is the biggest of the administration's and the least visited.
const DailyPanel = lazy(() => import('./DailyPanel').then((m) => ({ default: m.DailyPanel })))
const AccessPanel = lazy(() => import('./AccessPanel').then((m) => ({ default: m.AccessPanel })))
// The security page, with the providers and the audit log, is another.
const SecurityPanel = lazy(() => import('./AdminSecurityPanel').then((m) => ({ default: m.AdminSecurityPanel })))

const ICONS: Record<AdminSection, typeof Users> = { users: Users, groups: UsersRound, spaces: Layers, daily: Quote, access: KeyRound, security: ShieldCheck, settings: SlidersHorizontal, tenants: Building2, status: HardDrive }
const SECTIONS: readonly AdminSection[] = ['users', 'groups', 'spaces', 'daily', 'access', 'security', 'settings', 'tenants', 'status']
// What only an administrator of the whole server may open: the server's own settings. A tenant's
// administrator sees the rest, for their tenant, and the settings page for their tenant's layer.
const SITE_ONLY: readonly AdminSection[] = ['daily', 'access', 'security', 'tenants', 'status']

// The administration: users and invites, every space, the state of the server.
// It is a route of its own, loaded only when an administrator goes there (see
// the gate in App.tsx), and everything on it is an endpoint only administrators
// may call.
export default function AdminRoute() {
  const { section } = useParams()
  const { id } = useActiveSpace()
  // The server says siteAdmin on every user; a user object without it is from before tenants, when every administrator was one.
  const siteAdmin = useSession().data?.user?.siteAdmin !== false
  const sections = SECTIONS.filter((s) => siteAdmin || !SITE_ONLY.includes(s))
  useDocumentTitle(`${isAdminSection(section) ? ta.sections[section] : ta.title} · ${ta.title} · ${t.appName}`)
  if (!isAdminSection(section) || !sections.includes(section)) return <Navigate to={adminPath('users')} replace />

  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{ta.title}</h1>
      </TopBar>
      <div className="mx-auto w-full max-w-5xl min-w-0 flex-1 px-4 py-5 md:px-8 md:py-7">
        <div className="mb-6 flex flex-wrap items-center gap-x-6 gap-y-2">
          <nav aria-label={ta.nav}>
            <ul className="flex flex-wrap gap-1">
              {sections.map((s) => {
                const Icon = ICONS[s]
                return (
                  <li key={s}>
                    <Link
                      to={adminPath(s)}
                      aria-current={s === section ? 'page' : undefined}
                      className={`flex h-8 items-center gap-2 rounded-md px-2.5 text-sm whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${s === section ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'}`}
                    >
                      <Icon aria-hidden="true" className="size-4 shrink-0" strokeWidth={1.75} />
                      {ta.sections[s]}
                    </Link>
                  </li>
                )
              })}
            </ul>
          </nav>
          <Link to={id ? spaceUrls(id).home() : '/'} className="ml-auto flex items-center gap-1 text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
            <ArrowLeft className="size-3.5" />
            {ta.back}
          </Link>
        </div>
        {section === 'users' && <UsersPanel />}
        {section === 'groups' && <GroupsPanel />}
        {section === 'spaces' && <SpacesPanel />}
        {section === 'daily' && (
          <Suspense fallback={<PageFallback />}>
            <DailyPanel />
          </Suspense>
        )}
        {section === 'access' && (
          <Suspense fallback={<PageFallback />}>
            <AccessPanel />
          </Suspense>
        )}
        {section === 'security' && (
          <Suspense fallback={<PageFallback />}>
            <SecurityPanel />
          </Suspense>
        )}
        {section === 'settings' && <SettingsPanel />}
        {section === 'tenants' && <TenantsPanel />}
        {section === 'status' && <StatusPanel />}
      </div>
    </>
  )
}
