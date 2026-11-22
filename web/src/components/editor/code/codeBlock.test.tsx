import { act } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { t } from '@/i18n'
import { setEditableQuietly } from '../editable'
import { cleanup, markdownOf, mount, pageEditor } from '../pageTestKit'
import { loadHighlighter } from './highlighter'
import { MAX_HIGHLIGHTED_CHARS } from './highlightPlugin'

const fence = (lang: string, code = 'x') => '```' + lang + '\n' + code + '\n```\n'

describe('the language of a code block', () => {
  it('is set on the block the cursor is in, and written after the fence', () => {
    const editor = pageEditor('```\nlet a = 1\n```\n')
    editor.commands.setTextSelection(3)
    expect(editor.commands.setCodeBlockLanguage('typescript')).toBe(true)
    expect(markdownOf(editor)).toBe(fence('typescript', 'let a = 1').trimEnd() + '\n')
    editor.commands.setCodeBlockLanguage(null)
    expect(markdownOf(editor)).toBe(fence('', 'let a = 1'))
  })

  it('keeps what follows the language in the info string', () => {
    const editor = pageEditor(fence('js title=a.js', 'a'))
    editor.commands.setTextSelection(3)
    editor.commands.setCodeBlockLanguage('python')
    expect(markdownOf(editor)).toBe(fence('python title=a.js', 'a'))
  })

  it('does nothing outside a code block', () => {
    const editor = pageEditor('正文\n')
    expect(editor.commands.setCodeBlockLanguage('go')).toBe(false)
    expect(markdownOf(editor)).toBe('正文\n')
  })

  it('does not change what a page with code blocks is saved as', () => {
    const source = fence('js', 'const a = 1') + '\n' + fence('', 'plain') + '\n' + fence('klingon', 'qapla')
    expect(markdownOf(pageEditor(source))).toBe(source)
  })
})

describe('the language picker', () => {
  it('follows whether the editor can be typed in, both ways', async () => {
    const editor = pageEditor(fence('js', 'a'))
    const host = await mount(editor)
    let updates = 0
    editor.on('update', () => updates++)
    const picker = () => host.host.querySelector(`[aria-label^="${t.code.language}:"]`) !== null
    expect(picker()).toBe(true)
    await act(async () => setEditableQuietly(editor, false))
    expect(picker()).toBe(false)
    await act(async () => setEditableQuietly(editor, true))
    expect(picker()).toBe(true)
    // Switching is not typing.
    expect(updates).toBe(0)
    await host.unmount()
  })
})

describe('syntax colours', () => {
  const colours = async (source: string) => {
    await loadHighlighter()
    const editor = pageEditor(source)
    const host = await mount(editor)
    const classes = Array.from(host.host.querySelectorAll('[class*="hljs-"]')).map((e) => e.className)
    await host.unmount()
    return { classes, editor }
  }

  it('are drawn for a known language once the highlighter is loaded', async () => {
    const { classes } = await colours(fence('js', 'const a = "x"'))
    expect(classes.some((c) => c.includes('hljs-keyword'))).toBe(true)
    expect(classes.some((c) => c.includes('hljs-string'))).toBe(true)
  })

  it('are not drawn for plain text, an unknown language, or a diagram, and nothing fails', async () => {
    for (const lang of ['', 'klingon', 'mermaid']) {
      const { classes } = await colours(fence(lang, 'const a = "x"'))
      expect(classes, lang).toEqual([])
    }
  })

  it('follow the language when it is changed, and an edit of the code', async () => {
    await loadHighlighter()
    const editor = pageEditor(fence('js', 'const a = 1'))
    const host = await mount(editor)
    const kinds = () => Array.from(host.host.querySelectorAll('[class*="hljs-"]')).length
    expect(kinds()).toBeGreaterThan(0)
    editor.commands.setTextSelection(3)
    editor.commands.setCodeBlockLanguage(null)
    expect(kinds()).toBe(0)
    editor.commands.setCodeBlockLanguage('python')
    expect(kinds()).toBeGreaterThan(0)
    await host.unmount()
  })

  it('are not drawn for a block too long to be re-coloured on every key', async () => {
    const { classes } = await colours(fence('js', 'const a = 1\n'.repeat(Math.ceil(MAX_HIGHLIGHTED_CHARS / 10))))
    expect(classes).toEqual([])
  })
})

afterEach(cleanup)
