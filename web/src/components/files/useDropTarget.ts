import { useRef, useState, type DragEvent } from 'react'
import { useSpacePermissions } from '@/api/hooks'
import { draggedItems, endDrag, isOurDrag } from './dnd'
import { planDrop, type FileItem, type FolderIndex } from '@/lib/folders'

// How long a dragged item rests on a collapsed folder before it opens.
const OPEN_AFTER_MS = 700

// Makes an element a place to drop files and folders onto the folder `path`.
// It lights up only while what is being dragged could really go there, and
// does not accept anything else, so the pointer shows "not allowed".
export function useDropTarget(path: string, index: FolderIndex | undefined, onDrop: (items: FileItem[], target: string) => void, onHover?: () => void) {
  const [over, setOver] = useState(false)
  const { write } = useSpacePermissions()
  // dragenter/dragleave fire for every child element crossed; count them.
  const depth = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const accepts = (e: DragEvent) => {
    const items = draggedItems()
    return write && isOurDrag(e.dataTransfer) && items !== null && planDrop(items, path, index).ok
  }
  const reset = () => {
    depth.current = 0
    setOver(false)
    clearTimeout(timer.current)
  }

  return {
    over,
    props: {
      onDragEnter: (e: DragEvent) => {
        if (!accepts(e)) return
        e.preventDefault()
        depth.current += 1
        setOver(true)
        if (onHover && depth.current === 1) timer.current = setTimeout(onHover, OPEN_AFTER_MS)
      },
      onDragOver: (e: DragEvent) => {
        if (!accepts(e)) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      },
      onDragLeave: (e: DragEvent) => {
        if (!isOurDrag(e.dataTransfer)) return
        depth.current = Math.max(0, depth.current - 1)
        if (depth.current === 0) reset()
      },
      onDrop: (e: DragEvent) => {
        const items = draggedItems()
        const ok = accepts(e)
        reset()
        if (!ok || !items) return
        e.preventDefault()
        e.stopPropagation()
        endDrag()
        onDrop(items, path)
      },
    },
  }
}
