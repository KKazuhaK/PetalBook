// Reading one side of a merged document back: what the old version said, or
// what the new one says. The comparison is correct exactly when both come out
// equal to the documents it was made from, which is how it is tested.

import { DEL, FMT, INS, isDiffMark, type Node } from './doc'

type Side = 'old' | 'new'
type Mark = NonNullable<Node['marks']>[number]

function projectInline(n: Node, side: Side): Node | null {
  const marks = n.marks ?? []
  const has = (t: string) => marks.some((m) => m.type === t)
  if (side === 'new' && has(DEL)) return null
  if (side === 'old' && has(INS)) return null
  let rest: Mark[] = marks.filter((m) => !isDiffMark(m))
  if (side === 'old' && has(FMT)) {
    const fmt = marks.find((m) => m.type === FMT)!
    rest = JSON.parse(String(fmt.attrs?.old ?? '[]')) as Mark[]
  }
  const { marks: _m, ...node } = n
  void _m
  return rest.length ? { ...node, marks: rest } : node
}

function joinText(nodes: Node[]): Node[] {
  const out: Node[] = []
  for (const n of nodes) {
    const last = out[out.length - 1]
    if (
      last &&
      last.type === 'text' &&
      n.type === 'text' &&
      JSON.stringify(last.marks ?? []) === JSON.stringify(n.marks ?? [])
    )
      out[out.length - 1] = { ...last, text: (last.text ?? '') + (n.text ?? '') }
    else out.push(n)
  }
  return out
}

function projectBlock(n: Node, side: Side): Node | null {
  if (n.type === 'text' || !n.type) return projectInline(n, side)
  const diff = n.attrs?.diff
  if (diff === 'ins' && side === 'old') return null
  if (diff === 'del' && side === 'new') return null
  let type = n.type
  let attrs: Record<string, unknown> = Object.fromEntries(
    Object.entries(n.attrs ?? {}).filter(([k]) => k !== 'diff' && k !== 'diffOld' && k !== 'diffMove'),
  )
  if (side === 'old' && typeof n.attrs?.diffOld === 'string') {
    const old = JSON.parse(n.attrs.diffOld) as { type: string; attrs: Record<string, unknown> }
    type = old.type
    attrs = old.attrs
  }
  const out: Node = { ...n, type }
  if (Object.keys(attrs).length) out.attrs = attrs
  else delete out.attrs
  if (n.content) {
    const kids = n.content.map((c) => projectBlock(c, side)).filter((c): c is Node => c !== null)
    if (kids.length) out.content = joinText(kids)
    else delete out.content
  }
  // Inline atoms (an image) carry marks too.
  if (n.marks) {
    const m = projectInline(n, side)
    if (!m) return null
    if (m.marks) out.marks = m.marks
    else delete out.marks
  }
  return out
}

export function projectDoc(merged: Node, side: Side): Node {
  const kids = (merged.content ?? []).map((c) => projectBlock(c, side)).filter((c): c is Node => c !== null)
  return { type: 'doc', content: kids.length ? kids : [{ type: 'paragraph' }] }
}
