import { afterEach, describe, expect, it } from 'vitest'
import type { Editor } from '@tiptap/core'
import { createExtensions } from '@/markdown'
import { makeEditor, paste } from '@/markdown/testTyping'
import { hardBreaksInParagraphs, PlainTextPaste, splitPlainText } from './paste'

let editor: Editor
const fresh = (content?: Parameters<typeof makeEditor>[0]) => {
  editor = makeEditor(content, [...createExtensions(), PlainTextPaste])
  editor.commands.focus('end')
  return editor
}
afterEach(() => editor?.destroy())

const br = { type: 'hardBreak' }
const t = (text: string, marks?: { type: string; attrs?: object }[]) => ({ type: 'text', text, ...(marks ? { marks } : {}) })
const p = (...content: object[]) => ({ type: 'paragraph', content })
const body = () => editor.getJSON().content

describe('splitPlainText', () => {
  it('single breaks stay inside a paragraph, blank lines split', () => {
    expect(splitPlainText('a\nb\n\nc')).toEqual([['a', 'b'], ['c']])
  })
  it('any run of blank lines (also with spaces) is one split; the ends are trimmed', () => {
    expect(splitPlainText('\n\na\n \t \n\n\nb\n\n')).toEqual([['a'], ['b']])
  })
  it('CRLF and CR', () => expect(splitPlainText('a\r\nb\r\rc')).toEqual([['a', 'b'], ['c']]))
  it('indentation is kept', () => expect(splitPlainText('  a\n\tb')).toEqual([['  a', '\tb']]))
  it('nothing but blanks', () => expect(splitPlainText(' \n\n ')).toEqual([]))
})

describe('plain text paste', () => {
  it('line breaks become hard breaks, blank lines paragraphs', () => {
    fresh()
    paste(editor, { text: '床前明月光\n疑是地上霜\n\n举头望明月\n低头思故乡\n' })
    expect(body()).toEqual([p(t('床前明月光'), br, t('疑是地上霜')), p(t('举头望明月'), br, t('低头思故乡'))])
  })
  it('one line goes inline into the current paragraph', () => {
    fresh({ type: 'doc', content: [p(t('前后'))] })
    editor.commands.setTextSelection(2)
    paste(editor, { text: '中间' })
    expect(body()).toEqual([p(t('前中间后'))])
  })
  it('several lines in the middle of a paragraph stay in it', () => {
    fresh({ type: 'doc', content: [p(t('前后'))] })
    editor.commands.setTextSelection(2)
    paste(editor, { text: 'a\nb' })
    expect(body()).toEqual([p(t('前a'), br, t('b后'))])
  })
  it('takes on the marks at the cursor, as the default does', () => {
    fresh({ type: 'doc', content: [p(t('粗', [{ type: 'bold' }]))] })
    paste(editor, { text: 'x\ny' })
    const bold = [{ type: 'bold' }]
    expect(body()).toEqual([p(t('粗x', bold), { type: 'hardBreak', marks: bold }, t('y', bold))])
  })
  it('stays literal in a code block', () => {
    fresh({ type: 'doc', content: [{ type: 'codeBlock', content: [t('x')] }] })
    paste(editor, { text: '# a\n\n- b\n- c\r\nd' })
    expect(body()).toEqual([{ type: 'codeBlock', attrs: { language: null }, content: [t('x# a\n\n- b\n- c\nd')] }])
  })
  it('in a heading the lines are joined with a space', () => {
    fresh({ type: 'doc', content: [{ type: 'heading', attrs: { level: 2 }, content: [t('题')] }] })
    paste(editor, { text: '甲\n乙' })
    expect(body()).toEqual([{ type: 'heading', attrs: { level: 2 }, content: [t('题甲 乙')] }])
  })
  it('replaces a selection', () => {
    fresh({ type: 'doc', content: [p(t('abc'))] })
    editor.commands.setTextSelection({ from: 1, to: 3 })
    paste(editor, { text: 'x\ny' })
    expect(body()).toEqual([p(t('x'), br, t('yc'))])
  })
  it('text and HTML together is the browser\'s own HTML paste', () => {
    fresh()
    paste(editor, { text: 'a\nb', html: '<p>rich</p>' })
    expect(body()).toEqual([p(t('rich'))])
  })
})

