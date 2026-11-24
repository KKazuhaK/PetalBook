import { describe, expect, it } from 'vitest'
import { currentHighlighter } from './highlighter'
import { mount, pageEditor } from '../pageTestKit'

// Its own file: the highlighter is loaded once per module registry, and this
// is about the time before it is.
describe('loading the highlighter', () => {
  const colours = (host: HTMLElement) => host.querySelectorAll('[class*="hljs-"]').length

  it('is not started by a page without code, or with code it cannot colour', async () => {
    for (const source of [
      '只有正文\n',
      '```\nplain\n```\n',
      '```klingon\nqapla\n```\n',
      '```mermaid\ngraph TD\nA-->B\n```\n',
    ]) {
      const host = await mount(pageEditor(source))
      await new Promise((r) => setTimeout(r, 30))
      expect(currentHighlighter(), source).toBeNull()
      await host.unmount()
    }
  })

  it('starts when a block in a known language is there, and paints it', async () => {
    const host = await mount(pageEditor('```js\nconst a = 1\n```\n'))
    await expect.poll(() => colours(host.host), { timeout: 5000 }).toBeGreaterThan(0)
    expect(currentHighlighter()).not.toBeNull()
    await host.unmount()
  })

  it('paints a block in a known language that arrives after the page was opened', async () => {
    const source = '正文\n'
    const editor = pageEditor(source)
    const host = await mount(editor)
    editor.commands.setContent({
      type: 'doc',
      content: [
        { type: 'codeBlock', attrs: { language: 'python' }, content: [{ type: 'text', text: 'def f(): pass' }] },
      ],
    })
    await expect.poll(() => colours(host.host), { timeout: 5000 }).toBeGreaterThan(0)
    await host.unmount()
  })
})
