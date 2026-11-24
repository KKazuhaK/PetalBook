import { useEffect, useRef, useState } from 'react'
import { EditorContent, useEditor } from '@tiptap/react'
import { createExtensions, type DocJSON } from '@/markdown'
import { addressToUrl } from '@/lib/fileAddress'
import { CodeBlockWithView } from '@/components/editor/code/codeBlock'
import { diffExtensions } from './diffExtension'

// The page's own extensions and typography, read-only, plus what draws changes.
// Images are drawn from the same mapped addresses as in the editor (see
// src/lib/fileAddress.ts), not from the raw address the page keeps.
class Where {
  space: string
  folder: string
  constructor(space: string, folder: string) {
    this.space = space
    this.folder = folder
  }
  set(space: string, folder: string) {
    this.space = space
    this.folder = folder
  }
}
const pageExtensions = (where: Where) => [
  ...createExtensions({
    linkOpensOnClick: false,
    resolveImageSrc: (src) => addressToUrl(where.space, where.folder, src),
    codeBlock: CodeBlockWithView,
  }),
  ...diffExtensions,
]

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
export function DiffView({
  doc,
  label,
  onRendered,
  space = '',
  folder = '',
}: {
  doc: DocJSON
  label: string
  onRendered: (root: HTMLElement | null) => void
  space?: string
  folder?: string
}) {
  // Read when a picture is drawn, so the one editor of the view follows the page.
  const [where] = useState(() => new Where(space, folder))
  useEffect(() => where.set(space, folder), [where, space, folder])
  const [extensions] = useState(() => pageExtensions(where))
  const root = useRef<HTMLDivElement>(null)
  const shown = useRef<DocJSON>(doc)
  const editor = useEditor({
    extensions,
    content: doc,
    editable: false,
    editorProps: {
      attributes: { class: 'pb-prose pb-diff', role: 'document', 'aria-label': label, 'aria-readonly': 'true' },
    },
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
