import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ArrowLeft, Menu, PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useActiveSpace, useSession } from '@/api/hooks'
import { can } from '@/lib/access'
import { Logo } from '@/components/brand/Logo'
import { topBarButton, topBarFrame } from '@/components/layout/topBarStyles'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import { spaceUrls, type AdminSection } from '@/lib/urls'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import type { NavSection } from './adminNav'
import { VersionBadge } from './VersionBadge'
import { useAdminPaths, useScope } from './TenantScope'
import { ScopeSelector } from './ScopeSelector'

// A row of the rail. Collapsed, a row is a 40 px square centred in the rail, so the icon is on the rail's centre line and
// the highlight is a square round it; open, it is a full-width row.
const RAIL_ITEM =
  'flex h-10 items-center rounded-md text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50'
const RAIL_ICON_ONLY = 'mx-auto w-10 justify-center'

const COLLAPSED_KEY = 'pb.admin.collapsed'

// Whether the sidebar starts collapsed: what the administrator chose (kept in this browser only), else on a tablet (under
// 1024 wide) it starts collapsed so the page has the room.
function startCollapsed(): boolean {
  try {
    const saved = localStorage.getItem(COLLAPSED_KEY)
    if (saved === '1' || saved === '0') return saved === '1'
  } catch {
    // Not kept; the width decides.
  }
  return typeof window !== 'undefined' && window.innerWidth > 0 && window.innerWidth < 1024
}

// The administration's frame: a sidebar of sections (a drawer on a phone, a rail that folds on a tablet) and the page
// beside it. It replaces the app's own sidebar while the administration is open.
export function AdminLayout({
  sections,
  active,
  banner,
  children,
}: {
  sections: NavSection[]
  active: AdminSection | null
  // The bar that says the console is in a tenant's scope (ScopeBanner): above everything, and the top bar sits under it.
  banner?: ReactNode
  children: ReactNode
}) {
  const [collapsed, setCollapsed] = useState(startCollapsed)
  const [open, setOpen] = useState(false)
  const { id, spaces } = useActiveSpace()
  const space = spaces.find((s) => s.id === id)
  // Only the administrators of the whole server may read the build (it is in the server's status).
  const scope = useScope()
  const site = can(useSession().data?.user, 'site.status.read') && scope === null
  const toggle = () =>
    setCollapsed((c) => {
      try {
        localStorage.setItem(COLLAPSED_KEY, c ? '0' : '1')
      } catch {
        // The choice holds until the page is closed.
      }
      return !c
    })
  const back = id ? spaceUrls(id).home() : '/'
  const current = sections.find((s) => s.key === active)

  return (
    <div
      className="flex min-h-dvh w-full min-w-0 flex-col bg-background [--banner-h:0px] data-[banner]:[--banner-h:calc(3rem+env(safe-area-inset-top,0px))]"
      data-slot="admin-layout"
      data-banner={banner ? '' : undefined}
    >
      {banner}
      <div className="flex min-w-0 flex-1">
        <aside
          aria-label={tc.nav}
          data-collapsed={collapsed || undefined}
          className={`sticky top-(--banner-h) hidden h-[calc(100dvh-var(--banner-h))] shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground transition-[width] duration-200 md:flex ${collapsed ? 'w-16' : 'w-[232px]'}`}
        >
          <Nav
            sections={sections}
            active={active}
            back={back}
            spaceName={space?.name}
            collapsed={collapsed}
            Title="p"
            footer={
              <>
                <VersionBadge site={site} collapsed={collapsed} />
                <button
                  type="button"
                  onClick={toggle}
                  aria-label={collapsed ? tc.expand : tc.collapse}
                  aria-expanded={!collapsed}
                  className={`${RAIL_ITEM} ${collapsed ? RAIL_ICON_ONLY : 'w-full gap-2 px-3'} text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground`}
                >
                  {collapsed ? (
                    <PanelLeftOpen className="size-4 shrink-0" aria-hidden="true" />
                  ) : (
                    <>
                      <PanelLeftClose className="size-4 shrink-0" aria-hidden="true" />
                      <span className="truncate">{tc.collapse}</span>
                    </>
                  )}
                </button>
              </>
            }
          />
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <header
            className={`${topBarFrame} md:hidden`}
            style={banner ? { top: 'var(--banner-h)', paddingTop: 0, height: '3.5rem' } : undefined}
          >
            <button
              type="button"
              onClick={() => setOpen(true)}
              aria-label={tc.menu}
              aria-haspopup="dialog"
              className={topBarButton}
            >
              <Menu className="size-5" aria-hidden="true" />
            </button>
            <p className="min-w-0 flex-1 truncate text-sm font-medium">{current?.label ?? tc.title}</p>
          </header>
          <ScopeSelector variant="bar" active={active} />
          <main className="mx-auto w-full max-w-6xl min-w-0 flex-1 px-4 py-5 md:px-8 md:py-7">{children}</main>
        </div>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent
            side="left"
            showCloseButton
            className="flex w-[min(20rem,calc(100vw-3rem))] flex-col gap-0 bg-sidebar p-0 text-sidebar-foreground md:hidden"
          >
            <SheetTitle className="sr-only">{tc.nav}</SheetTitle>
            <Nav
              sections={sections}
              active={active}
              back={back}
              spaceName={space?.name}
              collapsed={false}
              Title="p"
              footer={<VersionBadge site={site} collapsed={false} />}
              scopeControl={<ScopeSelector variant="drawer" active={active} onNavigate={() => setOpen(false)} />}
              onNavigate={() => setOpen(false)}
            />
          </SheetContent>
        </Sheet>
      </div>
    </div>
  )
}

