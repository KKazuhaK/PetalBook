// Tiptap as the editor with our own Markdown layer (lib/markdown.mjs), run
// through a real Editor under jsdom. The official @tiptap/markdown extension
// is not used here.

import { JSDOM } from 'jsdom'

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>')
for (const key of ['window', 'document', 'navigator', 'Node', 'Element', 'HTMLElement', 'DOMParser', 'MutationObserver', 'getComputedStyle']) {
  Object.defineProperty(globalThis, key, { value: key === 'window' ? dom.window : dom.window[key], configurable: true, writable: true })
}
globalThis.requestAnimationFrame ??= (cb) => setTimeout(cb, 0)
globalThis.cancelAnimationFrame ??= (id) => clearTimeout(id)

const { Editor } = await import('@tiptap/core')
const { extensions, docToMarkdown, markdownToDoc, normalizeDoc } = await import('../lib/markdown.mjs')

export const label = 'Tiptap 3.35 editor + remark Markdown layer'

// Document-first cases that are expected not to come back identical, and why.
// A failure outside this list fails the test run.
export const knownDifferences = {
  'mark/粗体尾部带空格': 'the space moves outside the bold run; Markdown cannot close emphasis after a space',
  'mark/粗体头部带空格': 'the space moves outside the bold run; Markdown cannot open emphasis before a space',
  'lit/网址': 'a bare URL in text is read back as a link',
}

const editor = new Editor({ element: dom.window.document.getElementById('root'), extensions })

export function canonical(json) {
  editor.commands.setContent(json)
  return editor.getJSON()
}
// What the document is equal to once invisible differences are dropped.
export function normalize(json) {
  return canonical(normalizeDoc(canonical(json)))
}
export function toMarkdown(json) {
  return docToMarkdown(canonical(json))
}
export function fromMarkdown(markdown) {
  return canonical(markdownToDoc(markdown))
}
export async function roundtrip(markdown) {
  return toMarkdown(fromMarkdown(markdown))
}
