import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { Editor } from '@tiptap/core'
import { GripHorizontal, GripVertical, Plus, Table2 } from 'lucide-react'
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { t } from '@/i18n'
import { ColumnItems, RowItems, TableItems } from './TableMenus'
import { cellAtSelection, cellRefAt, insertColumn, insertRow, type CellRef } from './tableOps'

// Controls at the edges of a table, in the manner of Notion and Google Docs:
// a "+" along the bottom edge and the right edge, a handle on the column and
// the row under the pointer, and one for the whole table. They are drawn in a
// layer above the page, not in the document, so the document is untouched.
//
// The pointer decides which cell they belong to; when the pointer is not over a
// table they follow the cursor, which is also how they appear on a touch screen.

interface Box {
  left: number
  top: number
  width: number
  height: number
}
interface Layout {
  table: Box
  // The part of the table in view: a wide table scrolls inside its own box.
  visibleX: [number, number]
  column: Box
  row: Box
  cell: CellRef
  rows: number
  cols: number
}

const box = (r: DOMRect): Box => ({ left: r.left, top: r.top, width: r.width, height: r.height })

// The nearest ancestor that scrolls vertically: controls leaving its edge are hidden.
function scrollerOf(el: HTMLElement): HTMLElement | null {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY
    if (o === 'auto' || o === 'scroll') return p
  }
  return null
}

function measure(editor: Editor, cellEl: HTMLElement): Layout | null {
  const table = cellEl.closest('table')
  const row = cellEl.parentElement as HTMLTableRowElement | null
  if (!table || !row || !cellEl.isConnected) return null
  let cell: CellRef | null
  try {
    cell = cellRefAt(editor.state.doc.resolve(editor.view.posAtDOM(cellEl, 0)))
  } catch {
    // The cell has just been taken out of the document.
    return null
  }
  if (!cell) return null
  const head = table.rows[0]?.cells[cell.col]
  if (!head) return null
  const wrapper = table.closest('.tableWrapper') ?? table
  const w = wrapper.getBoundingClientRect()
  const clip = scrollerOf(table)?.getBoundingClientRect()
  const t = table.getBoundingClientRect()
  // Wholly out of sight, or under the top of its scroll area.
  if (clip && (t.bottom < clip.top + 8 || t.top > clip.bottom)) return null
  return { table: box(t), visibleX: [w.left, w.right], column: box(head.getBoundingClientRect()), row: box(row.getBoundingClientRect()), cell, rows: table.rows.length, cols: head.parentElement?.children.length ?? 1 }
}

const handle =
  'absolute flex items-center justify-center rounded-md bg-background text-muted-foreground shadow-sm ring-1 ring-border outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring aria-expanded:bg-muted aria-expanded:text-foreground'

