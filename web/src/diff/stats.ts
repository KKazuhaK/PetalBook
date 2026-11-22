// Counting what a merged document holds: characters inserted and deleted,
// characters whose formatting changed, and how many separate changes there
// are (a deletion next to the insertion that replaces it is one).

import { DEL, FMT, INS, plainText, type Node } from './doc'
import { graphemeCount } from './segment'

export interface DiffStats {
  inserted: number
  deleted: number
  formatted: number
  // Separate places that changed, in document order.
  changes: number
}

const size = (n: Node) => graphemeCount(plainText(n))

function inlineKind(n: Node): 'ins' | 'del' | 'fmt' | null {
  for (const m of n.marks ?? []) {
    if (m.type === INS) return 'ins'
    if (m.type === DEL) return 'del'
    if (m.type === FMT) return 'fmt'
  }
  return null
}

export function countChanges(doc: Node): DiffStats {
  const s: DiffStats = { inserted: 0, deleted: 0, formatted: 0, changes: 0 }
  const walk = (nodes: readonly Node[]) => {
    let open = false
    for (const n of nodes) {
      const block = n.attrs?.diff as 'ins' | 'del' | 'fmt' | undefined
      const kind = n.type === 'text' || !n.attrs?.diff ? inlineKind(n) : block
      if (kind) {
        if (!open) s.changes++
        open = true
        if (kind === 'ins') s.inserted += size(n)
        else if (kind === 'del') s.deleted += size(n)
        else s.formatted += n.type === 'text' ? size(n) : 0
      } else open = false
      // A wholly inserted or deleted block has nothing marked inside it.
      if (n.content && kind !== 'ins' && kind !== 'del') walk(n.content)
    }
  }
  walk(doc.content ?? [])
  return s
}
