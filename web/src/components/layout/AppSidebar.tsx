import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { ChevronRight, Clock, Folder, FolderOpen, FolderPlus, Home, Inbox, PenLine, Search, Tag } from 'lucide-react'
import { useActiveSpace, useSession, useTags, useTree, useUrls } from '@/api/hooks'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { Skeleton } from '@/components/ui/skeleton'
import { buildFolderTree, ancestorsOf, pathAfter, type FolderNode } from '@/lib/folders'
import { t } from '@/i18n/zh-CN'
import { NewFolderDialog } from './NewFolderDialog'
import { useQuickCapture } from './QuickCapture'
import { SidebarToggle } from './SidebarToggle'
import { SpaceSwitcher } from './SpaceSwitcher'
import { ThemeToggle } from './ThemeToggle'
import { UserMenu } from './UserMenu'

const OPEN_KEY = 'pb.folders.open'
const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)

// Which folders the person collapsed or expanded by hand. Remembered per
// browser; storage can be blocked, so every access is guarded.
function loadOpen(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(OPEN_KEY)
    return raw ? (JSON.parse(raw) as Record<string, boolean>) : {}
  } catch {
    return {}
  }
}

function SearchBox() {
  const { pathname, search } = useLocation()
  const navigate = useNavigate()
  const { isMobile, open, setOpen, setOpenMobile } = useSidebar()
  const input = useRef<HTMLInputElement>(null)
  const urls = useUrls()
  const searchPath = `${urls.base}/search`
  const urlQ = pathname === searchPath ? (new URLSearchParams(search).get('q') ?? '') : ''
  // While typing the box owns its text; otherwise it mirrors the URL.
  const [draft, setDraft] = useState<string | null>(null)
  const value = draft ?? urlQ

  const go = (q: string) => {
    setDraft(null)
    const trimmed = q.trim()
    if (trimmed) navigate(urls.search(trimmed))
    else if (pathname === searchPath) navigate(urls.recent())
  }

  useEffect(() => {
    if (draft === null) return
    const timer = setTimeout(() => go(draft), 350)
    return () => clearTimeout(timer)
    // go() only reads navigation state captured when the draft changed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft])

  // ⌘K / Ctrl+K from anywhere, including inside the editor.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== 'k') return
      e.preventDefault()
      if (isMobile) setOpenMobile(true)
      else if (!open) setOpen(true)
      // The drawer / collapsed sidebar needs a moment to mount its content.
      setTimeout(() => {
        input.current?.focus()
        input.current?.select()
      }, 0)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isMobile, open, setOpen, setOpenMobile])

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    // Enter that confirms an IME candidate must not submit the search.
    if (e.key === 'Enter' && !e.nativeEvent.isComposing) go(value)
    if (e.key === 'Escape') input.current?.blur()
  }

  return (
    <div className="relative">
      <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
      <input
        ref={input}
        type="search"
        value={value}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={t.nav.searchPlaceholder}
        aria-label={t.nav.search}
        className="h-8 w-full rounded-lg border border-input bg-background pr-10 pl-8 text-sm outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-search-cancel-button]:hidden"
      />
      <kbd className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 rounded border bg-muted px-1 font-sans text-[10px] text-muted-foreground">⌘K</kbd>
    </div>
  )
}

// The button above the search box. On a phone it sits in the drawer, which
// closes first so the box does not open on top of it.
function QuickNoteButton() {
  const { open, available } = useQuickCapture()
  const { isMobile, setOpenMobile } = useSidebar()
  return (
    <button
      type="button"
      disabled={!available}
      onClick={() => {
        if (isMobile) setOpenMobile(false)
        open()
      }}
      className="flex h-8 w-full items-center gap-2 rounded-lg border border-input bg-background px-2.5 text-sm outline-none transition-colors hover:bg-sidebar-accent focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50"
    >
      <PenLine className="size-4 text-muted-foreground" />
      <span className="flex-1 text-left">{t.nav.quickNote}</span>
      <kbd className="pointer-events-none hidden rounded border bg-muted px-1 font-sans text-[10px] text-muted-foreground md:inline">{isMac ? '⌥⇧N' : 'Alt+Shift+N'}</kbd>
    </button>
  )
}

