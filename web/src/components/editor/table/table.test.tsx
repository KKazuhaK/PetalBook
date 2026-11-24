import { describe, expect, it } from 'vitest'
import type { Editor } from '@tiptap/core'
import { markdownToDoc } from '@/markdown'
import { cursorIn, markdownOf, pageEditor } from '../pageTestKit'
import {
  cellAtSelection,
  columnAlign,
  insertColumn,
  insertRow,
  insertTable,
  removeColumn,
  removeRow,
  removeTable,
  setColumnAlign,
  tableFixes,
  tableSize,
  type CellRef,
} from './tableOps'

const SOURCE = '| 甲 | 乙 |\n| - | - |\n| 1 | 2 |\n| 3 | 4 |\n'
const ref = (row: number, col: number): CellRef => ({ tablePos: 0, row, col })

// The cell types of every row of the first table, as "H" and "c".
function shape(editor: Editor): string[] {
  const table = editor.state.doc.firstChild!
  const rows: string[] = []
  table.forEach((row) => {
    let s = ''
    row.forEach((cell) => (s += cell.type.name === 'tableHeader' ? 'H' : 'c'))
    rows.push(s)
  })
  return rows
}
const text = (editor: Editor) =>
  editor.state.doc.firstChild!.content.content.map((row) =>
    row.content.content.map((cell) => cell.textContent).join('|'),
  )

describe('inserting a table of a given size', () => {
  it('has that many columns and rows, and the first row is the header', () => {
    const editor = pageEditor()
    expect(insertTable(editor, 3, 4)).toBe(true)
    const table = editor.state.doc.firstChild!
    expect(table.type.name).toBe('table')
    expect(tableSize(editor.state, 0)).toEqual({ cols: 3, rows: 4 })
    expect(shape(editor)).toEqual(['HHH', 'ccc', 'ccc', 'ccc'])
  })

  it('writes as a Markdown table of that shape', () => {
    const editor = pageEditor()
    insertTable(editor, 2, 2)
    const md = markdownOf(editor)
    expect(md.split('\n').filter((l) => l.startsWith('|'))).toHaveLength(3)
    expect(md).toMatch(/\|\s*-+\s*\|\s*-+\s*\|/)
  })

  it('works at the largest size picked from the grid', () => {
    const editor = pageEditor()
    insertTable(editor, 10, 8)
    expect(tableSize(editor.state, 0)).toEqual({ cols: 10, rows: 8 })
  })
})

describe('adding rows and columns', () => {
  it('adds a row after, and the new row is a body row', () => {
    const editor = pageEditor(SOURCE)
    expect(insertRow(editor, ref(0, 0), 'after')).toBe(true)
    expect(text(editor)).toEqual(['甲|乙', '|', '1|2', '3|4'])
    expect(shape(editor)).toEqual(['HH', 'cc', 'cc', 'cc'])
  })

  it('adds a row at the end, as the + along the bottom edge does', () => {
    const editor = pageEditor(SOURCE)
    insertRow(editor, ref(2, 0), 'after')
    expect(text(editor)).toEqual(['甲|乙', '1|2', '3|4', '|'])
  })

  it('adds a row above a body row', () => {
    const editor = pageEditor(SOURCE)
    insertRow(editor, ref(1, 1), 'before')
    expect(text(editor)).toEqual(['甲|乙', '|', '1|2', '3|4'])
  })

  it('adds a column before and after, header cell included', () => {
    const editor = pageEditor(SOURCE)
    insertColumn(editor, ref(0, 0), 'before')
    insertColumn(editor, ref(0, 2), 'after')
    expect(text(editor)).toEqual(['|甲|乙|', '|1|2|', '|3|4|'])
    expect(shape(editor)).toEqual(['HHHH', 'cccc', 'cccc'])
    expect(tableSize(editor.state, 0)).toEqual({ cols: 4, rows: 3 })
  })
})

