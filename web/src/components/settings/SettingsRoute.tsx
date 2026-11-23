import { lazy, Suspense, useCallback, type ReactNode } from 'react'
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router'
import { Dialog as DialogPrimitive } from '@base-ui/react/dialog'
import { X } from 'lucide-react'
import { useActiveSpace, useSession } from '@/api/hooks'
import { can } from '@/lib/permissions'
import { PageFallback } from '@/components/common'
import { Button } from '@/components/ui/button'
import { DialogOverlay, DialogPortal, DialogTitle } from '@/components/ui/dialog'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n'
import { GeneralPanel } from './GeneralPanel'
import { ImportPanel } from './ImportPanel'
import { PreferencesPanel } from './PreferencesPanel'
import { ProfilePanel } from './ProfilePanel'
import { SecurityPanel } from './SecurityPanel'
import { SectionIcon } from './SettingsParts'
import { groupsOf, resolveSection, settingsPath, visibleSections, type Visibility } from './sections'
import type { SettingsState } from './useSettings'
import { SyncPanel } from './SyncPanel'
import { MembersPanel } from './MembersPanel'

// The page types' editor is a chunk of its own, loaded when its section is opened.
const TypesPanel = lazy(() => import('./TypesPanel'))
// So is the home page's setup, which has the words of the home page with it.
const HomePanel = lazy(() => import('./HomePanel'))
// And the templates', which have the words of templates with them.
const TemplatesPanel = lazy(() => import('./TemplatesPanel'))

