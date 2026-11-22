// The difference of two runs of inline content (text with marks, images, line
// breaks, raw HTML, footnote marks).
//
// Text is cut into words (`Intl.Segmenter`, which knows where Chinese words
// end) and the words are compared; where a changed stretch holds Chinese or
// Japanese it is then compared again by character, so "今天" -> "昨天" shows
// one changed character, not two changed words. Every unit keeps the marks it
// had on its own side, so bold text deleted stays bold.

import { editScript } from './lcs'
import { CJK, graphemeCount, graphemes, wordCuts } from './segment'
import { DEL, FMT, INS, marksKey, type Node } from './doc'

type Mark = NonNullable<Node['marks']>[number]

// More than this many units after the equal ends are cut off: the quadratic
// part is not worth it and the block is shown replaced.
export const MAX_UNITS = 6000
const MAX_EDIT_DISTANCE = 1000
// A changed stretch is refined by character only up to this many characters a side.
const MAX_REFINE = 400
// Unchanged stretches this short between two changes are shown as changed too.
const ISLAND = 1
// Below this share of unchanged text a comparison is not worth showing.
export const MIN_KEPT = 0.3

export interface Tok {
  key: string
  node: Node
  marks: string
  // How many characters it counts for in the totals.
  size: number
}

export type OpKind = 'eq' | 'fmt' | 'del' | 'ins'
export interface Op {
  k: OpKind
  a?: Tok
  b?: Tok
}

export function tokenize(content: readonly Node[] | undefined, forceFallback = false): Tok[] {
  const nodes = content ?? []
  let flat = ''
  const spans: { node: Node; start: number; end: number }[] = []
  for (const n of nodes) {
    const start = flat.length
    flat += n.type === 'text' ? (n.text ?? '') : '￼'
    spans.push({ node: n, start, end: flat.length })
  }
  if (flat === '') return []
  const cuts = wordCuts(flat, forceFallback)
  const toks: Tok[] = []
  let ci = 0
  for (const { node, start, end } of spans) {
    const marks = marksKey(node.marks)
    if (node.type !== 'text') {
      toks.push({ key: `\u0001${node.type}${node.attrs ? JSON.stringify(node.attrs) : ''}`, node, marks, size: 1 })
      continue
    }
    let pos = start
    while (pos < end) {
      while (ci < cuts.length && cuts[ci] <= pos) ci++
      const stop = Math.min(ci < cuts.length ? cuts[ci] : end, end)
      const text = flat.slice(pos, stop)
      toks.push({ key: text, node: { type: 'text', text, ...(node.marks?.length ? { marks: node.marks } : {}) }, marks, size: graphemeCount(text) })
      pos = stop
    }
  }
  return toks
}

const sizeOf = (toks: readonly Tok[]) => toks.reduce((n, t) => n + t.size, 0)

// Word-level script as operations, before any cleaning.
function rawOps(a: readonly Tok[], b: readonly Tok[]): Op[] | null {
  // Only what is left after the equal ends counts against the cap.
  let lo = 0
  while (lo < a.length && lo < b.length && a[lo].key === b[lo].key) lo++
  let ea = a.length
  let eb = b.length
  while (ea > lo && eb > lo && a[ea - 1].key === b[eb - 1].key) {
    ea--
    eb--
  }
  if (ea - lo + (eb - lo) > MAX_UNITS) return null
  const script = editScript(
    a.map((t) => t.key),
    b.map((t) => t.key),
    MAX_EDIT_DISTANCE,
  )
  // The edit script gave up (too many changes): it left one big deletion and insertion.
  return script.map((e) => {
    if (e.op === 'del') return { k: 'del' as const, a: a[e.a] }
    if (e.op === 'ins') return { k: 'ins' as const, b: b[e.b] }
    const x = a[e.a]
    const y = b[e.b]
    return { k: x.marks === y.marks ? ('eq' as const) : ('fmt' as const), a: x, b: y }
  })
}

