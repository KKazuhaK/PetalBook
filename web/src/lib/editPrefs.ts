// The way pages open for editing: rich text, the Markdown source, or both side
// by side. The editor remembers the last one used under this key; Settings
// changes the same value, so "the default" is simply what the editor will start
// with. Kept as a separate small reader so the settings dialog does not load the
// editor's code to show it.

export type DefaultEditMode = 'rich' | 'source' | 'split'
export const EDIT_MODE_KEY = 'pb.editor.mode'
export const EDIT_MODES: readonly DefaultEditMode[] = ['rich', 'source', 'split']

export function getEditMode(): DefaultEditMode {
  try {
    const v = localStorage.getItem(EDIT_MODE_KEY)
    return v === 'source' || v === 'split' ? v : 'rich'
  } catch {
    return 'rich'
  }
}

export function setEditMode(mode: DefaultEditMode) {
  try {
    localStorage.setItem(EDIT_MODE_KEY, mode)
  } catch {
    // Not stored; the choice lasts until the page is left.
  }
}
