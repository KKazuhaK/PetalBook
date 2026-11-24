// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { Editor } from '@tiptap/core'
import type { PageLink } from '@/api/types'
import { markdownToDoc } from '@/markdown'
import { destroyEditors, trackEditor } from '@/testing/editors'
import { markdownOf, pageExtensions } from './pageTestKit'
import { PageLinks, pageLinksOf, retitle } from './pageLinks'

// The page link marks without the page around them: what an edited address does to the id in the title, and
// what drawing the links costs in a page that has many.

afterEach(() => destroyEditors())

const MUXUE = '01JD3K8Q7Z'
const OTHER = '01JD3K8Q80'

function linkEditor(markdown: string, links: PageLink[] | undefined, folder = '') {
  let report = links
  const editor = trackEditor(
    new Editor({
      element: document.createElement('div'),
      extensions: [
        ...pageExtensions(),
        PageLinks.configure({ getFolder: () => folder, getLinks: () => report, open: () => undefined }),
      ],
      content: markdownToDoc(markdown),
    }),
  )
  return {
    editor,
    setLinks(next: PageLink[]) {
      report = next
      pageLinksOf(editor)!.refresh()
    },
  }
}

// Puts the cursor in the text `text` and changes the address of its link, as the link's popover does.
function setHref(editor: Editor, text: string, href: string) {
  let at = -1
  editor.state.doc.descendants((node, pos) => {
    if (at < 0 && node.isText && node.text!.includes(text)) at = pos + node.text!.indexOf(text) + 1
  })
  editor.chain().setTextSelection(at).extendMarkRange('link').setLink({ href }).run()
}

const linked = (path: string, id: string, title = path): PageLink => ({
  text: title,
  path,
  id,
  broken: false,
  target: { id, path, title, untitled: false },
})

describe('a new address for a page link', () => {
  it('drops the id of the old page when nothing is known of the new one', () => {
    const { editor } = linkEditor(`见[暮雪](Tang/暮雪.md "${MUXUE}")。`, [linked('Tang/暮雪.md', MUXUE)])
    setHref(editor, '暮雪', 'Other.md')
    expect(markdownOf(editor).trim()).toBe('见[暮雪](Other.md)。')
  })

  it('takes the id of the page the new address names, when the report knows it', () => {
    const { editor } = linkEditor(`[暮雪](Tang/暮雪.md "${MUXUE}") [别的](Other.md "${OTHER}")`, [
      linked('Tang/暮雪.md', MUXUE),
      linked('Other.md', OTHER),
    ])
    setHref(editor, '暮雪', 'Other.md')
    expect(markdownOf(editor).trim()).toBe(`[暮雪](Other.md "${OTHER}") [别的](Other.md "${OTHER}")`)
  })

  it('keeps the id while the address names the same file, and keeps a title that is not an id', () => {
    const { editor } = linkEditor(`[暮雪](Tang/暮雪.md "${MUXUE}") [网](https://a.example "说明")`, [
      linked('Tang/暮雪.md', MUXUE),
    ])
    setHref(editor, '暮雪', './Tang/暮雪.md#开头')
    setHref(editor, '网', 'https://b.example')
    expect(markdownOf(editor).trim()).toBe(`[暮雪](./Tang/暮雪.md#开头 "${MUXUE}") [网](https://b.example "说明")`)
  })

  it('drops the id when the address leaves the space', () => {
    const { editor } = linkEditor(`[暮雪](Tang/暮雪.md "${MUXUE}")`, undefined)
    setHref(editor, '暮雪', 'https://example.com')
    expect(markdownOf(editor).trim()).toBe('[暮雪](https://example.com)')
  })

  it('a URL pasted over the link (the mark set again with the old title kept) is treated the same', () => {
    const { editor } = linkEditor(`[暮雪](Tang/暮雪.md "${MUXUE}")`, [linked('Tang/暮雪.md', MUXUE)])
    editor.chain().selectAll().setMark('link', { href: 'https://example.com' }).run()
    expect(markdownOf(editor).trim()).toBe('[暮雪](https://example.com)')
  })

  it('undo brings the old address and its id back', () => {
    const { editor } = linkEditor(`[暮雪](Tang/暮雪.md "${MUXUE}")`, [linked('Tang/暮雪.md', MUXUE)])
    setHref(editor, '暮雪', 'Other.md')
    editor.commands.undo()
    expect(markdownOf(editor).trim()).toBe(`[暮雪](Tang/暮雪.md "${MUXUE}")`)
  })

  it('retitle by itself', () => {
    expect(retitle('', undefined, 'a.md', 'a.md', MUXUE)).toBe(MUXUE)
    expect(retitle('Notes', undefined, '../a.md', '/a.md', MUXUE)).toBe(MUXUE)
    expect(retitle('', undefined, 'a.md', 'b.md', 'not an id')).toBe('not an id')
    expect(retitle('', [linked('b.md', OTHER)], 'a.md', 'b.md', MUXUE)).toBe(OTHER)
  })
})

