import { useEffect, useMemo, useRef, useState } from 'react'
import { Logo } from '@/components/brand/Logo'
import { NavLink, useLocation } from 'react-router'
import {
  ChevronRight,
  Clock,
  Folder,
  FolderOpen,
  FolderPlus,
  Home,
  House,
  Inbox,
  LogOut,
  MoreHorizontal,
  PenLine,
  Search,
  Settings,
  Tag,
  Upload,
} from 'lucide-react'
import { useActiveSpace, useSession, useSpacePermissions, useTagColors, useTags, useTree, useUrls } from '@/api/hooks'
import type { TagColor } from '@/api/types'
import { PageIcon } from '@/components/icons/PageIcon'
import { useFolderStyle } from '@/lib/icons/useStyles'
import { TAG_ATTR, TagColorMenu, useOpenTagColorMenu } from '@/components/TagColorMenu'
import { isTagColor } from '@/lib/tagColors'
import { tagPillClass, tagPillStyle } from '@/components/TagChip'
import { useSearchDialog } from '@/components/search/SearchDialog'
import { useOpenSettings } from '@/components/settings/useSettings'
import { Button } from '@/components/ui/button'
import { ENTRY_ATTR, FileContextMenu } from '@/components/files/FileContextMenu'
import { useFileOps } from '@/components/files/FileOps'
import { useDropTarget } from '@/components/files/useDropTarget'
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
import { buildFolderTree, ancestorsOf, isSameOrUnder, pathAfter, type FileItem, type FolderNode } from '@/lib/folders'
import { t } from '@/i18n'
import { SidebarFavorites } from '@/components/favorites/SidebarFavorites'
import { HelpMenu } from '@/components/onboarding/HelpMenu'
import { useQuickCapture } from './QuickCapture'
import { SidebarToggle } from './SidebarToggle'
import { SpaceSwitcher } from './SpaceSwitcher'
import { ThemeToggle } from './ThemeToggle'
import { UserMenu } from './UserMenu'
import { viewAsId } from '@/lib/viewAs'
import { exitViewAs } from '@/components/viewas/Banner'

const OPEN_KEY = 'pb.folders.open'
// A folder that would accept what is being dragged.
const DROP_CLASS = 'bg-primary/15 text-primary ring-2 ring-primary/40'
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

// Looks like a search box and opens the search dialog (⌘K does too); typing
// happens there, with the results and the filters.
function SearchBox() {
  const { open } = useSearchDialog()
  const { isMobile, setOpenMobile } = useSidebar()
  return (
    <button
      type="button"
      aria-label={t.nav.search}
      aria-haspopup="dialog"
      onClick={() => {
        // On a phone the sidebar is a drawer; it closes first so the dialog does not open on top of it.
        if (isMobile) setOpenMobile(false)
        open()
      }}
      className="flex h-8 w-full items-center gap-2 rounded-lg border border-input bg-background px-2.5 text-sm text-muted-foreground outline-none transition-colors hover:bg-sidebar-accent focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <Search className="size-4" />
      <span className="flex-1 text-left">{t.nav.searchPlaceholder}</span>
      <kbd className="pointer-events-none hidden rounded border bg-muted px-1 font-sans text-[10px] text-muted-foreground md:inline">
        {isMac ? '⌘K' : 'Ctrl K'}
      </kbd>
    </button>
  )
}

// The button above the search box. On a phone it sits in the drawer, which
// closes first so the box does not open on top of it.
function QuickNoteButton() {
  const { open, available } = useQuickCapture()
  const { write } = useSpacePermissions()
  const { isMobile, setOpenMobile } = useSidebar()
  // A note is a page: it needs `write`.
  if (!write) return null
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
      <kbd className="pointer-events-none hidden rounded border bg-muted px-1 font-sans text-[10px] text-muted-foreground md:inline">
        {isMac ? '⌥⇧N' : 'Alt+Shift+N'}
      </kbd>
    </button>
  )
}

// The inbox is a folder, so this is a link to its listing. It works before the
// folder exists: the listing is then empty and says how to fill it.
function InboxLink({ inbox, activeFolder }: { inbox: string | undefined; activeFolder: string | null }) {
  const urls = useUrls()
  const ops = useFileOps()
  const { data } = useTree()
  const count = inbox === undefined ? 0 : (data?.folders.find((f) => f.path === inbox)?.pages ?? 0)
  const drop = useDropTarget(inbox ?? '', ops?.index, (items, target) => void ops?.moveTo(items, target))
  if (inbox === undefined) return null
  return (
    <SidebarMenuItem {...drop.props}>
      <SidebarMenuButton
        isActive={activeFolder === inbox}
        render={<NavLink to={urls.folder(inbox)} />}
        className={drop.over ? DROP_CLASS : undefined}
      >
        <Inbox />
        <span>{t.nav.inbox}</span>
      </SidebarMenuButton>
      {count > 0 && <SidebarMenuBadge>{count}</SidebarMenuBadge>}
    </SidebarMenuItem>
  )
}

