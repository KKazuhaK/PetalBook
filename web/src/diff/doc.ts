// Helpers over editor documents (ProseMirror JSON) and the names the
// difference is written in.

import type { DocJSON } from '@/markdown'

export type Node = DocJSON
type Mark = NonNullable<Node['marks']>[number]

// Marks on inline content.
export const INS = 'diffIns'
export const DEL = 'diffDel'
// Same text, other formatting. `attrs.old` is the old marks as JSON.
export const FMT = 'diffFmt'

// Attributes on blocks. `diff` is `ins`, `del` or `fmt`; `diffOld` is the
// block's old type and attributes (as JSON) when they differ from what it has
// now; `diffMove` marks a block that only moved.
export type BlockDiff = 'ins' | 'del' | 'fmt'

export const isDiffMark = (m: Mark) => m.type === INS || m.type === DEL || m.type === FMT

// A stable text form of a mark set, whatever order the marks came in.
export function marksKey(marks: readonly Mark[] | undefined): string {
  if (!marks || marks.length === 0) return ''
  return marks
    .map((m) => m.type + (m.attrs ? JSON.stringify(sortKeys(m.attrs)) : ''))
    .sort()
    .join('|')
}

function sortKeys(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sortKeys)
  if (v && typeof v === 'object') {
    const o = v as Record<string, unknown>
    return Object.fromEntries(Object.keys(o).sort().map((k) => [k, sortKeys(o[k])]))
  }
  return v
}

const canonCache = new WeakMap<object, string>()
// What two blocks must share to count as the same block: type, attributes,
// marks and content. Marks are compared as sets.
export function canon(node: Node): string {
  const hit = canonCache.get(node)
  if (hit !== undefined) return hit
  let s: string
  if (node.type === 'text') s = `"${node.text}"${marksKey(node.marks)}`
  else {
    const attrs = node.attrs ? JSON.stringify(sortKeys(node.attrs)) : ''
    const marks = marksKey(node.marks)
    const kids = node.content ? node.content.map(canon).join(',') : ''
    s = `${node.type}${attrs}${marks ? `^${marks}` : ''}(${kids})`
  }
  canonCache.set(node, s)
  return s
}

const textCache = new WeakMap<object, string>()
// The text a block holds. A line break reads as a newline and any other
// non-text inline (an image, raw HTML, a footnote mark) as one object
// character, so such a block is never mistaken for an empty one.
export function plainText(node: Node): string {
  if (node.type === 'text') return node.text ?? ''
  const hit = textCache.get(node)
  if (hit !== undefined) return hit
  let s: string
  if (node.type === 'hardBreak') s = '\n'
  else if (node.type === 'image' || node.type === 'rawInline' || node.type === 'footnoteRef') s = '￼'
  // Inline content runs together; blocks inside a block are told apart by a space.
  else s = (node.content ?? []).map(plainText).join(node.type === 'paragraph' || node.type === 'heading' || node.type === 'codeBlock' ? '' : ' ')
  textCache.set(node, s)
  return s
}

// Text with no object characters: what people read.
export const readableText = (node: Node) => plainText(node).replace(/￼/g, '')

export function withMarks(node: Node, marks: Mark[]): Node {
  const { marks: _old, ...rest } = node
  void _old
  return marks.length ? { ...rest, marks } : rest
}

export function setDiff(node: Node, diff: BlockDiff | null, extra: { old?: { type: string; attrs?: Record<string, unknown> }; move?: boolean } = {}): Node {
  const attrs: Record<string, unknown> = { ...node.attrs }
  if (diff) attrs.diff = diff
  else delete attrs.diff
  if (extra.old) attrs.diffOld = JSON.stringify({ type: extra.old.type, attrs: extra.old.attrs ?? {} })
  if (extra.move) attrs.diffMove = true
  return { ...node, attrs }
}

// An empty document is one empty paragraph in the editor; for a comparison it
// is no blocks at all.
export function blocksOf(doc: Node): Node[] {
  const c = doc.content ?? []
  if (c.length === 1 && c[0].type === 'paragraph' && !(c[0].content?.length)) return []
  return c
}
