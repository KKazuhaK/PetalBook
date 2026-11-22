// A real Tiptap editor under jsdom, used only by tests: documents are pushed
// through the schema so defaults (attrs, ordering) are filled in before they
// are compared, exactly as the page does when it loads content.

import { Editor } from '@tiptap/core'
import { extensions, docToMarkdown, markdownToDoc, normalizeDoc, type DocJSON } from './index'
import { trackEditor } from '@/testing/editors'

let editor: Editor | null = null
const get = () => {
  // A file's editor is destroyed when the file is done (testing/setup.ts).
  if (editor?.isDestroyed) editor = null
  return (editor ??= trackEditor(new Editor({ element: document.createElement('div'), extensions })))
}

export function canonical(json: DocJSON): DocJSON {
  get().commands.setContent(json)
  return get().getJSON()
}
// What the document is equal to once invisible differences are dropped.
export function normalize(json: DocJSON): DocJSON {
  return canonical(normalizeDoc(canonical(json)))
}
export function toMarkdown(json: DocJSON): string {
  return docToMarkdown(canonical(json))
}
export function fromMarkdown(markdown: string): DocJSON {
  return canonical(markdownToDoc(markdown))
}
export const roundtrip = (markdown: string) => toMarkdown(fromMarkdown(markdown))

// Document-first cases that are expected not to come back identical, and why.
// A failure outside this list fails the test run, and so does a case in this
// list that now passes.
export const knownDifferences: Record<string, string> = {
  'mark/粗体尾部带空格': 'the space moves outside the bold run; Markdown cannot close emphasis after a space',
  'mark/粗体头部带空格': 'the space moves outside the bold run; Markdown cannot open emphasis before a space',
  'lit/网址': 'a bare URL in text is read back as a link',
}
