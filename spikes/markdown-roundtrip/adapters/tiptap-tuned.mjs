// Tiptap 3 with the official @tiptap/markdown extension plus the CJK-friendly
// plugin for its parser (marked). Driven through a real
// Editor instance (under jsdom) so the document passes through the ProseMirror
// schema exactly as it would in the browser.

import { JSDOM } from 'jsdom'

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>')
for (const key of ['window', 'document', 'navigator', 'Node', 'Element', 'HTMLElement', 'DOMParser', 'MutationObserver', 'getComputedStyle']) {
  if (!(key in globalThis) || key === 'document' || key === 'window') {
    Object.defineProperty(globalThis, key, { value: key === 'window' ? dom.window : dom.window[key], configurable: true, writable: true })
  }
}
globalThis.requestAnimationFrame ??= (cb) => setTimeout(cb, 0)
globalThis.cancelAnimationFrame ??= (id) => clearTimeout(id)

const { Editor } = await import('@tiptap/core')
const { default: StarterKit } = await import('@tiptap/starter-kit')
const { Markdown } = await import('@tiptap/markdown')
const { TableKit } = await import('@tiptap/extension-table')
const { TaskList, TaskItem } = await import('@tiptap/extension-list')
const { default: Image } = await import('@tiptap/extension-image')

const { Marked } = await import('marked')
const { default: markedCjkFriendly } = await import('marked-cjk-friendly')
const marked = new Marked()
marked.use(markedCjkFriendly())

export const label = 'Tiptap 3.35 + @tiptap/markdown + marked-cjk-friendly'

const editor = new Editor({
  element: dom.window.document.getElementById('root'),
  extensions: [
    StarterKit,
    TableKit,
    TaskList,
    TaskItem.configure({ nested: true }),
    Image.configure({ inline: true }),
    Markdown.configure({ marked }),
  ],
})

export async function roundtrip(markdown) {
  editor.commands.setContent(markdown, { contentType: 'markdown' })
  return editor.getMarkdown()
}

// Document-first helpers: what the editor holds -> markdown -> what it loads.
export function canonical(json) {
  editor.commands.setContent(json)
  return editor.getJSON()
}
export function toMarkdown(json) {
  editor.commands.setContent(json)
  return editor.getMarkdown()
}
export function fromMarkdown(markdown) {
  editor.commands.setContent(markdown, { contentType: 'markdown' })
  return editor.getJSON()
}
