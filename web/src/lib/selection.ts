// Which items of a listing are selected. Pure, so the rules (checkbox, shift
// for a range, ⌘ or Ctrl to toggle) are tested without a page.

export interface Selection {
  keys: Set<string>
  // The item a shift-click measures its range from.
  anchor: string | null
}

export const emptySelection = (): Selection => ({ keys: new Set(), anchor: null })

// `order` is every selectable key as shown, top to bottom. A plain click on a
// checkbox and ⌘/Ctrl-click both toggle one item; shift-click selects the
// run between the anchor and the item (the earlier selection is replaced, as
// in a file manager). With no usable anchor it selects just that item.
export function applyClick(order: string[], sel: Selection, key: string, mods: { shift?: boolean }): Selection {
  if (mods.shift && sel.anchor !== null) {
    const a = order.indexOf(sel.anchor)
    const b = order.indexOf(key)
    if (a >= 0 && b >= 0) return { keys: new Set(order.slice(Math.min(a, b), Math.max(a, b) + 1)), anchor: sel.anchor }
  }
  const keys = new Set(sel.keys)
  if (keys.has(key)) keys.delete(key)
  else keys.add(key)
  return { keys, anchor: key }
}

// What is selected of what is still shown: a page that moved away or a folder
// that was deleted drops out by itself.
export function prune(sel: Selection, order: string[]): Selection {
  if (sel.keys.size === 0) return sel
  const shown = new Set(order)
  const keys = new Set([...sel.keys].filter((k) => shown.has(k)))
  if (keys.size === sel.keys.size) return sel
  return { keys, anchor: sel.anchor !== null && shown.has(sel.anchor) ? sel.anchor : null }
}
