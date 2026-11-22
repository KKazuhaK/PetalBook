import type { Editor } from '@tiptap/react'

// Switches the editor between typing and reading while it stays open (the person's
// permission changed). It is not an edit: no update event is sent (that would count
// as typing and mark the page unsaved), but a transaction is, so what shows the
// state (the code blocks' language picker) reads it again.
export function setEditableQuietly(editor: Editor, editable: boolean) {
  if (editor.isDestroyed || editor.isEditable === editable) return
  editor.setEditable(editable, false)
  editor.view.dispatch(editor.state.tr.setMeta('addToHistory', false))
}