const opSize = (o: Op) => (o.k === 'ins' ? (o.b?.size ?? 0) : (o.a?.size ?? 0))

// Unchanged stretches of at most ISLAND characters between two changes become
// changed: a lone letter or space left standing in the middle of a rewrite
// reads as noise.
function mergeIslands(ops: Op[]): Op[] {
  const out: Op[] = []
  let i = 0
  while (i < ops.length) {
    if (ops[i].k !== 'eq') {
      out.push(ops[i++])
      continue
    }
    let j = i
    let size = 0
    while (j < ops.length && ops[j].k === 'eq') size += opSize(ops[j++])
    const between = i > 0 && j < ops.length
    if (between && size <= ISLAND) {
      for (let q = i; q < j; q++) {
        out.push({ k: 'del', a: ops[q].a }, { k: 'ins', b: ops[q].b })
      }
    } else for (let q = i; q < j; q++) out.push(ops[q])
    i = j
  }
  return out
}

function charToks(toks: readonly Tok[]): Tok[] {
  const out: Tok[] = []
  for (const t of toks) {
    for (const g of graphemes(t.key)) out.push({ key: g, node: { ...t.node, text: g }, marks: t.marks, size: 1 })
  }
  return out
}

// Changed stretches (deletions and insertions with nothing between) that hold
// CJK text are compared again, by character.
function refine(ops: Op[]): Op[] {
  const out: Op[] = []
  let i = 0
  while (i < ops.length) {
    if (ops[i].k !== 'del' && ops[i].k !== 'ins') {
      out.push(ops[i++])
      continue
    }
    let j = i
    while (j < ops.length && (ops[j].k === 'del' || ops[j].k === 'ins')) j++
    const run = ops.slice(i, j)
    i = j
    const dels = run.filter((o) => o.k === 'del').map((o) => o.a!)
    const inss = run.filter((o) => o.k === 'ins').map((o) => o.b!)
    const texty = [...dels, ...inss].every((t) => t.node.type === 'text')
    if (!texty || dels.length === 0 || inss.length === 0 || !CJK.test([...dels, ...inss].map((t) => t.key).join(''))) {
      out.push(...run)
      continue
    }
    const ca = charToks(dels)
    const cb = charToks(inss)
    if (ca.length > MAX_REFINE || cb.length > MAX_REFINE) {
      out.push(...run)
      continue
    }
    const script = editScript(
      ca.map((t) => t.key),
      cb.map((t) => t.key),
      MAX_EDIT_DISTANCE,
    )
    const fine = mergeIslands(
      script.map((e): Op => {
        if (e.op === 'del') return { k: 'del', a: ca[e.a] }
        if (e.op === 'ins') return { k: 'ins', b: cb[e.b] }
        return { k: ca[e.a].marks === cb[e.b].marks ? 'eq' : 'fmt', a: ca[e.a], b: cb[e.b] }
      }),
    )
    const kept = fine.filter((o) => o.k === 'eq' || o.k === 'fmt').length
    // Confetti is worse than a plain replacement.
    if (kept * 2.5 < Math.max(ca.length, cb.length)) out.push(...run)
    else out.push(...fine)
  }
  return out
}

// Within a run of deletions and insertions between two kept stretches, the
// deleted text comes first, then the inserted.
function orderRuns(ops: Op[]): Op[] {
  const out: Op[] = []
  let i = 0
  while (i < ops.length) {
    if (ops[i].k !== 'del' && ops[i].k !== 'ins') {
      out.push(ops[i++])
      continue
    }
    let j = i
    while (j < ops.length && (ops[j].k === 'del' || ops[j].k === 'ins')) j++
    const run = ops.slice(i, j)
    out.push(...run.filter((o) => o.k === 'del'), ...run.filter((o) => o.k === 'ins'))
    i = j
  }
  return out
}

export interface InlineDiff {
  ops: Op[]
  // Share of the text (by characters, both sides together) that is unchanged.
  kept: number
}