// The inbox is a folder, so this is a link to its listing. It works before the
// folder exists: the listing is then empty and says how to fill it.
function InboxLink({ inbox, activeFolder }: { inbox: string | undefined; activeFolder: string | null }) {
  const urls = useUrls()
  const { data } = useTree()
  const count = inbox === undefined ? 0 : (data?.folders.find((f) => f.path === inbox)?.pages ?? 0)
  if (inbox === undefined) return null
  return (
    <SidebarMenuItem>
      <SidebarMenuButton isActive={activeFolder === inbox} render={<NavLink to={urls.folder(inbox)} />}>
        <Inbox />
        <span>{t.nav.inbox}</span>
      </SidebarMenuButton>
      {count > 0 && <SidebarMenuBadge>{count}</SidebarMenuBadge>}
    </SidebarMenuItem>
  )
}

function FolderRow({ node, depth, activeFolder, isOpen, toggle }: { node: FolderNode; depth: number; activeFolder: string | null; isOpen: (p: string) => boolean; toggle: (p: string) => void }) {
  const urls = useUrls()
  const open = node.children.length > 0 && isOpen(node.path)
  const Icon = open ? FolderOpen : Folder
  return (
    <>
      <SidebarMenuItem>
        {node.children.length > 0 && (
          <button
            type="button"
            aria-label={open ? t.nav.collapse : t.nav.expand}
            aria-expanded={open}
            onClick={() => toggle(node.path)}
            className="absolute top-1.5 z-10 flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
            style={{ left: 2 + depth * 12 }}
          >
            <ChevronRight className={`size-3.5 transition-transform ${open ? 'rotate-90' : ''}`} />
          </button>
        )}
        <SidebarMenuButton isActive={activeFolder === node.path} render={<NavLink to={urls.folder(node.path)} />} style={{ paddingLeft: 24 + depth * 12 }}>
          <Icon />
          <span className="truncate">{node.name}</span>
        </SidebarMenuButton>
        <SidebarMenuBadge>{node.pages}</SidebarMenuBadge>
      </SidebarMenuItem>
      {open && node.children.map((c) => <FolderRow key={c.path} node={c} depth={depth + 1} activeFolder={activeFolder} isOpen={isOpen} toggle={toggle} />)}
    </>
  )
}

function FolderTree({ activeFolder }: { activeFolder: string | null }) {
  const { data, isPending, isError } = useTree()
  const urls = useUrls()
  const [explicit, setExplicit] = useState<Record<string, boolean>>(loadOpen)
  const tree = useMemo(() => buildFolderTree(data?.folders ?? []), [data])
  // The folder being viewed is opened to show its subfolders, unless the person closed it.
  const revealed = useMemo(() => new Set(activeFolder ? ancestorsOf(activeFolder) : []), [activeFolder])
  const isOpen = (path: string) => explicit[path] ?? revealed.has(path)
  const toggle = (path: string) =>
    setExplicit((prev) => {
      const next = { ...prev, [path]: !(prev[path] ?? revealed.has(path)) }
      try {
        localStorage.setItem(OPEN_KEY, JSON.stringify(next))
      } catch {
        // Storage blocked: the choice just lasts for this session.
      }
      return next
    })

  if (isPending) return <div className="space-y-1.5 px-2">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-6 w-full" />)}</div>
  if (isError) return null
  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SidebarMenuButton isActive={activeFolder === ''} render={<NavLink to={urls.folder('')} />}>
          <Home />
          <span className="truncate">{t.nav.rootFolder}</span>
        </SidebarMenuButton>
        <SidebarMenuBadge>{tree.rootPages}</SidebarMenuBadge>
      </SidebarMenuItem>
      {tree.nodes.map((n) => (
        <FolderRow key={n.path} node={n} depth={0} activeFolder={activeFolder} isOpen={isOpen} toggle={toggle} />
      ))}
    </SidebarMenu>
  )
}