function FolderRow({
  node,
  depth,
  activeFolder,
  isOpen,
  toggle,
  expand,
}: {
  node: FolderNode
  depth: number
  activeFolder: string | null
  isOpen: (p: string) => boolean
  toggle: (p: string) => void
  expand: (p: string) => void
}) {
  const urls = useUrls()
  const ops = useFileOps()
  const hasChildren = node.children.length > 0
  const open = hasChildren && isOpen(node.path)
  const Icon = open ? FolderOpen : Folder
  const style = useFolderStyle()(node.path)
  // A dragged item resting on a closed folder opens it, so it can be dropped deeper.
  const drop = useDropTarget(
    node.path,
    ops?.index,
    (items, target) => void ops?.moveTo(items, target),
    hasChildren && !open ? () => expand(node.path) : undefined,
  )
  const row = useRef<HTMLLIElement>(null)
  const active = activeFolder === node.path
  // The tree scrolls by itself, so the folder being viewed is brought into view.
  useEffect(() => {
    if (active) row.current?.scrollIntoView?.({ block: 'nearest' })
  }, [active])
  return (
    <>
      <SidebarMenuItem ref={row} {...{ [ENTRY_ATTR]: `f:${node.path}` }} {...drop.props}>
        {hasChildren && (
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
        <SidebarMenuButton
          isActive={activeFolder === node.path}
          render={
            <NavLink
              to={urls.folder(node.path)}
              // Choosing the folder that is already open opens or closes it instead of going nowhere.
              onClick={(e) => {
                if (activeFolder === node.path && hasChildren) {
                  e.preventDefault()
                  toggle(node.path)
                }
              }}
            />
          }
          className={drop.over ? DROP_CLASS : undefined}
          style={{ paddingLeft: 24 + depth * 12 }}
        >
          <PageIcon icon={style.icon} color={style.color} fallback={<Icon />} className="size-4" />
          <span className="truncate">{node.name}</span>
        </SidebarMenuButton>
        <SidebarMenuBadge>{node.total}</SidebarMenuBadge>
      </SidebarMenuItem>
      {open &&
        node.children.map((c) => (
          <FolderRow
            key={c.path}
            node={c}
            depth={depth + 1}
            activeFolder={activeFolder}
            isOpen={isOpen}
            toggle={toggle}
            expand={expand}
          />
        ))}
    </>
  )
}

// The tree starts below the space itself: its top-level folders sit directly
// under the "Folders" heading, which is the link to the space's own listing.
// The inbox has its own entry above, so it is left out here.
function FolderTree({ activeFolder, inbox }: { activeFolder: string | null; inbox: string | undefined }) {
  const { data, isPending, isError } = useTree()
  const [explicit, setExplicit] = useState<Record<string, boolean>>(loadOpen)
  const tree = useMemo(
    () => buildFolderTree((data?.folders ?? []).filter((f) => inbox === undefined || !isSameOrUnder(f.path, inbox))),
    [data, inbox],
  )
  // The folder being viewed is opened to show its subfolders, unless the person closed it.
  const revealed = useMemo(() => new Set(activeFolder ? ancestorsOf(activeFolder) : []), [activeFolder])
  const isOpen = (path: string) => explicit[path] ?? revealed.has(path)
  const remember = (update: (prev: Record<string, boolean>) => Record<string, boolean>) =>
    setExplicit((prev) => {
      const next = update(prev)
      try {
        localStorage.setItem(OPEN_KEY, JSON.stringify(next))
      } catch {
        // Storage blocked: the choice just lasts for this session.
      }
      return next
    })
  const toggle = (path: string) => remember((prev) => ({ ...prev, [path]: !(prev[path] ?? revealed.has(path)) }))
  const expand = (path: string) => remember((prev) => (prev[path] === true ? prev : { ...prev, [path]: true }))

  if (isPending)
    return (
      <div className="space-y-1.5 px-2">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-6 w-full" />
        ))}
      </div>
    )
  if (isError) return null
  return (
    <FileContextMenu
      resolve={(key): FileItem[] | null => (key.startsWith('f:') ? [{ kind: 'folder', path: key.slice(2) }] : null)}
    >
      <SidebarMenu>
        {/* The top of the tree has a row of its own: it is where the pages that are in no folder are found. */}
        <RootRow activeFolder={activeFolder} pages={(data?.folders ?? []).find((f) => f.path === '')?.pages ?? 0} />
        {tree.nodes.map((n) => (
          <FolderRow
            key={n.path}
            node={n}
            depth={1}
            activeFolder={activeFolder}
            isOpen={isOpen}
            toggle={toggle}
            expand={expand}
          />
        ))}
      </SidebarMenu>
    </FileContextMenu>
  )
}

