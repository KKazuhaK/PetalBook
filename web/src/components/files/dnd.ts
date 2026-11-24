// Dragging files and folders with the browser's own drag and drop.
//
// What is being dragged is kept here, not only in the DataTransfer: a drop
// target has to decide during `dragover` whether it accepts, and the browser
// hides the data until the drop. One drag at a time exists, so a module
// variable is enough.

import type { FileItem } from '@/lib/folders'

const MIME = 'application/x-petalbook-items'

let current: FileItem[] | null = null

export const draggedItems = (): FileItem[] | null => current

export function startDrag(e: { dataTransfer: DataTransfer | null }, items: FileItem[]): void {
  current = items
  const dt = e.dataTransfer
  if (!dt) return
  dt.effectAllowed = 'move'
  // Firefox does not start a drag without some data; the custom type keeps
  // other pages and the text fields of this one from taking it as text.
  dt.setData(MIME, String(items.length))
}

// Also called on `drop`'s counterpart `dragend`, which fires wherever the drag ends.
export function endDrag(): void {
  current = null
}

// Whether something of ours is being dragged over an element.
export const isOurDrag = (dt: DataTransfer | null): boolean =>
  current !== null && !!dt && Array.from(dt.types).includes(MIME)