export function TableControls({ editor }: { editor: Editor }) {
  const [layout, setLayout] = useState<Layout | null>(null)
  // The cell the pointer is over, if any; what the controls belong to.
  const hovered = useRef<HTMLElement | null>(null)
  const hideTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  // Open menus keep the controls where they are.
  const menus = useRef(0)
  const frame = useRef(0)

  const refresh = useCallback(() => {
    cancelAnimationFrame(frame.current)
    frame.current = requestAnimationFrame(() => {
      let el = hovered.current?.isConnected ? hovered.current : null
      if (!el && (editor.isFocused || menus.current > 0)) {
        // Follow the cursor when the pointer is elsewhere.
        const at = cellAtSelection(editor.state)
        const dom = at ? editor.view.nodeDOM(at.tablePos) : null
        const table = dom instanceof HTMLElement ? (dom instanceof HTMLTableElement ? dom : dom.querySelector('table')) : null
        el = (table?.rows[at!.row]?.cells[at!.col] as HTMLElement | undefined) ?? null
      }
      const next = el ? measure(editor, el) : null
      // A menu that went away with its controls cannot report closing.
      if (!next) menus.current = 0
      setLayout((prev) => (JSON.stringify(prev) === JSON.stringify(next) ? prev : next))
    })
  }, [editor])

  useEffect(() => {
    if (!editor.isEditable) return
    const root = editor.view.dom
    const cancelHide = () => clearTimeout(hideTimer.current)
    const scheduleHide = () => {
      cancelHide()
      hideTimer.current = setTimeout(() => {
        if (menus.current > 0) return
        hovered.current = null
        refresh()
      }, 250)
    }
    const onMove = (e: MouseEvent) => {
      if (menus.current > 0) return
      const cell = (e.target as HTMLElement).closest?.('td, th') as HTMLElement | null
      if (!cell || !root.contains(cell)) return scheduleHide()
      cancelHide()
      if (cell !== hovered.current) {
        hovered.current = cell
        refresh()
      }
    }
    root.addEventListener('mousemove', onMove)
    root.addEventListener('mouseleave', scheduleHide)
    editor.on('transaction', refresh)
    editor.on('focus', refresh)
    editor.on('blur', refresh)
    window.addEventListener('scroll', refresh, { capture: true, passive: true })
    window.addEventListener('resize', refresh)
    return () => {
      root.removeEventListener('mousemove', onMove)
      root.removeEventListener('mouseleave', scheduleHide)
      editor.off('transaction', refresh)
      editor.off('focus', refresh)
      editor.off('blur', refresh)
      window.removeEventListener('scroll', refresh, { capture: true })
      window.removeEventListener('resize', refresh)
      cancelHide()
      cancelAnimationFrame(frame.current)
    }
  }, [editor, refresh])

  if (!layout || typeof document === 'undefined') return null
  const { table, column, row, cell, visibleX } = layout
  const inX = (x: number) => x >= visibleX[0] - 1 && x <= visibleX[1] + 1
  const columnX = column.left + column.width / 2
  const onMenu = (open: boolean) => {
    menus.current += open ? 1 : -1
    // Closing hands the controls back to the pointer or the cursor.
    if (!open) refresh()
  }
  const tableEnd = (rowIndex: number, colIndex: number): CellRef => ({ tablePos: cell.tablePos, row: rowIndex, col: colIndex })

  return createPortal(
    <div role="group" aria-label={t.toolbar.tableControls} className="pointer-events-none fixed inset-0 z-10" onMouseEnter={() => clearTimeout(hideTimer.current)}>
      {/* Along the bottom edge: a row at the end. */}
      <button
        type="button"
        aria-label={t.toolbar.addRowEnd}
        title={t.toolbar.addRowEnd}
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => insertRow(editor, tableEnd(layout.rows - 1, 0), 'after')}
        className="group/add pointer-events-auto absolute flex items-center justify-center outline-none"
        style={{ left: Math.max(table.left, visibleX[0]), width: Math.min(table.left + table.width, visibleX[1]) - Math.max(table.left, visibleX[0]), top: table.top + table.height + 2, height: 14 }}
      >
        <span className="flex h-3.5 w-full items-center justify-center rounded-sm bg-muted/0 text-muted-foreground opacity-60 group-hover/add:bg-muted group-hover/add:opacity-100 group-focus-visible/add:bg-muted group-focus-visible/add:opacity-100">
          <Plus className="size-3" />
        </span>
      </button>
      {/* Along the right edge: a column at the end. */}
      {inX(table.left + table.width) && (
        <button
          type="button"
          aria-label={t.toolbar.addColumnEnd}
          title={t.toolbar.addColumnEnd}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => insertColumn(editor, tableEnd(0, layout.cols - 1), 'after')}
          className="group/add pointer-events-auto absolute flex items-center justify-center outline-none"
          style={{ left: Math.min(table.left + table.width, visibleX[1]) + 2, width: 14, top: table.top, height: table.height }}
        >
          <span className="flex h-full w-3.5 items-center justify-center rounded-sm text-muted-foreground opacity-60 group-hover/add:bg-muted group-hover/add:opacity-100 group-focus-visible/add:bg-muted group-focus-visible/add:opacity-100">
            <Plus className="size-3" />
          </span>
        </button>
      )}
      {/* The column under the pointer: a handle on its top edge. */}
      {inX(columnX) && (
        <DropdownMenu onOpenChange={onMenu}>
          <DropdownMenuTrigger
            aria-label={t.toolbar.columnHandle}
            title={t.toolbar.columnHandle}
            onMouseDown={(e) => e.preventDefault()}
            className={`${handle} pointer-events-auto h-4 w-7`}
            style={{ left: columnX - 14, top: table.top - 9 }}
          >
            <GripHorizontal className="size-3.5" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="center" className="min-w-52">
            <ColumnItems editor={editor} cell={cell} />
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      {/* The row under the pointer: a handle on its left edge. */}
      {inX(table.left) && (
        <DropdownMenu onOpenChange={onMenu}>
          <DropdownMenuTrigger
            aria-label={t.toolbar.rowHandle}
            title={t.toolbar.rowHandle}
            onMouseDown={(e) => e.preventDefault()}
            className={`${handle} pointer-events-auto h-7 w-4`}
            style={{ left: table.left - 19, top: row.top + row.height / 2 - 14 }}
          >
            <GripVertical className="size-3.5" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="center" side="right" className="min-w-52">
            <RowItems editor={editor} cell={cell} />
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      {/* The whole table: the corner. */}
      {inX(table.left) && (
        <DropdownMenu onOpenChange={onMenu}>
          <DropdownMenuTrigger
            aria-label={t.toolbar.tableHandle}
            title={t.toolbar.tableHandle}
            onMouseDown={(e) => e.preventDefault()}
            className={`${handle} pointer-events-auto size-4`}
            style={{ left: table.left - 20, top: table.top - 20 }}
          >
            <Table2 className="size-3" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-44">
            <TableItems editor={editor} cell={cell} />
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>,
    document.body,
  )
}