// The root of the tree: the space's top level, with the folders one step in below it.
function RootRow({ activeFolder, pages }: { activeFolder: string | null; pages: number }) {
  const urls = useUrls()
  const ops = useFileOps()
  const drop = useDropTarget('', ops?.index, (items, target) => void ops?.moveTo(items, target))
  return (
    <SidebarMenuItem {...drop.props}>
      <SidebarMenuButton
        isActive={activeFolder === ''}
        render={<NavLink to={urls.folder('')} />}
        className={drop.over ? DROP_CLASS : undefined}
      >
        <Home />
        <span className="truncate">{t.nav.rootFolder}</span>
      </SidebarMenuButton>
      <SidebarMenuBadge>{pages}</SidebarMenuBadge>
    </SidebarMenuItem>
  )
}

// The heading is the way to the space's top level, and a place to drop things onto it.
function FoldersHeading({ activeFolder }: { activeFolder: string | null }) {
  const urls = useUrls()
  const ops = useFileOps()
  const drop = useDropTarget('', ops?.index, (items, target) => void ops?.moveTo(items, target))
  return (
    <SidebarGroupLabel
      render={<NavLink to={urls.folder('')} aria-current={activeFolder === '' ? 'page' : undefined} />}
      className={`mr-16 cursor-pointer hover:bg-sidebar-accent/70 hover:text-sidebar-foreground ${activeFolder === '' ? 'font-semibold text-sidebar-primary' : ''} ${drop.over ? DROP_CLASS : ''}`}
      {...drop.props}
    >
      {t.nav.folders}
    </SidebarGroupLabel>
  )
}

// With more tags than this a filter box appears above them.
const TAG_FILTER_FROM = 20

// Tags are pills in their own colour, in a list that scrolls by itself, so
// hundreds of them do not make the sidebar endless. A right-click, a long press
// or the "…" on a tag opens a menu to set its colour.
function TagCloud({ activeTag }: { activeTag: string | null }) {
  const { data } = useTags()
  const colors = useTagColors()
  const urls = useUrls()
  const [filter, setFilter] = useState('')
  const tags = useMemo(() => data?.tags ?? [], [data])
  const needle = filter.trim().toLowerCase()
  const shown = useMemo(
    () => (needle ? tags.filter((x) => x.tag.toLowerCase().includes(needle)) : tags),
    [tags, needle],
  )
  if (tags.length === 0) return null
  return (
    <div className="grid gap-1.5">
      {tags.length > TAG_FILTER_FROM && (
        <div className="relative px-2">
          <input
            type="search"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t.nav.filterTags}
            aria-label={t.nav.filterTags}
            className="h-7 w-full rounded-md border border-input bg-background px-2 text-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 [&::-webkit-search-cancel-button]:hidden"
          />
        </div>
      )}
      <TagColorMenu colorOf={(tag) => colors.get(tag)} className="max-h-48 overflow-y-auto overscroll-contain px-2">
        <div className="flex flex-wrap gap-1.5" role="list" aria-label={t.nav.tags}>
          {shown.map((tag) => (
            <TagLink
              key={tag.tag}
              tag={tag.tag}
              pages={tag.pages}
              color={tag.color}
              to={urls.tag(tag.tag)}
              active={activeTag === tag.tag}
            />
          ))}
          {shown.length === 0 && <p className="px-1 text-xs text-muted-foreground">{t.nav.noTagMatch}</p>}
        </div>
      </TagColorMenu>
    </div>
  )
}

