import { createContext, useContext, useRef, useState, type MouseEvent, type ReactNode, type TouchEvent } from 'react'
import { FilePlus2, FolderInput, FolderPlus, LayoutTemplate, ListChecks, MoreHorizontal, PenLine, Shapes, SquareArrowOutUpRight, Star, StarOff, Trash2 } from 'lucide-react'
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuSeparator, ContextMenuTrigger } from '@/components/ui/context-menu'
import { useCanFavorite, useSpacePermissions } from '@/api/hooks'
import { useToggleFavorite } from '@/components/favorites/StarButton'
import type { FileItem } from '@/lib/folders'
import { t } from '@/i18n'
import { useFileOps } from './FileOps'

// One menu serves a whole listing (or the whole tree): entries only carry
// `data-file-key`, and the right-click, a long press or the "…" button of an
// entry tell which one it was for. Nothing is built per entry, and the menu
// itself exists only while it is open.
const Open = createContext<((anchor: HTMLElement) => void) | null>(null)

export const ENTRY_ATTR = 'data-file-key'

export function FileContextMenu({
  children,
  className,
  resolve,
  emptyParent,
  onSelect,
  afterAction,
}: {
  children: ReactNode
  className?: string
  // The items a menu on this entry acts on, or null when the key is unknown.
  resolve: (key: string) => FileItem[] | null
  // The folder "New page" and "New folder" work in when the menu is opened on
  // empty space; without it empty space has no menu of ours.
  emptyParent?: string
  // Starts a selection with this entry (for touch, which has no checkbox on hover).
  onSelect?: (key: string) => void
  // Called after Move or Delete, which are the ones that make a selection obsolete.
  afterAction?: () => void
}) {
  const ops = useFileOps()
  const { write, manage } = useSpacePermissions()
  const canFavorite = useCanFavorite()
  const trigger = useRef<HTMLDivElement>(null)
  const [target, setTarget] = useState<{ key: string; items: FileItem[] } | null>(null)

  const capture = (e: MouseEvent) => {
    const el = (e.target as Element).closest?.(`[${ENTRY_ATTR}]`)
    const key = el?.getAttribute(ENTRY_ATTR) ?? null
    const items = key ? resolve(key) : null
    if (items && key) setTarget({ key, items })
    else if (emptyParent !== undefined && !key) setTarget(null)
    else e.stopPropagation()
  }

  // A long press on a touch screen opens the menu without a contextmenu event,
  // so what is under the finger is noted when the touch begins: the menu must
  // never act on whatever was chosen the time before.
  const note = (e: TouchEvent) => {
    const el = (e.target as Element).closest?.(`[${ENTRY_ATTR}]`)
    const key = el?.getAttribute(ENTRY_ATTR) ?? null
    const items = key ? resolve(key) : null
    setTarget(items && key ? { key, items } : null)
  }

  // The "…" button asks the menu to open as if the entry had been right-clicked, just below the button.
  const openAt = (anchor: HTMLElement) => {
    const r = anchor.getBoundingClientRect()
    anchor.dispatchEvent(new window.MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.left, clientY: r.bottom, button: 2 }))
  }

  // Nearly everything in the menu changes pages: without `write` there is nothing, except the person's own favourites.
  if (!ops || (!write && !canFavorite)) return <div className={className}>{children}</div>
  const items = target?.items ?? null
  const one = items?.length === 1 ? items[0] : null

  return (
    <Open.Provider value={openAt}>
      <ContextMenu>
        <ContextMenuTrigger ref={trigger} className={className} onContextMenuCapture={capture} onTouchStartCapture={note}>
          {children}
        </ContextMenuTrigger>
        <ContextMenuContent>
          {items ? (
            <>
              {one && (
                <ContextMenuItem onClick={() => ops.open(one)}>
                  <SquareArrowOutUpRight />
                  {t.manage.open}
                </ContextMenuItem>
              )}
              {one && canFavorite && <FavoriteItem item={one} />}
              {write && one?.kind === 'folder' && (
                <ContextMenuItem onClick={() => ops.rename(one.path)}>
                  <PenLine />
                  {t.manage.rename}
                </ContextMenuItem>
              )}
              {one?.kind === 'folder' && manage && (
                <ContextMenuItem onClick={() => ops.setType(one.path)}>
                  <Shapes />
                  {t.manage.setType}
                </ContextMenuItem>
              )}
              {one?.kind === 'folder' && manage && (
                <ContextMenuItem onClick={() => ops.setTemplate(one.path)}>
                  <LayoutTemplate />
                  {t.manage.setTemplate}
                </ContextMenuItem>
              )}
              {write && one && target && onSelect && (
                <ContextMenuItem onClick={() => onSelect(target.key)}>
                  <ListChecks />
                  {t.manage.select}
                </ContextMenuItem>
              )}
              {write && (
                <>
                  <ContextMenuItem onClick={() => ops.chooseMove(items, afterAction)}>
                    <FolderInput />
                    {t.manage.move}
                  </ContextMenuItem>
                  <ContextMenuSeparator />
                  <ContextMenuItem variant="destructive" onClick={() => ops.confirmDelete(items, afterAction)}>
                    <Trash2 />
                    {t.manage.delete}
                  </ContextMenuItem>
                </>
              )}
            </>
          ) : (
            write &&
            emptyParent !== undefined && (
              <>
                <ContextMenuItem onClick={() => void ops.newPage(emptyParent)}>
                  <FilePlus2 />
                  {t.manage.newPage}
                </ContextMenuItem>
                <ContextMenuItem onClick={() => ops.newFolder(emptyParent)}>
                  <FolderPlus />
                  {t.manage.newFolder}
                </ContextMenuItem>
              </>
            )
          )}
        </ContextMenuContent>
      </ContextMenu>
    </Open.Provider>
  )
}

// "Favourite" or, for one that is, "Remove from favourites": for a single page or folder.
function FavoriteItem({ item }: { item: FileItem }) {
  const { on, toggle } = useToggleFavorite(item.kind === 'page' ? { kind: 'page', target: item.id } : { kind: 'folder', target: item.path })
  return (
    <ContextMenuItem onClick={toggle}>
      {on ? <StarOff /> : <Star />}
      {on ? t.fav.remove : t.fav.addFolder}
    </ContextMenuItem>
  )
}

// The button at the end of an entry that opens the same menu, for touch.
export function MoreButton({ label, className = '' }: { label: string; className?: string }) {
  const open = useContext(Open)
  // The menu is not there without `write` (see above), so neither is its button.
  if (!open) return null
  return (
    <button
      type="button"
      aria-label={`${t.manage.more}: ${label}`}
      title={t.manage.more}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        open(e.currentTarget)
      }}
      className={`flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity outline-none group-focus-within/entry:opacity-100 group-hover/entry:opacity-100 pointer-coarse:opacity-100 hover:bg-muted hover:text-foreground focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50 ${className}`}
    >
      <MoreHorizontal className="size-4" />
    </button>
  )
}
