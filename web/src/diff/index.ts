// Comparing two versions of a page. No React, no network: two versions in
// (header and Markdown body), a merged document and a list of header changes
// out. See docs/design.md 8.7 for what it is for.

import { markdownToDoc } from '@/markdown'
import { diffBlocks } from './blocks'
import { blocksOf, type Node } from './doc'
import { diffHeader, type HeaderChange, type Meta } from './header'
import { countChanges, type DiffStats } from './stats'
import { diffText } from './inline'

export { diffLines, type LineDiff, type LineOp } from './lines'
export { projectDoc } from './project'
export { diffHeader, type HeaderChange, type Meta } from './header'
export type { DiffStats } from './stats'
export type { TextPart } from './inline'
export { DEL, FMT, INS } from './doc'

export interface VersionContent {
  meta: Meta
  body: string
}

export interface DiffResult {
  header: HeaderChange[]
  // The merged document: new content, deleted content still in place, each
  // marked (see doc.ts). Read it with `projectDoc`.
  doc: Node
  stats: DiffStats
  // Nothing differs: no header change and no mark in the document.
  identical: boolean
}

const EMPTY: Node = { type: 'doc', content: [{ type: 'paragraph' }] }

// Two documents (already parsed).
export function diffDocs(a: Node, b: Node): { doc: Node; stats: DiffStats } {
  const content = diffBlocks(blocksOf(a), blocksOf(b))
  const doc: Node = { type: 'doc', content: content.length ? content : [{ type: 'paragraph' }] }
  return { doc, stats: countChanges(doc) }
}

// `null` stands for a version with nothing in it (before the first one).
export function diffVersions(older: VersionContent | null, newer: VersionContent | null): DiffResult {
  const a = older ? markdownToDoc(older.body) : EMPTY
  const b = newer ? markdownToDoc(newer.body) : EMPTY
  const header = diffHeader(older?.meta ?? {}, newer?.meta ?? {})
  const { doc, stats } = diffDocs(a, b)
  for (const h of header) {
    stats.changes++
    if (h.kind === 'text') {
      for (const p of h.parts) {
        const n = Array.from(p.text).length
        if (p.type === 'ins') stats.inserted += n
        else if (p.type === 'del') stats.deleted += n
      }
    }
  }
  return { header, doc, stats, identical: header.length === 0 && stats.changes === 0 }
}

// Re-exported for the header lines the view shows.
export { diffText }