// The settings surface: one dialog, reached from `/settings/<section>`. It is a
// route so a link, a reload and the old addresses all work; what is behind it is
// not drawn (the address says "settings", not "settings over this page"), and
// closing goes back to where the person came from.
export default function SettingsRoute() {
  const { section: requested } = useParams()
  const location = useLocation()
  const navigate = useNavigate()
  const session = useSession().data
  const { id: space, spaces, resolving } = useActiveSpace()
  const spacesReady = !resolving

  const current = spaces.find((s) => s.id === space)
  const visibility: Visibility = {
    mode: session?.mode,
    signedIn: !!session?.user,
    hasSpace: !!space,
    canRead: can(current, 'read'),
    canWrite: can(current, 'write'),
    canManage: can(current, 'manage'),
    disabled: current?.disabled === true,
  }
  const shown = visibleSections(visibility)
  const section = resolveSection(requested, visibility)
  useDocumentTitle(`${t.settingsUi.sections[section]} · ${t.settingsUi.title} · ${t.appName}`)

  const from = (location.state as SettingsState | null)?.from
  const close = useCallback(() => {
    // Opened from inside the app: the entry before this one is where the person was.
    if (from) navigate(-1)
    else navigate('/', { replace: true })
  }, [from, navigate])

  // An address that names no section, or one this person does not have, goes
  // to the section they would get; the query (a failed sign-in link says why in
  // `?error=`) and where to return to are kept.
  // A link that failed comes back to plain `/settings?error=…`: its message is
  // shown where the linked sign-ins are.
  const landing = requested === undefined && new URLSearchParams(location.search).has('error') && shown.some((s) => s.id === 'security') ? 'security' : section
  if (session && spacesReady && requested !== landing) return <Navigate to={{ pathname: settingsPath(landing), search: location.search }} state={location.state} replace />

  // The sections that need a space are only offered while there is one, so
  // `sp` is a real space id whenever one of them is showing.
  const sp = space ?? ''
  let body: ReactNode = <PageFallback />
  if (session && spacesReady) {
    switch (section) {
      case 'profile':
        body = <ProfilePanel />
        break
      case 'security':
        body = <SecurityPanel />
        break
      case 'preferences':
        body = <PreferencesPanel />
        break
      case 'general':
        body = <GeneralPanel space={sp} />
        break
      case 'types':
        body = (
          <Suspense fallback={<PageFallback />}>
            <TypesPanel canManage={can(current, 'manage')} />
          </Suspense>
        )
        break
      case 'templates':
        body = (
          <Suspense fallback={<PageFallback />}>
            <TemplatesPanel canManage={can(current, 'manage')} />
          </Suspense>
        )
        break
      case 'home':
        body = (
          <Suspense fallback={<PageFallback />}>
            <HomePanel canManage={can(current, 'manage')} />
          </Suspense>
        )
        break
      case 'members':
        body = <MembersPanel space={sp} />
        break
      case 'sync':
        body = <SyncPanel space={sp} />
        break
      case 'import':
        body = <ImportPanel space={sp} />
        break
    }
  }

  return (
    <DialogPrimitive.Root open onOpenChange={(open) => !open && close()}>
      <DialogPortal>
        <DialogOverlay />
        <DialogPrimitive.Popup
          data-slot="settings-dialog"
          className="fixed inset-0 z-50 flex flex-col overflow-hidden bg-background text-sm text-foreground outline-none duration-100 data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0 md:inset-auto md:top-1/2 md:left-1/2 md:h-[min(46rem,calc(100dvh-4rem))] md:w-[min(64rem,calc(100vw-4rem))] md:-translate-x-1/2 md:-translate-y-1/2 md:flex-row md:rounded-xl md:shadow-2xl md:ring-1 md:ring-foreground/10 md:data-open:zoom-in-95"
        >
          <aside className="flex shrink-0 flex-col border-b md:w-60 md:border-r md:border-b-0 md:bg-sidebar md:text-sidebar-foreground">
            <div className="flex items-center justify-between px-4 pt-3 pb-1 md:px-5 md:pt-5 md:pb-3">
              <DialogTitle className="text-base font-semibold">{t.settingsUi.title}</DialogTitle>
              <DialogPrimitive.Close render={<Button variant="ghost" size="icon-sm" className="md:hidden" />}>
                <X />
                <span className="sr-only">{t.settingsUi.close}</span>
              </DialogPrimitive.Close>
            </div>
            <nav aria-label={t.settingsUi.nav} className="flex gap-4 overflow-x-auto [scrollbar-width:none] px-3 pb-2 md:flex-1 md:flex-col md:gap-4 md:overflow-x-visible md:overflow-y-auto md:px-3 md:pb-4">
              {groupsOf(shown).map(({ group, sections }) => (
                <div key={group} role="group" aria-label={t.settingsUi.groups[group]} className="flex items-center gap-1 md:block">
                  <div className="hidden px-2 pb-1 text-xs font-medium text-muted-foreground md:block">
                    {t.settingsUi.groups[group]}
                    {group === 'space' && current && <span className="ml-1 font-normal">· {current.name}</span>}
                  </div>
                  <ul className="flex gap-1 md:flex-col md:gap-0.5">
                    {sections.map((s) => (
                      <li key={s.id}>
                        <Link
                          to={settingsPath(s.id)}
                          replace
                          state={location.state}
                          aria-current={s.id === section ? 'page' : undefined}
                          className={`flex h-8 items-center gap-2 rounded-md px-2.5 text-sm whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${s.id === section ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground' : 'text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground'}`}
                        >
                          <SectionIcon id={s.id} name={session?.user?.name} />
                          {t.settingsUi.sections[s.id]}
                        </Link>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </nav>
          </aside>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col">
            <header className="flex h-12 shrink-0 items-center justify-between border-b px-4 md:px-8">
              <h2 className="truncate text-sm font-semibold">{t.settingsUi.sections[section]}</h2>
              <DialogPrimitive.Close render={<Button variant="ghost" size="icon-sm" className="max-md:hidden" />}>
                <X />
                <span className="sr-only">{t.settingsUi.close}</span>
              </DialogPrimitive.Close>
            </header>
            <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
              <div className="mx-auto w-full max-w-2xl px-4 py-5 md:px-8 md:py-7">{body}</div>
            </div>
          </div>
        </DialogPrimitive.Popup>
      </DialogPortal>
    </DialogPrimitive.Root>
  )
}
