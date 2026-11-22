import { describe, expect, it } from 'vitest'
import { canInsertFootnote, insertFootnote, nextFootnoteId } from './footnote'
import { cursorIn, markdownOf, pageEditor } from './pageTestKit'

describe('the next footnote name', () => {
  it('is the smallest number not in use', () => {
    expect(nextFootnoteId([])).toBe('1')
    expect(nextFootnoteId(['1', '2'])).toBe('3')
    expect(nextFootnoteId(['2', '3'])).toBe('1')
    expect(nextFootnoteId(['1', '3'])).toBe('2')
  })
  it('leaves names that are not numbers alone', () => {
    expect(nextFootnoteId(['note', 'a1'])).toBe('1')
  })
})

describe('inserting a footnote', () => {
  it('puts a reference at the cursor and its definition at the end of the page', () => {
    const editor = pageEditor('第一段\n\n第二段\n')
    cursorIn(editor, '第一')
    expect(insertFootnote(editor)).toBe(true)
    const md = markdownOf(editor)
    expect(md).toMatch(/^第\[\^1\]一段\n\n第二段\n\n\[\^1\]: /)
  })

  it('numbers the next one after those in use, and keeps the first intact', () => {
    const editor = pageEditor('甲[^1]\n\n[^1]: 注一\n')
    cursorIn(editor, '甲')
    insertFootnote(editor)
    const md = markdownOf(editor)
    expect(md).toContain('[^1]: 注一')
    expect(md).toContain('[^2]: ')
    expect(md.match(/\[\^2\]/g)).toHaveLength(2)
  })

  it('moves the cursor into the new definition, ready for the note', () => {
    const editor = pageEditor('正文\n')
    cursorIn(editor, '正')
    insertFootnote(editor)
    expect(editor.state.selection.$from.node(1).type.name).toBe('footnoteDef')
    editor.commands.insertContent('这是注释')
    expect(markdownOf(editor)).toContain('[^1]: 这是注释')
  })

  it('is one undo step', () => {
    const editor = pageEditor('正文\n')
    cursorIn(editor, '正')
    insertFootnote(editor)
    editor.commands.undo()
    expect(markdownOf(editor)).toBe('正文\n')
  })

  it('is not offered in code, where a reference cannot live', () => {
    const editor = pageEditor('```\ncode\n```\n')
    editor.commands.setTextSelection(3)
    expect(canInsertFootnote(editor.state)).toBe(false)
    expect(insertFootnote(editor)).toBe(false)
    expect(markdownOf(editor)).toBe('```\ncode\n```\n')
  })

  it('works in a table cell and a heading', () => {
    const editor = pageEditor('# 标题\n\n| a | b |\n| - | - |\n| 1 | 2 |\n')
    cursorIn(editor, '标')
    expect(canInsertFootnote(editor.state)).toBe(true)
    cursorIn(editor, '1')
    expect(insertFootnote(editor)).toBe(true)
  })
})
