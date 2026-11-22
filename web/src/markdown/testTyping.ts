// Test helpers: a real Tiptap editor under jsdom and typing into it the way the
// browser does, so input rules run through the same plugin props.

import { Editor } from '@tiptap/core'
import type { Extensions } from '@tiptap/core'
import { extensions as markdownExtensions, type DocJSON } from './index'
import { trackEditor } from '@/testing/editors'

export function makeEditor(content?: DocJSON, extensions: Extensions = markdownExtensions): Editor {
  return trackEditor(new Editor({ element: document.createElement('div'), extensions, ...(content ? { content } : {}) }))
}

// One character at a time: input rules only see what is typed, never a paste.
export function typeText(editor: Editor, text: string) {
  const { view } = editor
  for (const ch of Array.from(text)) {
    const { from, to } = view.state.selection
    const handled = view.someProp('handleTextInput', (f) => f(view, from, to, ch, () => view.state.tr.insertText(ch, from, to)))
    if (!handled) view.dispatch(view.state.tr.insertText(ch, from, to))
  }
}

export interface Run {
  text: string
  marks: string[]
}
// The inline content of the nth top-level block as runs of text with their mark names.
export function runsOf(editor: Editor, block = 0): (Run | { node: string })[] {
  const content = (editor.getJSON().content?.[block]?.content ?? []) as { type?: string; text?: string; marks?: { type: string }[] }[]
  return content.map((n) => (n.type === 'text' ? { text: n.text ?? '', marks: (n.marks ?? []).map((m) => m.type) } : { node: n.type ?? '' }))
}

// A paste event as the browser delivers it. jsdom has no ClipboardEvent, so the
// event carries the one property ProseMirror reads.
export function paste(editor: Editor, data: { text?: string; html?: string; files?: File[]; shift?: boolean }) {
  const { view } = editor
  if (data.shift) view.dom.dispatchEvent(new KeyboardEvent('keydown', { key: 'Shift', keyCode: 16, shiftKey: true, bubbles: true }))
  const event = new Event('paste', { bubbles: true, cancelable: true })
  const types: Record<string, string> = { 'text/plain': data.text ?? '', 'text/html': data.html ?? '' }
  const files = data.files ?? []
  Object.defineProperty(event, 'clipboardData', {
    value: {
      getData: (type: string) => types[type] ?? '',
      files,
      items: files.map((f) => ({ kind: 'file', type: f.type, getAsFile: () => f })),
      types: [...(data.text !== undefined ? ['text/plain'] : []), ...(data.html !== undefined ? ['text/html'] : []), ...(files.length ? ['Files'] : [])],
    },
  })
  view.dom.dispatchEvent(event)
  if (data.shift) view.dom.dispatchEvent(new KeyboardEvent('keyup', { key: 'Shift', keyCode: 16, bubbles: true }))
  return event
}