describe('Markdown source paste', () => {
  const source = '# 标题\n\n- 一\n- 二\n\n正文一行\n正文二行\n\n**粗**和[链接](https://example.com)\n'
  it('becomes formatted content, with hard breaks in ordinary paragraphs', () => {
    fresh()
    paste(editor, { text: source })
    expect(body()).toEqual([
      { type: 'heading', attrs: { level: 1 }, content: [t('标题')] },
      { type: 'bulletList', content: [{ type: 'listItem', content: [p(t('一'))] }, { type: 'listItem', content: [p(t('二'))] }] },
      p(t('正文一行'), br, t('正文二行')),
      p(t('粗', [{ type: 'bold' }]), t('和'), t('链接', [{ type: 'link', attrs: { href: 'https://example.com', target: '_blank', rel: 'noopener noreferrer nofollow', class: null, title: null } }])),
    ])
  })
  it('a fenced block keeps its text and language', () => {
    fresh()
    paste(editor, { text: '```ts\nlet a = 1\n\nlet b\n```' })
    expect(body()).toEqual([{ type: 'codeBlock', attrs: { language: 'ts' }, content: [t('let a = 1\n\nlet b')] }])
  })
  it('a table and a quote', () => {
    fresh()
    paste(editor, { text: '| a | b |\n| --- | --- |\n| 1 | 2 |\n\n> 引用\n> 两行' })
    const types = body()!.map((n) => n.type)
    expect(types).toEqual(['table', 'blockquote'])
    expect(body()![1].content).toEqual([p(t('引用'), br, t('两行'))])
  })
  it('an inline-only result merges into the paragraph', () => {
    fresh({ type: 'doc', content: [p(t('前后'))] })
    editor.commands.setTextSelection(2)
    paste(editor, { text: '**粗**' })
    expect(body()).toEqual([p(t('前'), t('粗', [{ type: 'bold' }]), t('后'))])
  })
  it('a heading or list pasted in the middle of a paragraph splits it instead of merging into it', () => {
    fresh({ type: 'doc', content: [p(t('前后'))] })
    editor.commands.setTextSelection(2)
    paste(editor, { text: '# 标题\n\n- 一\n- 二' })
    expect(body()!.map((n) => n.type)).toEqual(['paragraph', 'heading', 'bulletList', 'paragraph'])
    expect(body()![0]).toEqual(p(t('前')))
    expect(body()![3]).toEqual(p(t('后')))
  })
  it('Shift+paste inserts the text as it is, with only the line-break rule', () => {
    fresh()
    paste(editor, { text: '# 标题\n- 一\n- 二', shift: true })
    expect(body()).toEqual([p(t('# 标题'), br, t('- 一'), br, t('- 二'))])
  })
  it('not into a table cell', () => {
    fresh({ type: 'doc', content: [{ type: 'table', content: [{ type: 'tableRow', content: [{ type: 'tableHeader', content: [p()] }] }] }] })
    editor.commands.focus('start')
    paste(editor, { text: '# x\n- a\n- b' })
    expect(JSON.stringify(body())).not.toContain('"heading"')
    expect(JSON.stringify(body())).not.toContain('"bulletList"')
  })
  it('text that is not Markdown takes the plain path', () => {
    fresh()
    paste(editor, { text: '用 `foo` 来调用\n第二行' })
    expect(body()).toEqual([p(t('用 `foo` 来调用'), br, t('第二行'))])
  })
})

describe('hardBreaksInParagraphs', () => {
  it('splits newlines in paragraph text, not in code', () => {
    const doc = { type: 'doc', content: [p(t('a\nb', [{ type: 'bold' }]), t('c\nd', [{ type: 'code' }])), { type: 'codeBlock', content: [t('x\ny')] }] }
    expect(hardBreaksInParagraphs(doc)).toEqual({
      type: 'doc',
      content: [p(t('a', [{ type: 'bold' }]), { type: 'hardBreak', marks: [{ type: 'bold' }] }, t('b', [{ type: 'bold' }]), t('c\nd', [{ type: 'code' }])), { type: 'codeBlock', content: [t('x\ny')] }],
    })
  })
})