describe('deleting rows and columns', () => {
  it('deletes a body row', () => {
    const editor = pageEditor(SOURCE)
    expect(removeRow(editor, ref(1, 0))).toBe(true)
    expect(text(editor)).toEqual(['甲|乙', '3|4'])
  })

  it('deleting the header row makes the next row the header', () => {
    const editor = pageEditor(SOURCE)
    expect(removeRow(editor, ref(0, 0))).toBe(true)
    expect(text(editor)).toEqual(['1|2', '3|4'])
    expect(shape(editor)).toEqual(['HH', 'cc'])
    // As Markdown: a header row again, and still exactly one.
    expect(markdownOf(editor)).toBe('| 1 | 2 |\n| --- | --- |\n| 3 | 4 |\n')
  })

  it('never deletes the only row', () => {
    const editor = pageEditor('| 甲 | 乙 |\n| - | - |\n')
    expect(removeRow(editor, ref(0, 0))).toBe(false)
    expect(tableSize(editor.state, 0)).toEqual({ cols: 2, rows: 1 })
  })

  it('deletes a column, and never the only one', () => {
    const editor = pageEditor(SOURCE)
    expect(removeColumn(editor, ref(0, 0))).toBe(true)
    expect(text(editor)).toEqual(['乙', '2', '4'])
    expect(removeColumn(editor, ref(0, 0))).toBe(false)
    expect(tableSize(editor.state, 0)).toEqual({ cols: 1, rows: 3 })
  })

  it('deletes the whole table', () => {
    const editor = pageEditor('前\n\n' + SOURCE + '\n后\n')
    const at = editor.state.doc.child(0).nodeSize
    expect(removeTable(editor, { tablePos: at, row: 1, col: 1 })).toBe(true)
    expect(markdownOf(editor)).toBe('前\n\n后\n')
  })
})

describe('column alignment', () => {
  it('is set on the column and written on the delimiter line', () => {
    const editor = pageEditor(SOURCE)
    setColumnAlign(editor, ref(2, 1), 'center')
    setColumnAlign(editor, ref(0, 0), 'right')
    expect(markdownOf(editor)).toBe('| 甲 | 乙 |\n| ---: | :---: |\n| 1 | 2 |\n| 3 | 4 |\n')
    expect(columnAlign(editor.state, ref(0, 0))).toBe('right')
    expect(columnAlign(editor.state, ref(0, 1))).toBe('center')
  })

  it('can be set to left and cleared', () => {
    const editor = pageEditor(SOURCE)
    setColumnAlign(editor, ref(0, 0), 'left')
    expect(markdownOf(editor)).toContain('| :--- | --- |')
    setColumnAlign(editor, ref(0, 0), null)
    expect(markdownOf(editor)).toBe('| 甲 | 乙 |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |\n')
  })

  it('answers "none" for a table that was replaced while its menu was open, as the size does, instead of throwing', () => {
    const editor = pageEditor(SOURCE)
    const open = ref(1, 1)
    editor.commands.setContent('只剩一段话')
    // What was a table at that position is a paragraph now.
    expect(editor.state.doc.nodeAt(0)!.type.name).toBe('paragraph')
    expect(() => columnAlign(editor.state, open)).not.toThrow()
    expect(columnAlign(editor.state, open)).toBeNull()
    expect(tableSize(editor.state, 0)).toBeNull()
    // The action on it does nothing and does not throw either.
    expect(() => setColumnAlign(editor, open, 'center')).not.toThrow()
    expect(markdownOf(editor)).toBe('只剩一段话\n')
  })

  it('answers "none" for a column that the replacing table does not have', () => {
    const editor = pageEditor('| 甲 | 乙 | 丙 |\n| - | - | - |\n| 1 | 2 | 3 |\n')
    editor.commands.setContent(markdownToDoc('| 甲 |\n| - |\n| 1 |\n'))
    expect(editor.state.doc.nodeAt(0)!.type.name).toBe('table')
    const before = markdownOf(editor)
    expect(columnAlign(editor.state, ref(0, 2))).toBeNull()
    expect(() => setColumnAlign(editor, ref(0, 2), 'right')).not.toThrow()
    expect(setColumnAlign(editor, ref(0, 2), 'right')).toBe(false)
    expect(() => insertRow(editor, ref(1, 2), 'after')).not.toThrow()
    expect(() => removeColumn(editor, ref(0, 2))).not.toThrow()
    expect(markdownOf(editor)).toBe(before)
  })

  it('reads back from Markdown', () => {
    const editor = pageEditor('| a | b | c |\n| :- | :-: | -: |\n| 1 | 2 | 3 |\n')
    expect([0, 1, 2].map((c) => columnAlign(editor.state, ref(1, c)))).toEqual(['left', 'center', 'right'])
  })

  it('is kept by a row added later, and moves with a column added before it', () => {
    const editor = pageEditor('| a | b |\n| - | :-: |\n| 1 | 2 |\n')
    insertRow(editor, ref(1, 0), 'after')
    insertColumn(editor, ref(0, 0), 'before')
    expect(markdownOf(editor)).toBe('| | a | b |\n| --- | --- | :---: |\n| | 1 | 2 |\n| | | |\n')
  })
})

