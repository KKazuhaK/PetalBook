// Inserting a footnote: a reference at the cursor and its definition at the end
// of the page, where Markdown keeps them. The cursor moves to the definition to
// write the note, as in Google Docs.

import type { Editor } from '@tiptap/core'
import type { EditorState } from '@tiptap/pm/state'
import { TextSelection } from '@tiptap/pm/state'

// The smallest positive whole number no footnote uses yet. Names that are not
// numbers ("note") are never reused or renumbered.
export function nextFootnoteId(used: Iterable<string>): string {
  const taken = new Set(used)
  let n = 1
  while (taken.has(String(n))) n++
  return String(n)
}

// A reference needs a line of text to sit in, which is not code.
export function canInsertFootnote(state: EditorState): boolean {
  const { $to } = state.selection
  return (
    $to.parent.inlineContent &&
    !$to.parent.type.spec.code &&
    Boolean($to.parent.type.contentMatch.matchType(state.schema.nodes.footnoteRef))
  )
}

export function insertFootnote(editor: Editor): boolean {
  const { state } = editor
  if (!canInsertFootnote(state)) return false
  const used = new Set<string>()
  state.doc.descendants((node) => {
    if (node.type.name === 'footnoteRef' || node.type.name === 'footnoteDef') used.add(String(node.attrs.id))
    return true
  })
  const id = nextFootnoteId(used)
  const { footnoteRef, footnoteDef, paragraph } = state.schema.nodes
  const tr = state.tr.insert(state.selection.to, footnoteRef.create({ id }))
  const def = footnoteDef.create({ id }, paragraph.create())
  const at = tr.doc.content.size
  tr.insert(at, def)
  // Inside the definition's paragraph.
  tr.setSelection(TextSelection.create(tr.doc, at + 2)).scrollIntoView()
  editor.view.dispatch(tr)
  editor.view.focus()
  return true
}
