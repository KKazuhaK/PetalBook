// Editors made by test helpers, so they can all be destroyed when a test file
// is done. An editor left alive keeps ProseMirror's timers: one that fires
// after the file's jsdom is gone reads `document` and fails the whole run
// ("document is not defined"), although every test passed. A destroyed editor's
// timers find no view and do nothing.

import type { Editor } from '@tiptap/core'

const live = new Set<Editor>()

export function trackEditor<T extends Editor>(editor: T): T {
  live.add(editor)
  return editor
}

export function destroyEditors() {
  for (const editor of live) {
    if (!editor.isDestroyed) editor.destroy()
  }
  live.clear()
}