describe('the rules of a Markdown table, applied after every edit', () => {
  it('say what to fix: header cells only in the first row, one alignment per column', () => {
    const editor = pageEditor()
    editor.commands.setContent({
      type: 'doc',
      content: [
        {
          type: 'table',
          content: [
            {
              type: 'tableRow',
              content: [
                { type: 'tableCell', attrs: { align: 'right' }, content: [{ type: 'paragraph' }] },
                { type: 'tableHeader', content: [{ type: 'paragraph' }] },
              ],
            },
            {
              type: 'tableRow',
              content: [
                { type: 'tableHeader', content: [{ type: 'paragraph' }] },
                { type: 'tableCell', attrs: { align: 'center' }, content: [{ type: 'paragraph' }] },
              ],
            },
          ],
        },
      ],
    })
    // Edited by hand through the schema: the plugin has already put this right.
    expect(shape(editor)).toEqual(['HH', 'cc'])
    expect(columnAlign(editor.state, ref(0, 0))).toBe('right')
    expect(columnAlign(editor.state, ref(1, 1))).toBeNull()
    expect(tableFixes(editor.state.doc.firstChild!, 0)).toEqual([])
  })

  it('a table that breaks them is reported cell by cell', () => {
    const editor = pageEditor(SOURCE)
    // Without the plugin (a bare transaction that skips it is not possible), build the node by hand.
    const schema = editor.schema
    const cell = (type: 'tableCell' | 'tableHeader', align: string | null = null) =>
      schema.nodes[type].create({ align }, schema.nodes.paragraph.create())
    const row = (...cells: ReturnType<typeof cell>[]) => schema.nodes.tableRow.create(null, cells)
    const table = schema.nodes.table.create(null, [
      row(cell('tableCell', 'left'), cell('tableHeader')),
      row(cell('tableHeader'), cell('tableCell', 'right')),
    ])
    const fixes = tableFixes(table, 10)
    expect(fixes.map((f) => [f.type, f.align])).toEqual([
      ['tableHeader', 'left'],
      ['tableCell', 'left'],
      ['tableCell', null],
    ])
    expect(fixes.every((f) => f.pos > 10)).toBe(true)
  })

  it('stay out of the way of typing: one transaction per keystroke', () => {
    const editor = pageEditor(SOURCE)
    cursorIn(editor, '1')
    let count = 0
    editor.on('transaction', () => count++)
    editor.commands.insertContent('x')
    expect(count).toBe(1)
    expect(text(editor)[1]).toBe('1x|2')
    expect(cellAtSelection(editor.state)).toEqual(ref(1, 0))
  })
})

describe('Tab in a table', () => {
  const press = (editor: Editor, key: string, shift = false) =>
    editor.view.someProp('handleKeyDown', (f) => f(editor.view, new KeyboardEvent('keydown', { key, shiftKey: shift })))

  it('moves to the next cell, and in the last cell adds a row', () => {
    const editor = pageEditor(SOURCE)
    cursorIn(editor, '3')
    expect(press(editor, 'Tab')).toBe(true)
    expect(cellAtSelection(editor.state)).toEqual(ref(2, 1))
    expect(press(editor, 'Tab')).toBe(true)
    expect(tableSize(editor.state, 0)).toEqual({ cols: 2, rows: 4 })
    expect(cellAtSelection(editor.state)).toEqual(ref(3, 0))
    expect(shape(editor)).toEqual(['HH', 'cc', 'cc', 'cc'])
  })
})

describe('selection and copying in cells', () => {
  it('the cursor ends up in a cell of the table after an action, ready to type', () => {
    const editor = pageEditor(SOURCE)
    cursorIn(editor, '4')
    insertRow(editor, ref(1, 0), 'before')
    expect(cellAtSelection(editor.state)).not.toBeNull()
    expect(text(editor)).toEqual(['甲|乙', '|', '1|2', '3|4'])
  })

  it('what is copied out of a cell is its text, and pasting text in goes into the cell', () => {
    const editor = pageEditor(SOURCE)
    cursorIn(editor, '1')
    editor.commands.setTextSelection({ from: editor.state.selection.from - 1, to: editor.state.selection.from })
    const copied = editor.view.state.doc.textBetween(editor.state.selection.from, editor.state.selection.to)
    expect(copied).toBe('1')
    editor.commands.insertContent('一二')
    expect(text(editor)[1]).toBe('一二|2')
    expect(markdownOf(editor)).toContain('| 一二 | 2 |')
  })
})
