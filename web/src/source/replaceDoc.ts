// Make the editor show another document by replacing only the blocks that
// differ. Unlike setting the whole content, positions in the unchanged parts
// stay where they are (the cursor, a place an upload is waiting to land), the
// step is not something Undo steps back over, and no update is reported: the
// text came from the source view, which already is the content.

import type { Editor } from '@tiptap/core'
import type { Node as PMNode } from '@tiptap/pm/model'
import type { DocJSON } from '@/markdown'

export function replaceDoc(editor: Editor, json: DocJSON): void {
  const { state, view } = editor
  let next: PMNode
  try {
    next = state.schema.nodeFromJSON(json)
    next.check()
  } catch {
    // Not a document this editor can hold as it is: let the editor repair it.
    editor.chain().setMeta('addToHistory', false).setContent(json, { emitUpdate: false }).run()
    return
  }
  const a = state.doc
  const max = Math.min(a.childCount, next.childCount)
  let start = 0
  while (start < max && a.child(start).eq(next.child(start))) start++
  let endA = a.childCount
  let endB = next.childCount
  while (endA > start && endB > start && a.child(endA - 1).eq(next.child(endB - 1))) {
    endA--
    endB--
  }
  if (start === endA && start === endB) return
  let from = 0
  for (let i = 0; i < start; i++) from += a.child(i).nodeSize
  let to = from
  for (let i = start; i < endA; i++) to += a.child(i).nodeSize
  const nodes: PMNode[] = []
  for (let i = start; i < endB; i++) nodes.push(next.child(i))
  view.dispatch(state.tr.replaceWith(from, to, nodes).setMeta('addToHistory', false).setMeta('preventUpdate', true))
}