const TAGS_SHOWN = 24

function TagCloud({ activeTag }: { activeTag: string | null }) {
  const { data } = useTags()
  const urls = useUrls()
  const [all, setAll] = useState(false)
  const tags = data?.tags ?? []
  if (tags.length === 0) return null
  const shown = all ? tags : tags.slice(0, TAGS_SHOWN)
  return (
    <div className="flex flex-wrap gap-1.5 px-2">
      {shown.map((tag) => (
        <NavLink
          key={tag.tag}
          to={urls.tag(tag.tag)}
          className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs transition-colors hover:bg-sidebar-accent ${activeTag === tag.tag ? 'border-brand/50 bg-sidebar-accent text-sidebar-accent-foreground' : 'text-sidebar-foreground'}`}
        >
          <span className="max-w-32 truncate">{tag.tag}</span>
          <span className="text-muted-foreground tabular-nums">{tag.pages}</span>
        </NavLink>
      ))}
      {tags.length > TAGS_SHOWN && (
        <button type="button" onClick={() => setAll((v) => !v)} className="px-1 text-xs text-muted-foreground hover:text-foreground">
          {all ? t.nav.fewerTags : `${t.nav.allTags} (${tags.length})`}
        </button>
      )}
    </div>
  )
}

export function AppSidebar() {
  const { pathname } = useLocation()
  const urls = useUrls()
  const session = useSession().data
  const { id, spaces } = useActiveSpace()
  const [newFolder, setNewFolder] = useState(false)
  const accounts = session?.mode === 'accounts'
  const activeFolder = pathAfter(pathname, `${urls.base}/f`)
  const activeTag = pathAfter(pathname, `${urls.base}/t`)
  const current = spaces.find((s) => s.id === id)
  const spaceName = current?.name

  return (
    <Sidebar>
      <SidebarHeader className="gap-3 p-3 pb-1">
        <div className="flex items-center gap-1">
          <div className="min-w-0 flex-1">
            {accounts ? (
              <SpaceSwitcher />
            ) : (
              <NavLink to={urls.recent()} className="flex items-center gap-2 px-1 pt-0.5">
                <img src="/favicon.svg" alt="" className="size-6 rounded-md" />
                <span className="truncate text-sm font-semibold">{spaceName ?? t.appName}</span>
              </NavLink>
            )}
          </div>
          <SidebarToggle where="sidebar" />
        </div>
        <QuickNoteButton />
        <SearchBox />
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <InboxLink inbox={current?.inbox} activeFolder={activeFolder} />
              <SidebarMenuItem>
                <SidebarMenuButton isActive={pathname === urls.recent()} render={<NavLink to={urls.recent()} />}>
                  <Clock />
                  <span>{t.nav.recent}</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel>{t.nav.folders}</SidebarGroupLabel>
          <SidebarGroupAction title={t.nav.newFolder} aria-label={t.nav.newFolder} onClick={() => setNewFolder(true)}>
            <FolderPlus />
          </SidebarGroupAction>
          <SidebarGroupContent>
            <FolderTree activeFolder={activeFolder} />
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarGroup>
          <SidebarGroupLabel>
            <Tag className="mr-1.5 size-3.5" />
            {t.nav.tags}
          </SidebarGroupLabel>
          <SidebarGroupContent>
            <TagCloud activeTag={activeTag} />
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter className="flex-row items-center justify-between gap-1 border-t p-2">
        {accounts ? <UserMenu /> : null}
        <div className="ml-auto">
          <ThemeToggle />
        </div>
      </SidebarFooter>
      <NewFolderDialog open={newFolder} onOpenChange={setNewFolder} parent={activeFolder ?? ''} />
    </Sidebar>
  )
}