function Nav({
  sections,
  active,
  back,
  spaceName,
  collapsed,
  footer,
  scopeControl,
  onNavigate,
  Title,
}: {
  sections: NavSection[]
  active: AdminSection | null
  back: string
  spaceName?: string
  collapsed: boolean
  footer?: ReactNode
  // Where the console acts (the phone's menu has it here; a wider screen has a strip above the page).
  scopeControl?: ReactNode
  onNavigate?: () => void
  // The heading of the page: the sidebar's own, the drawer's is a copy and is none.
  Title?: 'h1' | 'p'
}) {
  const { path } = useAdminPaths()
  const backText = spaceName ? tc.back(spaceName) : tc.backFallback
  return (
    <>
      <div className="shrink-0 px-2 pt-3">
        <div
          data-slot="admin-brand"
          className={`flex h-10 items-center ${collapsed ? 'mx-auto w-10 justify-center' : 'gap-2 px-3'}`}
        >
          <Logo />
          {!collapsed && <span className="truncate text-sm font-semibold">{t.appName}</span>}
        </div>
        <Link
          to={back}
          onClick={onNavigate}
          title={collapsed ? backText : undefined}
          aria-label={backText}
          className={`${RAIL_ITEM} ${collapsed ? RAIL_ICON_ONLY : 'gap-2 px-3'} text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground`}
        >
          <ArrowLeft className="size-4 shrink-0" aria-hidden="true" />
          {!collapsed && <span className="truncate">{backText}</span>}
        </Link>
        {Title && <Title className={collapsed ? 'sr-only' : 'px-3 pt-4 pb-1 text-lg font-semibold'}>{tc.title}</Title>}
      </div>
      {scopeControl}
      <nav aria-label={tc.nav} className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        <ul className="grid gap-0.5">
          {sections.map(({ key, label, Icon }) => {
            const on = key === active
            return (
              <li key={key}>
                <Link
                  to={path(key)}
                  onClick={onNavigate}
                  aria-current={on ? 'page' : undefined}
                  title={collapsed ? label : undefined}
                  aria-label={collapsed ? label : undefined}
                  className={`${RAIL_ITEM} ${collapsed ? RAIL_ICON_ONLY : 'gap-3 px-3'} ${on ? 'bg-brand/10 font-medium text-brand' : 'text-sidebar-foreground/80 hover:bg-sidebar-accent/60 hover:text-foreground'}`}
                >
                  <Icon aria-hidden="true" className={`size-4 shrink-0 ${on ? 'text-brand' : ''}`} strokeWidth={1.75} />
                  {!collapsed && <span className="truncate">{label}</span>}
                </Link>
              </li>
            )
          })}
        </ul>
      </nav>
      {footer && <div className="grid shrink-0 gap-0.5 border-t p-2">{footer}</div>}
    </>
  )
}
