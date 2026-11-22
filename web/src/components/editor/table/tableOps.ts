// Table editing beyond what the table extension offers: the rules that keep a
// table expressible in Markdown, and the actions of the table controls.
//
// Markdown has exactly one header row, always the first, and one alignment per
// column, written on the header's delimiter line. The editor's cells are
// finer than that (any row may hold header cells, any cell may be aligned), so
// `tableFixes` says what a table has to look like and the plugin in
// TableTools.ts applies it after every edit. That is what makes "delete the
// header row" work: the next row simply becomes the header.

import type { Editor } from '@tiptap/core'
import type { EditorState } from '@tiptap/pm/state'
import { Selection } from '@tiptap/pm/state'
import type { Node as PMNode, ResolvedPos } from '@tiptap/pm/model'
import { TableMap, cellAround } from '@tiptap/pm/tables'

export type Align = 'left' | 'center' | 'right' | null

export interface CellFix {
  // Position of the cell in the document.
  pos: number
  type: 'tableHeader' | 'tableCell'
  align: Align
}

// What is wrong in a table: cells of the first row that are not header cells,
// header cells elsewhere, and cells whose alignment differs from their column's
// (the header cell's, which is the one the Markdown keeps).
export function tableFixes(table: PMNode, tablePos: number): CellFix[] {
  const map = TableMap.get(table)
  const fixes: CellFix[] = []
  const aligns: Align[] = []
  // A table read from a file may have rows of different lengths (Markdown
  // allows it). Until the table extension has padded them, a slot of the grid
  // may be empty, and what its offset points at is then not a cell: only real
  // cells are looked at or changed.
  const cellAt = (offset: number): PMNode | null => {
    const n = offset >= 0 && offset < table.content.size ? table.nodeAt(offset) : null
    return n && (n.type.name === 'tableCell' || n.type.name === 'tableHeader') ? n : null
  }
  for (let c = 0; c < map.width; c++) aligns.push((cellAt(map.map[c])?.attrs.align as Align) ?? null)
  for (let r = 0; r < map.height; r++) {
    for (let c = 0; c < map.width; c++) {
      const offset = map.map[r * map.width + c]
      // A cell spanning several slots is looked at once, at its first.
      if ((c > 0 && map.map[r * map.width + c - 1] === offset) || (r > 0 && map.map[(r - 1) * map.width + c] === offset)) continue
      const cell = cellAt(offset)
      if (!cell) continue
      const type = r === 0 ? 'tableHeader' : 'tableCell'
      const align = aligns[c] ?? null
      if (cell.type.name !== type || ((cell.attrs.align as Align) ?? null) !== align) fixes.push({ pos: tablePos + 1 + offset, type, align })
    }
  }
  return fixes
}

// --- finding cells ---

export interface CellRef {
  // Position of the table node.
  tablePos: number
  row: number
  col: number
}

// The cell that contains a position, if it is inside a table.
export function cellRefAt($pos: ResolvedPos): CellRef | null {
  const $cell = cellAround($pos)
  if (!$cell) return null
  const tablePos = $cell.before($cell.depth - 1)
  const table = $cell.node($cell.depth - 1)
  const rect = TableMap.get(table).findCell($cell.pos - tablePos - 1)
  return { tablePos, row: rect.top, col: rect.left }
}

// The cell the selection is in (or the first of a cell selection).
export const cellAtSelection = (state: EditorState): CellRef | null => cellRefAt(state.selection.$head)

export function tableSize(state: EditorState, tablePos: number): { rows: number; cols: number } | null {
  const table = state.doc.nodeAt(tablePos)
  if (!table || table.type.name !== 'table') return null
  const map = TableMap.get(table)
  return { rows: map.height, cols: map.width }
}

// Puts the cursor in the cell, so the table extension's commands act on it.
function selectCell(ref: CellRef) {
  return ({ tr }: { tr: import('@tiptap/pm/state').Transaction }) => {
    const table = tr.doc.nodeAt(ref.tablePos)
    if (!table || table.type.name !== 'table') return false
    const map = TableMap.get(table)
    if (ref.row >= map.height || ref.col >= map.width) return false
    const start = ref.tablePos + 1 + map.map[ref.row * map.width + ref.col]
    tr.setSelection(Selection.near(tr.doc.resolve(start + 1)))
    return true
  }
}

type Chain = ReturnType<Editor['chain']>
const act = (editor: Editor, ref: CellRef, then: (chain: Chain) => Chain): boolean => then(editor.chain().command(selectCell(ref))).focus().run()

// --- the actions of the controls ---

export const insertRow = (editor: Editor, ref: CellRef, where: 'before' | 'after') => act(editor, ref, (c) => (where === 'before' ? c.addRowBefore() : c.addRowAfter()))
export const insertColumn = (editor: Editor, ref: CellRef, where: 'before' | 'after') => act(editor, ref, (c) => (where === 'before' ? c.addColumnBefore() : c.addColumnAfter()))
export const removeRow = (editor: Editor, ref: CellRef) => act(editor, ref, (c) => c.deleteRow())
export const removeColumn = (editor: Editor, ref: CellRef) => act(editor, ref, (c) => c.deleteColumn())
export const removeTable = (editor: Editor, ref: CellRef) => act(editor, ref, (c) => c.deleteTable())

// The alignment of a column, on every cell of it (the Markdown keeps the header's).
export function setColumnAlign(editor: Editor, ref: CellRef, align: Align): boolean {
  return act(editor, ref, (chain) =>
    chain.command(({ tr }) => {
      const table = tr.doc.nodeAt(ref.tablePos)
      if (!table || table.type.name !== 'table') return false
      const map = TableMap.get(table)
      for (let r = 0; r < map.height; r++) {
        const pos = ref.tablePos + 1 + map.map[r * map.width + ref.col]
        const cell = tr.doc.nodeAt(pos)
        if (cell && ((cell.attrs.align as Align) ?? null) !== align) tr.setNodeMarkup(pos, undefined, { ...cell.attrs, align })
      }
      return true
    }),
  )
}

// What the controls need to know about a column: its alignment. A table menu can stay open while the table is replaced
// (a draft restored, a version loaded): what is at the position is then not a table, or one without that column, and
// the answer is that there is none (as `tableSize` says), not an error.
export function columnAlign(state: EditorState, ref: CellRef): Align {
  const table = state.doc.nodeAt(ref.tablePos)
  if (!table || table.type.name !== 'table') return null
  const map = TableMap.get(table)
  if (ref.col < 0 || ref.col >= map.width) return null
  return (table.nodeAt(map.map[ref.col])?.attrs.align as Align) ?? null
}

// Whether the action can be done: the last row and the last column cannot go.
export function canRemove(state: EditorState, ref: CellRef) {
  const size = tableSize(state, ref.tablePos)
  return { row: (size?.rows ?? 0) > 1, column: (size?.cols ?? 0) > 1 }
}

export function insertTable(editor: Editor, cols: number, rows: number): boolean {
  return editor.chain().focus().insertTable({ rows, cols, withHeaderRow: true }).run()
}
