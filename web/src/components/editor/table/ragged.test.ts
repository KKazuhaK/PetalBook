// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { Editor } from '@tiptap/core'
import { createExtensions, docToMarkdown, markdownToDoc } from '@/markdown'
import { TableTools } from './TableTools'

// Markdown allows rows shorter or longer than the header. Such a table must
// take edits like any other.
describe('a table whose rows differ in length', () => {
  for (const md of ['| a | b |\n| :- | -: |\n| 1 |\n', '| a |\n| --- |\n| 1 | 2 | 3 |\n']) {
    it(`can be typed in: ${JSON.stringify(md)}`, () => {
      const editor = new Editor({ extensions: [...createExtensions(), TableTools], content: markdownToDoc(md) })
      let cell = -1
      editor.state.doc.descendants((node, pos) => {
        if (cell < 0 && node.type.name === 'paragraph') cell = pos + 1
        return cell < 0
      })
      expect(() => editor.chain().setTextSelection(cell).insertContent('x').run()).not.toThrow()
      expect(docToMarkdown(editor.getJSON())).toContain('x')
      // Replacing the whole content with such a table works too.
      expect(() => editor.commands.setContent(markdownToDoc(md))).not.toThrow()
      editor.destroy()
    })
  }
})

// Rows are filled up at their end when the Markdown is read, so no cell
// changes column when the table is first edited.
describe('rows of different lengths keep their columns', () => {
  it('a short row gets empty cells at its end, a long row widens the table', () => {
    expect(docToMarkdown(markdownToDoc('| a | b | c |\n| --- | --- | --- |\n| 1 |\n'))).toBe('| a | b | c |\n| --- | --- | --- |\n| 1 | | |\n')
    const wide = markdownToDoc('| a | b |\n| --- | --- |\n| 1 | 2 | 3 |\n')
    const editor = new Editor({ extensions: [...createExtensions(), TableTools], content: wide })
    let first = -1
    editor.state.doc.descendants((node, pos) => {
      if (first < 0 && node.type.name === 'paragraph') first = pos + 1
      return first < 0
    })
    editor.chain().setTextSelection(first).insertContent('x').run()
    const md = docToMarkdown(editor.getJSON())
    // "a" is still over "1", "b" over "2"; the third column is the new one.
    expect(md.split('\n')[0]).toBe('| xa | b | |')
    expect(md.split('\n')[2]).toBe('| 1 | 2 | 3 |')
    editor.destroy()
  })
})