// Everything deleted, then everything inserted.
export function replaceAll(a: readonly Tok[], b: readonly Tok[]): InlineDiff {
  return { ops: [...a.map((t): Op => ({ k: 'del', a: t })), ...b.map((t): Op => ({ k: 'ins', b: t }))], kept: 0 }
}

// `null` when the two are too long to compare in reasonable time.
export function diffTokens(a: readonly Tok[], b: readonly Tok[]): InlineDiff | null {
  const raw = rawOps(a, b)
  if (!raw) return null
  const ops = orderRuns(refine(mergeIslands(raw)))
  const total = sizeOf(a) + sizeOf(b)
  const kept = ops.reduce((n, o) => n + (o.k === 'eq' || o.k === 'fmt' ? 2 * (o.a?.size ?? 0) : 0), 0)
  return { ops, kept: total === 0 ? 1 : kept / total }
}

const nodeWith = (tok: Tok, extra: Mark | null, marks: Mark[] | undefined): Node => {
  const all = [...(marks ?? []), ...(extra ? [extra] : [])]
  const { marks: _m, ...rest } = tok.node
  void _m
  return all.length ? { ...rest, marks: all } : rest
}

// Operations to inline nodes: unchanged text as it is, inserted and deleted
// text carrying the insertion or deletion mark, same text with other
// formatting carrying the format mark (and the old marks, so the old side can
// be read back). Neighbours alike are joined into one text node.
export function opsToNodes(ops: readonly Op[]): Node[] {
  const out: Node[] = []
  let last: { node: Node; id: string } | null = null
  for (const o of ops) {
    const tok = o.k === 'del' ? o.a! : o.b!
    let node: Node
    let id: string
    switch (o.k) {
      case 'eq':
        node = nodeWith(tok, null, tok.node.marks)
        id = `eq|${tok.marks}`
        break
      case 'ins':
        node = nodeWith(tok, { type: INS }, tok.node.marks)
        id = `ins|${tok.marks}`
        break
      case 'del':
        node = nodeWith(tok, { type: DEL }, tok.node.marks)
        id = `del|${tok.marks}`
        break
      default:
        node = nodeWith(tok, { type: FMT, attrs: { old: JSON.stringify(o.a!.node.marks ?? []) } }, tok.node.marks)
        id = `fmt|${tok.marks}|${o.a!.marks}`
    }
    if (last && last.id === id && node.type === 'text' && last.node.type === 'text') {
      last.node = { ...last.node, text: (last.node.text ?? '') + (node.text ?? '') }
      out[out.length - 1] = last.node
    } else {
      last = { node, id }
      out.push(node)
    }
  }
  return out
}

// Characters inserted and deleted by a set of operations (an object such as an
// image counts as one).
export function countOps(ops: readonly Op[]): { ins: number; del: number } {
  let ins = 0
  let del = 0
  for (const o of ops) {
    if (o.k === 'ins') ins += o.b!.size
    else if (o.k === 'del') del += o.a!.size
  }
  return { ins, del }
}

export type TextPart = { type: 'eq' | 'ins' | 'del'; text: string }

// Two plain strings (a header field) as text parts.
export function diffText(a: string, b: string): TextPart[] {
  const d = diffTokens(tokenize(a === '' ? [] : [{ type: 'text', text: a }]), tokenize(b === '' ? [] : [{ type: 'text', text: b }])) ?? replaceAll(tokenize([{ type: 'text', text: a }]), tokenize([{ type: 'text', text: b }]))
  const parts: TextPart[] = []
  for (const o of d.ops) {
    const type = o.k === 'del' ? 'del' : o.k === 'ins' ? 'ins' : 'eq'
    const text = o.k === 'del' ? o.a!.key : o.b!.key
    const prev = parts[parts.length - 1]
    if (prev && prev.type === type) prev.text += text
    else parts.push({ type, text })
  }
  return parts
}
