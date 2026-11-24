import type { Editor } from '@tiptap/core'
import { useEditorState } from '@tiptap/react'
import {
  AlignCenter,
  AlignLeft,
  AlignRight,
  ArrowDownToLine,
  ArrowLeftToLine,
  ArrowRightToLine,
  ArrowUpToLine,
  Trash2,
} from 'lucide-react'
import {
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { t } from '@/i18n'
import {
  canRemove,
  columnAlign,
  insertColumn,
  insertRow,
  removeColumn,
  removeRow,
  removeTable,
  setColumnAlign,
  type Align,
  type CellRef,
} from './tableOps'

// The entries of the table menus, shared by the handles at the table's edges
// and by the toolbar's table menu (the way in from the keyboard).

function useCellState(editor: Editor, cell: CellRef) {
  return useEditorState({
    editor,
    selector: ({ editor: e }) => ({ ...canRemove(e.state, cell), align: columnAlign(e.state, cell) }),
  })
}

export function RowItems({ editor, cell }: { editor: Editor; cell: CellRef }) {
  const s = useCellState(editor, cell)
  return (
    <DropdownMenuGroup>
      {/* Above the header row would make an empty row the header and demote the real one. */}
      <DropdownMenuItem disabled={cell.row === 0} onClick={() => insertRow(editor, cell, 'before')}>
        <ArrowUpToLine />
        {t.toolbar.addRowBefore}
      </DropdownMenuItem>
      <DropdownMenuItem onClick={() => insertRow(editor, cell, 'after')}>
        <ArrowDownToLine />
        {t.toolbar.addRowAfter}
      </DropdownMenuItem>
      <DropdownMenuItem disabled={!s.row} variant="destructive" onClick={() => removeRow(editor, cell)}>
        <Trash2 />
        {cell.row === 0 ? t.toolbar.deleteHeaderRow : t.toolbar.deleteRow}
      </DropdownMenuItem>
    </DropdownMenuGroup>
  )
}

const ALIGNS: { value: Exclude<Align, null> | 'default'; icon: typeof AlignLeft; label: () => string }[] = [
  { value: 'left', icon: AlignLeft, label: () => t.toolbar.alignLeft },
  { value: 'center', icon: AlignCenter, label: () => t.toolbar.alignCenter },
  { value: 'right', icon: AlignRight, label: () => t.toolbar.alignRight },
]

export function ColumnItems({ editor, cell }: { editor: Editor; cell: CellRef }) {
  const s = useCellState(editor, cell)
  return (
    <>
      <DropdownMenuGroup>
        <DropdownMenuItem onClick={() => insertColumn(editor, cell, 'before')}>
          <ArrowLeftToLine />
          {t.toolbar.addColumnBefore}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => insertColumn(editor, cell, 'after')}>
          <ArrowRightToLine />
          {t.toolbar.addColumnAfter}
        </DropdownMenuItem>
      </DropdownMenuGroup>
      <DropdownMenuSeparator />
      <DropdownMenuGroup>
        <DropdownMenuRadioGroup
          value={s.align ?? 'default'}
          onValueChange={(v) => setColumnAlign(editor, cell, v === 'default' ? null : (v as Exclude<Align, null>))}
        >
          {ALIGNS.map(({ value, icon: Icon, label }) => (
            <DropdownMenuRadioItem key={value} value={value} closeOnClick>
              <Icon />
              {label()}
            </DropdownMenuRadioItem>
          ))}
          <DropdownMenuRadioItem value="default" closeOnClick>
            {t.toolbar.alignDefault}
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuGroup>
      <DropdownMenuSeparator />
      <DropdownMenuGroup>
        <DropdownMenuItem disabled={!s.column} variant="destructive" onClick={() => removeColumn(editor, cell)}>
          <Trash2 />
          {t.toolbar.deleteColumn}
        </DropdownMenuItem>
      </DropdownMenuGroup>
    </>
  )
}

export function TableItems({ editor, cell }: { editor: Editor; cell: CellRef }) {
  return (
    <DropdownMenuItem variant="destructive" onClick={() => removeTable(editor, cell)}>
      <Trash2 />
      {t.toolbar.deleteTable}
    </DropdownMenuItem>
  )
}
