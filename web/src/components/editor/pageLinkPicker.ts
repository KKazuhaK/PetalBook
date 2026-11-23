// Typing `[[` (or `【【`, what a Chinese input method writes for it) in a page opens a
// picker for the page to link to. This file finds the trigger and hands the keys to the
// picker; the list and the insertion are PageLinkPicker.tsx's.
//
// Nothing starts while an input method is composing: the text is not final then, and
// Enter belongs to the input method.

import { Extension, type Editor } from '@tiptap/core'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import type { EditorView } from '@tiptap/pm/view'
import { linkDest } from '@/lib/pageLinks'

export interface PickerTrigger {
  // From the first bracket to the cursor: what is replaced by the link.
  from: number
  to: number
  // What was typed after the brackets.
  query: string
}

export interface PickerStorage {
  trigger: PickerTrigger | null
  listeners: Set<() => void>
  // The picker's handler for a key while it is open; true when it used the key.
  onKey: ((e: KeyboardEvent) => boolean) | null
  setKeyHandler: (fn: ((e: KeyboardEvent) => boolean) | null) => void
  // Closes the picker until the brackets are typed again.
  dismiss: () => void
}

const key = new PluginKey('pageLinkPicker')
const TRIGGER = /(?:\[\[|【【)([^[\]【】\n]{0,80})$/

export const PageLinkPicker = Extension.create<Record<string, never>, PickerStorage>({
  name: 'pageLinkPicker',

  addStorage() {
    // (`this`, not a variable: the editor may hand out a copy of the storage.)
    return {
      trigger: null,
      listeners: new Set(),
      onKey: null,
      setKeyHandler(this: PickerStorage, fn: ((e: KeyboardEvent) => boolean) | null) {
        this.onKey = fn
      },
      dismiss: () => {},
    } satisfies PickerStorage
  },

  addProseMirrorPlugins() {
    const storage = this.storage
    // Where the brackets were when the person pressed Esc: not offered again there.
    let dismissedAt = -1
    const set = (next: PickerTrigger | null) => {
      const a = storage.trigger
      if (a === next || (a && next && a.from === next.from && a.to === next.to && a.query === next.query)) return
      storage.trigger = next
      storage.listeners.forEach((l) => l())
    }
    const evaluate = (view: EditorView) => {
      // Mid-composition the text is not settled; it is looked at again when the composition ends.
      if (view.composing) return
      const { state } = view
      const sel = state.selection
      if (!view.editable || !sel.empty) return set(null)
      const $from = sel.$from
      if (!$from.parent.isTextblock || $from.parent.type.spec.code || $from.marks().some((m) => m.type.name === 'code')) return set(null)
      const before = $from.parent.textBetween(0, $from.parentOffset, undefined, '￼')
      const m = TRIGGER.exec(before)
      if (!m) {
        dismissedAt = -1
        return set(null)
      }
      const from = $from.start() + m.index
      if (from === dismissedAt) return set(null)
      set({ from, to: sel.from, query: m[1] })
    }
    storage.dismiss = () => {
      if (storage.trigger) dismissedAt = storage.trigger.from
      set(null)
    }
    return [
      new Plugin({
        key,
        view: (view) => {
          evaluate(view)
          return {
            update: (v) => evaluate(v),
            destroy: () => set(null),
          }
        },
        props: {
          handleKeyDown: (view, e) => {
            if (!storage.trigger || !storage.onKey) return false
            // Keys of an input method (Enter that picks a candidate, arrows in its list) are not ours.
            if (view.composing || e.isComposing || e.keyCode === 229) return false
            return storage.onKey(e)
          },
          handleDOMEvents: {
            compositionend: (view) => {
              // The editor has read the composed text by the next turn.
              setTimeout(() => !view.isDestroyed && evaluate(view), 0)
              return false
            },
            blur: () => {
              set(null)
              return false
            },
          },
        },
      }),
    ]
  },
})

export const pickerOf = (editor: Editor): PickerStorage => (editor.storage as unknown as { pageLinkPicker: PickerStorage }).pageLinkPicker

// Replaces what was typed (`[[` and the query) with a link to the page: the title as the
// text, the file as a path relative to the folder of this page, the id as the link's title.
export function insertPageLink(editor: Editor, at: { from: number; to: number }, fromFolder: string, page: { id: string; path: string; label: string }) {
  const text = page.label
  editor
    .chain()
    .focus()
    .insertContentAt({ from: at.from, to: at.to }, { type: 'text', text, marks: [{ type: 'link', attrs: { href: linkDest(fromFolder, page.path), title: page.id } }] })
    // What is typed next is not part of the link.
    .unsetMark('link', { extendEmptyMarkRange: false })
    .run()
}
