import { useEffect, useRef } from 'react'
import { EditorContent, useEditor } from '@tiptap/react'
import { createExtensions, type DocJSON } from '@/markdown'
import { diffExtensions } from './diffExtension'

// The page's own extensions and typography, read-only, plus what draws changes.
const extensions = [...createExtensions({ linkOpensOnClick: false }), ...diffExtensions]

// A run of changes next to each other (a deletion and the insertion that
// replaces it) is one place to jump to.
export function changeStops(root: HTMLElement): HTMLElement[][] {
  const els = Array.from(root.querySelectorAll<HTMLElement>('[data-diff]'))
  const groups: HTMLElement[][] = []
  for (const el of els) {
    const last = groups[groups.length - 1]
    if (last && last[last.length - 1].nextElementSibling === el) last.push(el)
    else groups.push([el])
  }
  return groups
}

// The merged document of a comparison, shown like the page itself but not
// editable. `doc` is replaced as a whole when the comparison changes; the
// editor itself lives as long as the view.
export function DiffView({ doc, label, onRendered }: { doc: DocJSON; label: string; onRendered: (root: HTMLElement | null) => void }) {
  const root = useRef<HTMLDivElement>(null)
  const shown = useRef<DocJSON>(doc)
  const editor = useEditor({
    extensions,
    content: doc,
    editable: false,
    editorProps: { attributes: { class: 'pb-prose pb-diff', role: 'document', 'aria-label': label, 'aria-readonly': 'true' } },
  })

  useEffect(() => {
    if (!editor || editor.isDestroyed) return
    if (shown.current !== doc) {
      shown.current = doc
      editor.commands.setContent(doc, { emitUpdate: false })
    }
    onRendered(root.current)
  }, [editor, doc, onRendered])

  return <div ref={root}>{editor && <EditorContent editor={editor} />}</div>
}