function TagLink({
  tag,
  pages,
  color: given,
  to,
  active,
}: {
  tag: string
  pages: number
  color: TagColor | undefined
  to: string
  active: boolean
}) {
  const openMenu = useOpenTagColorMenu()
  // A colour the client does not know is drawn as none.
  const color = isTagColor(given) ? given : undefined
  return (
    <span role="listitem" {...{ [TAG_ATTR]: tag }} className="group/tag relative inline-flex max-w-full">
      <NavLink
        to={to}
        style={tagPillStyle(color)}
        className={tagPillClass(
          color,
          `pr-2 transition-colors hover:brightness-95 dark:hover:brightness-125 ${color ? '' : 'hover:bg-sidebar-accent'} ${active ? (color ? 'ring-2 ring-brand/60' : 'border-brand/50 bg-sidebar-accent text-sidebar-accent-foreground') : ''}`,
        )}
      >
        <span className="max-w-32 truncate">{tag}</span>
        <span className="tabular-nums opacity-70">{pages}</span>
      </NavLink>
      {openMenu && (
        <button
          type="button"
          aria-label={t.tagColor.more(tag)}
          title={t.tagColor.menu(tag)}
          onClick={(e) => openMenu(e.currentTarget)}
          className="absolute top-1/2 right-0.5 hidden size-4 -translate-y-1/2 items-center justify-center rounded-full bg-popover text-foreground shadow-sm ring-1 ring-foreground/15 outline-none group-focus-within/tag:flex group-hover/tag:flex focus-visible:ring-2 focus-visible:ring-ring"
        >
          <MoreHorizontal className="size-3" />
        </button>
      )}
    </span>
  )
}

// In a view as a person the account menu is the way out of the view.
function ExitViewButton({ user }: { user: string }) {
  return (
    <Button
      variant="ghost"
      className="h-9 min-w-0 flex-1 justify-start gap-2 px-1.5"
      onClick={() => void exitViewAs(user)}
    >
      <LogOut className="size-4" aria-hidden="true" />
      <span className="truncate">{t.viewAs.exitView}</span>
    </Button>
  )
}

export function AppSidebar() {
  const { pathname } = useLocation()
  const urls = useUrls()
  const session = useSession().data
  const { id, spaces } = useActiveSpace()
  const ops = useFileOps()
  const { write } = useSpacePermissions()
  const openSettings = useOpenSettings()
  const accounts = session?.mode === 'accounts'
  const activeFolder = pathAfter(pathname, `${urls.base}/f`)
  const activeTag = pathAfter(pathname, `${urls.base}/t`)
  const current = spaces.find((s) => s.id === id)
  const spaceName = current?.name

  return (
    // Below the banner of a view as a person, in its tab (0 otherwise).
    <Sidebar className="top-(--viewas-h) h-[calc(100svh-var(--viewas-h))]">
      <SidebarHeader className="gap-3 p-3 pb-1">
        <div className="flex items-center gap-1">
          <div className="min-w-0 flex-1">
            {accounts ? (
              <SpaceSwitcher />
            ) : (
              <NavLink to={urls.home()} className="flex items-center gap-2 px-1 pt-0.5">
                <Logo />
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
              <SidebarMenuItem>
                <SidebarMenuButton
                  isActive={pathname === urls.home() || pathname === urls.base}
                  render={<NavLink to={urls.home()} end />}
                >
                  <House />
                  <span>{t.nav.home}</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
              <InboxLink inbox={current?.inbox || undefined} activeFolder={activeFolder} />
              <SidebarMenuItem>
                <SidebarMenuButton isActive={pathname === urls.recent()} render={<NavLink to={urls.recent()} />}>
                  <Clock />
                  <span>{t.nav.recent}</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        <SidebarFavorites />
        <SidebarGroup>
          <FoldersHeading activeFolder={activeFolder} />
          {write && (
            <>
              <SidebarGroupAction
                title={t.nav.newFolder}
                aria-label={t.nav.newFolder}
                onClick={() => ops?.newFolder(activeFolder ?? '')}
              >
                <FolderPlus />
              </SidebarGroupAction>
              {/* Next to "new folder"; brings files into the folder being viewed. */}
              <SidebarGroupAction
                title={t.nav.import}
                aria-label={t.nav.import}
                className="right-9"
                render={<NavLink to={urls.import(activeFolder ?? '')} />}
              >
                <Upload />
              </SidebarGroupAction>
            </>
          )}
          {/* Scrolls by itself, so a hundred folders do not push the tags out of reach. */}
          <SidebarGroupContent className="max-h-[min(24rem,45vh)] overflow-y-auto overscroll-contain">
            <FolderTree activeFolder={activeFolder} inbox={current?.inbox} />
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
        {viewAsId() !== null && session?.user ? (
          <ExitViewButton user={session.user.id} />
        ) : accounts ? (
          <UserMenu />
        ) : null}
        <div className="ml-auto flex items-center">
          {/* With accounts, Settings is in the account menu; a single-user server has no such menu. */}
          {!accounts && (
            <Button
              variant="ghost"
              size="icon"
              aria-label={t.nav.settings}
              title={t.nav.settings}
              onClick={() => openSettings(accounts ? 'profile' : 'preferences')}
            >
              <Settings className="size-4" />
            </Button>
          )}
          <HelpMenu />
          <ThemeToggle />
        </div>
      </SidebarFooter>
    </Sidebar>
  )
}