describe('the drawn links', () => {
  const drawn = (editor: Editor) =>
    [...editor.view.dom.querySelectorAll('.pb-page-link')].map(
      (el) => `${el.textContent}${el.classList.contains('pb-page-link-broken') ? '!' : ''}`,
    )

  it('follow the text as it changes, and the report when it changes', async () => {
    const { editor, setLinks } = linkEditor('[甲](a.md) 中间 [乙](b.md)\n\n[丙](c.md)', undefined)
    // The editor hands out `refresh` once it has been created (on the next turn).
    await new Promise((done) => setTimeout(done, 0))
    expect(drawn(editor)).toEqual(['甲', '乙', '丙'])
    setLinks([{ text: '乙', path: 'b.md', broken: true }])
    expect(drawn(editor)).toEqual(['甲', '乙!', '丙'])
    editor.chain().setTextSelection(4).insertContent('多').run()
    expect(drawn(editor)).toEqual(['甲', '乙!', '丙'])
    // A link made by hand is drawn at once; one taken off is not drawn any more.
    let at = -1
    editor.state.doc.descendants((node, pos) => {
      if (at < 0 && node.isText && node.text!.includes('中多间')) at = pos + node.text!.indexOf('中多间')
    })
    editor
      .chain()
      .setTextSelection({ from: at, to: at + 3 })
      .setLink({ href: 'd.md' })
      .run()
    expect(drawn(editor)).toEqual(['甲', '中多间', '乙!', '丙'])
    editor.chain().setTextSelection(2).extendMarkRange('link').unsetLink().run()
    expect(drawn(editor)).toEqual(['中多间', '乙!', '丙'])
    // Joining two paragraphs keeps both links drawn.
    editor
      .chain()
      .setTextSelection(editor.state.doc.child(0).nodeSize + 1)
      .joinBackward()
      .run()
    expect(drawn(editor)).toEqual(['中多间', '乙!', '丙'])
  })

  it('cost little per keystroke in a page with 2000 links', () => {
    const n = 2000
    const lines: string[] = []
    const links: PageLink[] = []
    for (let i = 0; i < n; i++) {
      const id = `01JD3K${String(i).padStart(4, '0')}`
      lines.push(`第${i}段 [页${i}](Tang/页${i}.md "${id}")`)
      links.push(linked(`Tang/页${i}.md`, id, `页${i}`))
    }
    const { editor } = linkEditor(lines.join('\n\n'), links)
    expect(editor.view.dom.querySelectorAll('.pb-page-link')).toHaveLength(n)
    editor.commands.setTextSelection(3)
    const times: number[] = []
    for (let i = 0; i < 40; i++) {
      const t0 = performance.now()
      editor.view.dispatch(editor.state.tr.insertText('字'))
      times.push(performance.now() - t0)
    }
    times.sort((a, b) => a - b)
    const median = times[times.length >> 1]
    // Before the decorations were mapped, each keystroke walked every link and looked each one up in the whole
    // report (tens of ms); now it is well under one. The bound leaves room for a slow runner.
    expect(median).toBeLessThan(10)
    expect(editor.view.dom.querySelectorAll('.pb-page-link')).toHaveLength(n)
  })
})
