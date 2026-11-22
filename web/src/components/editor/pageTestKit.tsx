// Test helpers for the page editor's own extensions: a real editor under jsdom
// with the code block, the table rules and the Markdown layer, and the same
// mounted through React so node views and menus are built.

import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { Editor } from '@tiptap/core'
import { EditorContent } from '@tiptap/react'
import { createExtensions, docToMarkdown, markdownToDoc, type DocJSON } from '@/markdown'
import { CodeBlockWithView } from './code/codeBlock'
import { TableTools } from './table/TableTools'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// jsdom has no layout; ProseMirror asks for rectangles when it scrolls the cursor into view.
if (!Range.prototype.getClientRects) {
  Range.prototype.getClientRects = () => [] as unknown as DOMRectList
  Range.prototype.getBoundingClientRect = () => new DOMRect()
}

export const pageExtensions = () => [...createExtensions({ linkOpensOnClick: false, codeBlock: CodeBlockWithView }), TableTools]

export function pageEditor(markdown = ''): Editor {
  return new Editor({ element: document.createElement('div'), extensions: pageExtensions(), content: markdown ? markdownToDoc(markdown) : undefined })
}

export const markdownOf = (editor: Editor) => docToMarkdown(editor.getJSON() as DocJSON)

// Puts the cursor inside the text that starts with `text`.
export function cursorIn(editor: Editor, text: string): void {
  let found = -1
  editor.state.doc.descendants((node, pos) => {
    if (found < 0 && node.isText && node.text!.includes(text)) found = pos + node.text!.indexOf(text)
  })
  if (found < 0) throw new Error(`no text "${text}"`)
  editor.commands.setTextSelection(found + 1)
}

const mounted = new Set<() => Promise<void>>()

// Takes down everything `mount` put up; for afterEach, so a failed test leaves nothing behind.
export async function cleanup() {
  for (const unmount of [...mounted]) await unmount()
}

// Mounts the editor's content, with whatever else `extra` draws, and tidies up.
export async function mount(editor: Editor, extra?: React.ReactNode) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  await act(async () =>
    root.render(
      <>
        <EditorContent editor={editor} />
        {extra}
      </>,
    ),
  )
  const unmount = async () => {
    if (!mounted.delete(unmount)) return
    await act(async () => root.unmount())
    host.remove()
  }
  mounted.add(unmount)
  return { host, unmount }
}

// A click as the browser delivers it to a menu trigger.
export async function click(el: Element) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

// Lets React show what the editor's last change did.
export const settle = () => act(async () => void (await Promise.resolve()))
