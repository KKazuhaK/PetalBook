import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ArrowLeft, Menu, PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useActiveSpace } from '@/api/hooks'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import { spaceUrls, type AdminSection, adminPath } from '@/lib/urls'
import { tc } from '@/i18n/console'
import type { NavSection } from './adminNav'

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
  children,
}: {
  sections: NavSection[]
  active: AdminSection | null
  children: ReactNode
}) {
  const [collapsed, setCollapsed] = useState(startCollapsed)
  const [open, setOpen] = useState(false)
  const { id, spaces } = useActiveSpace()
  const space = spaces.find((s) => s.id === id)
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
    <div className="flex min-h-dvh w-full min-w-0 bg-background" data-slot="admin-layout">
      <aside
        aria-label={tc.nav}
        data-collapsed={collapsed || undefined}
        className={`sticky top-0 hidden h-dvh shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground transition-[width] duration-200 md:flex ${collapsed ? 'w-16' : 'w-[232px]'}`}
      >
        <Nav
          sections={sections}
          active={active}
          back={back}
          spaceName={space?.name}
          collapsed={collapsed}
          Title="h1"
          footer={
            <button
              type="button"
              onClick={toggle}
              aria-label={collapsed ? tc.expand : tc.collapse}
              aria-expanded={!collapsed}
              className="flex h-10 w-full items-center gap-2 rounded-md px-3 text-sm text-muted-foreground outline-none transition-colors hover:bg-sidebar-accent/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
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
          }
        />
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-2 border-b bg-background/85 px-3 backdrop-blur md:hidden">
          <button
            type="button"
            onClick={() => setOpen(true)}
            aria-label={tc.menu}
            aria-haspopup="dialog"
            className="inline-flex size-10 items-center justify-center rounded-md outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <Menu className="size-5" aria-hidden="true" />
          </button>
          <p className="min-w-0 flex-1 truncate text-sm font-medium">{current?.label ?? tc.title}</p>
        </header>
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
            onNavigate={() => setOpen(false)}
          />
        </SheetContent>
      </Sheet>
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
  onNavigate,
  Title,
}: {
  sections: NavSection[]
  active: AdminSection | null
  back: string
  spaceName?: string
  collapsed: boolean
  footer?: ReactNode
  onNavigate?: () => void
  // The heading of the page: the sidebar's own, the drawer's is a copy and is none.
  Title?: 'h1' | 'p'
}) {
  const backText = spaceName ? tc.back(spaceName) : tc.backFallback
  return (
    <>
      <div className="shrink-0 px-2 pt-3">
        <Link
          to={back}
          onClick={onNavigate}
          title={collapsed ? backText : undefined}
          aria-label={backText}
          className="flex h-10 items-center gap-2 rounded-md px-3 text-sm text-muted-foreground outline-none transition-colors hover:bg-sidebar-accent/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <ArrowLeft className="size-4 shrink-0" aria-hidden="true" />
          {!collapsed && <span className="truncate">{backText}</span>}
        </Link>
        {Title && <Title className={collapsed ? 'sr-only' : 'px-3 pt-4 pb-1 text-lg font-semibold'}>{tc.title}</Title>}
      </div>
      <nav aria-label={tc.nav} className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        <ul className="grid gap-0.5">
          {sections.map(({ key, label, Icon }) => {
            const on = key === active
            return (
              <li key={key}>
                <Link
                  to={adminPath(key)}
                  onClick={onNavigate}
                  aria-current={on ? 'page' : undefined}
                  title={collapsed ? label : undefined}
                  aria-label={collapsed ? label : undefined}
                  className={`flex h-10 items-center gap-3 rounded-md px-3 text-sm outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${on ? 'bg-brand/10 font-medium text-brand' : 'text-sidebar-foreground/80 hover:bg-sidebar-accent/60 hover:text-foreground'}`}
                >
                  <Icon aria-hidden="true" className={`size-4 shrink-0 ${on ? 'text-brand' : ''}`} strokeWidth={1.75} />
                  {!collapsed && <span className="truncate">{label}</span>}
                </Link>
              </li>
            )
          })}
        </ul>
      </nav>
      {footer && <div className="shrink-0 border-t p-2">{footer}</div>}
    </>
  )
}
