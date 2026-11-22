// Typing shortcuts, driven through a real editor (see inputRules.ts).

import { afterEach, describe, expect, it } from 'vitest'
import type { Editor } from '@tiptap/core'
import { makeEditor, runsOf, typeText } from './testTyping'

let editor: Editor
const fresh = (content?: Parameters<typeof makeEditor>[0]) => (editor = makeEditor(content))
const typed = (text: string) => {
  fresh()
  editor.commands.focus('end')
  typeText(editor, text)
  return runsOf(editor)
}
afterEach(() => editor?.destroy())

describe('right after a Chinese character', () => {
  it('bold', () => expect(typed('这是**粗体**')).toEqual([{ text: '这是', marks: [] }, { text: '粗体', marks: ['bold'] }]))
  it('italic', () => expect(typed('这是*斜体*')).toEqual([{ text: '这是', marks: [] }, { text: '斜体', marks: ['italic'] }]))
  it('strike', () => expect(typed('这是~~删除~~')).toEqual([{ text: '这是', marks: [] }, { text: '删除', marks: ['strike'] }]))
  it('inline code', () => expect(typed('这是`代码`')).toEqual([{ text: '这是', marks: [] }, { text: '代码', marks: ['code'] }]))
  it('keeps typing outside the mark afterwards', () => expect(typed('这是**粗体**的字')).toEqual([{ text: '这是', marks: [] }, { text: '粗体', marks: ['bold'] }, { text: '的字', marks: [] }]))
  it('works next to full-width punctuation too', () => expect(typed('他说：**别急**')).toEqual([{ text: '他说：', marks: [] }, { text: '别急', marks: ['bold'] }]))
})

describe('as before', () => {
  it('at the start of the line and after a space', () => {
    expect(typed('**a**')).toEqual([{ text: 'a', marks: ['bold'] }])
    expect(typed('x *a*')).toEqual([{ text: 'x ', marks: [] }, { text: 'a', marks: ['italic'] }])
  })
  it('after a Latin letter, as CommonMark reads it', () => expect(typed('a**b**')).toEqual([{ text: 'a', marks: [] }, { text: 'b', marks: ['bold'] }]))
  it('inside existing text', () => {
    fresh()
    editor.commands.setContent('<p>前后</p>')
    editor.commands.setTextSelection(2)
    typeText(editor, '**中**')
    expect(runsOf(editor)).toEqual([{ text: '前', marks: [] }, { text: '中', marks: ['bold'] }, { text: '后', marks: [] }])
  })
})

describe('what must not convert', () => {
  it('`**` is not two italics, at any point while typing', () => {
    expect(typed('字**粗体*')).toEqual([{ text: '字**粗体*', marks: [] }])
    expect(typed('字**粗体**')).toEqual([{ text: '字', marks: [] }, { text: '粗体', marks: ['bold'] }])
  })
  it('spaces just inside the markers', () => {
    expect(typed('2 * 3 * 4')).toEqual([{ text: '2 * 3 * 4', marks: [] }])
    expect(typed('a ** b ** c')).toEqual([{ text: 'a ** b ** c', marks: [] }])
    expect(typed('~~ x ~~')).toEqual([{ text: '~~ x ~~', marks: [] }])
    expect(typed('` `')).toEqual([{ text: '` `', marks: [] }])
  })
  it('a single tilde is not strike', () => expect(typed('约~10 个，5~8 元~')).toEqual([{ text: '约~10 个，5~8 元~', marks: [] }]))
  it('an empty pair', () => {
    expect(typed('****')).toEqual([{ text: '****', marks: [] }])
    expect(typed('``')).toEqual([{ text: '``', marks: [] }])
  })
  it('bold and italic at once is left as typed', () => expect(typed('***x***')).toEqual([{ text: '***x***', marks: [] }]))
  it('not inside a code block', () => {
    fresh({ type: 'doc', content: [{ type: 'codeBlock', content: [{ type: 'text', text: '前' }] }] })
    editor.commands.focus('end')
    typeText(editor, '**粗**~~删~~`码`')
    expect(editor.getJSON().content![0].content).toEqual([{ type: 'text', text: '前**粗**~~删~~`码`' }])
  })
  it('not inside inline code', () => {
    fresh({ type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'x', marks: [{ type: 'code' }] }] }] })
    editor.commands.focus('end')
    typeText(editor, '**粗**')
    expect(runsOf(editor)).toEqual([{ text: 'x**粗**', marks: ['code'] }])
  })
})

describe('underscore keeps the word boundary', () => {
  it('after a space or at the start', () => {
    expect(typed('_a_')).toEqual([{ text: 'a', marks: ['italic'] }])
    expect(typed('x __a__')).toEqual([{ text: 'x ', marks: [] }, { text: 'a', marks: ['bold'] }])
  })
  it('after punctuation', () => expect(typed('(_a_')).toEqual([{ text: '(', marks: [] }, { text: 'a', marks: ['italic'] }]))
  it('not after a letter, a digit or a Chinese character', () => {
    expect(typed('snake_case_name')).toEqual([{ text: 'snake_case_name', marks: [] }])
    expect(typed('v1_2_')).toEqual([{ text: 'v1_2_', marks: [] }])
    expect(typed('中文_斜体_')).toEqual([{ text: '中文_斜体_', marks: [] }])
    expect(typed('中文__粗体__')).toEqual([{ text: '中文__粗体__', marks: [] }])
  })
  it('`__` is not two italics', () => expect(typed('x __a_')).toEqual([{ text: 'x __a_', marks: [] }]))
})
