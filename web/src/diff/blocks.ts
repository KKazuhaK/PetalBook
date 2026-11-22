// The difference of two lists of blocks, and of the blocks that hold lists of
// blocks (lists, items, quotes, tables), as one merged list in which deleted
// and inserted blocks are marked.
//
// Order of work for one list: (1) blocks that are the same in both are matched
// by a longest common subsequence over their canonical text; (2) of what is
// left, a block that only moved is found by its content; (3) between two
// matches, the remaining old and new blocks are paired by similarity (an
// order-preserving assignment), and a pair is compared inside; whatever has no
// partner is a deletion or an insertion.

import { editScript } from './lcs'
import { diffTokens, MIN_KEPT, opsToNodes, replaceAll, tokenize } from './inline'
import { canon, plainText, setDiff, type Node } from './doc'

// Blocks that cannot be compared inside (code, raw HTML, a footnote text, a
// rule) have no family: they are only ever the same, or replaced.
type Family = 'text' | 'list' | 'quote' | 'item' | 'table' | 'row' | 'cell'
function family(n: Node): Family | null {
  switch (n.type) {
    case 'paragraph':
    case 'heading':
      return 'text'
    case 'bulletList':
    case 'orderedList':
    case 'taskList':
      return 'list'
    case 'blockquote':
      return 'quote'
    case 'listItem':
    case 'taskItem':
      return 'item'
    case 'table':
      return 'table'
    case 'tableRow':
      return 'row'
    case 'tableHeader':
    case 'tableCell':
      return 'cell'
    default:
      return null
  }
}

// --- similarity -----------------------------------------------------------------

// Two blocks this alike are compared inside; less, and they are replaced.
const PAIR_MIN = 0.2
// More candidate pairs than this and a stretch of changed blocks is not paired up.
const MAX_CELLS = 40_000

const gramCache = new WeakMap<object, Map<string, number>[]>()
// Counts of the characters (size 1) or pairs of characters (size 2) of a block's text.
function grams(n: Node, size: 1 | 2): Map<string, number> {
  let both = gramCache.get(n)
  if (!both) gramCache.set(n, (both = []))
  const hit = both[size - 1]
  if (hit) return hit
  const chars = Array.from(plainText(n).replace(/\s+/g, ' ').trim())
  const out = new Map<string, number>()
  for (let i = 0; i + size <= chars.length; i++) {
    const g = chars.slice(i, i + size).join('')
    out.set(g, (out.get(g) ?? 0) + 1)
  }
  both[size - 1] = out
  return out
}

// Dice coefficient of the character pairs of the two blocks' text: cheap, and
// works for Chinese, which has no spaces to split words on. When either text is
// very short, single characters are counted instead, on both sides alike.
function dice(a: Node, b: Node): number {
  const size = Math.min(plainText(a).length, plainText(b).length) < 4 ? 1 : 2
  const ga = grams(a, size)
  const gb = grams(b, size)
  let na = 0
  let nb = 0
  for (const v of ga.values()) na += v
  for (const v of gb.values()) nb += v
  if (na === 0 || nb === 0) return 0
  let common = 0
  const [small, big] = ga.size <= gb.size ? [ga, gb] : [gb, ga]
  for (const [g, v] of small) common += Math.min(v, big.get(g) ?? 0)
  return (2 * common) / (na + nb)
}

const rowCount = (t: Node) => t.content?.length ?? 0
const colCount = (t: Node) => t.content?.[0]?.content?.length ?? 0
const rectangular = (t: Node) => (t.content ?? []).every((r) => (r.content?.length ?? 0) === colCount(t))

// Whether two blocks may be compared inside at all.
function comparable(a: Node, b: Node): boolean {
  const f = family(a)
  if (!f || f !== family(b)) return false
  return f !== 'table' || (colCount(a) === colCount(b) && rectangular(a) && rectangular(b))
}

function similarity(a: Node, b: Node): number {
  if (!comparable(a, b)) return 0
  const s = dice(a, b)
  return s >= PAIR_MIN ? s : 0
}

// --- entries ---------------------------------------------------------------------

type Entry = { t: 'eq'; b: Node } | { t: 'pair'; a: Node; b: Node } | { t: 'del'; a: Node; move: boolean } | { t: 'ins'; b: Node; move: boolean }

// A block long enough that finding it elsewhere is not a coincidence.
const movable = (n: Node) => n.type !== 'horizontalRule' && plainText(n).length >= 6

function alignGap(A: Node[], B: Node[], movedA: boolean[], movedB: boolean[]): Entry[] {
  const n = A.length
  const m = B.length
  const out: Entry[] = []
  const dels = (from: number, to: number) => {
    for (let i = from; i < to; i++) out.push({ t: 'del', a: A[i], move: movedA[i] })
  }
  const inss = (from: number, to: number) => {
    for (let j = from; j < to; j++) out.push({ t: 'ins', b: B[j], move: movedB[j] })
  }
  if (n === 0 || m === 0 || n * m > MAX_CELLS) {
    dels(0, n)
    inss(0, m)
    return out
  }
  // One old block against one new block between two matches: very probably an
  // edit of it, however short the text. The comparison itself decides whether
  // enough is left unchanged to show it that way.
  if (n === 1 && m === 1 && !movedA[0] && !movedB[0] && comparable(A[0], B[0])) return [{ t: 'pair', a: A[0], b: B[0] }]
  // Far from the diagonal a pairing is unlikely and not worth computing.
  const band = n * m <= 400 ? Infinity : Math.max(10, Math.abs(n - m) + 10)
  const sim = (i: number, j: number) => (movedA[i] || movedB[j] || Math.abs((i * m) / n - j) > band ? 0 : similarity(A[i], B[j]))
  const W = m + 1
  const f = new Float64Array((n + 1) * W)
  for (let i = 1; i <= n; i++) {
    for (let j = 1; j <= m; j++) {
      let best = Math.max(f[(i - 1) * W + j], f[i * W + j - 1])
      const s = sim(i - 1, j - 1)
      if (s > 0) best = Math.max(best, f[(i - 1) * W + j - 1] + s)
      f[i * W + j] = best
    }
  }
  // Walk back from the end; collect in reverse.
  const rev: Entry[] = []
  let i = n
  let j = m
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0) {
      const s = sim(i - 1, j - 1)
      if (s > 0 && f[i * W + j] === f[(i - 1) * W + j - 1] + s) {
        rev.push({ t: 'pair', a: A[i - 1], b: B[j - 1] })
        i--
        j--
        continue
      }
    }
    if (i > 0 && (j === 0 || f[i * W + j] === f[(i - 1) * W + j])) {
      rev.push({ t: 'del', a: A[i - 1], move: movedA[i - 1] })
      i--
    } else {
      rev.push({ t: 'ins', b: B[j - 1], move: movedB[j - 1] })
      j--
    }
  }
  rev.reverse()
  // Between two pairs: the deleted blocks first, then the inserted.
  let k = 0
  while (k < rev.length) {
    if (rev[k].t === 'pair') {
      out.push(rev[k++])
      continue
    }
    let e = k
    while (e < rev.length && rev[e].t !== 'pair') e++
    const run = rev.slice(k, e)
    out.push(...run.filter((x) => x.t === 'del'), ...run.filter((x) => x.t === 'ins'))
    k = e
  }
  return out
}

// --- the list of blocks ----------------------------------------------------------

export function diffBlocks(a: readonly Node[], b: readonly Node[]): Node[] {
  if (a.length === 0 && b.length === 0) return []
  const script = editScript(a.map(canon), b.map(canon), 2000)

  // Blocks found in both lists in another place, not matched by the sequence.
  const movedA = new Array<boolean>(a.length).fill(false)
  const movedB = new Array<boolean>(b.length).fill(false)
  const lostB = new Map<string, number[]>()
  for (const e of script) {
    if (e.op !== 'ins' || !movable(b[e.b])) continue
    const key = canon(b[e.b])
    const at = lostB.get(key)
    if (at) at.push(e.b)
    else lostB.set(key, [e.b])
  }
  for (const e of script) {
    if (e.op !== 'del' || !movable(a[e.a])) continue
    const j = lostB.get(canon(a[e.a]))?.shift()
    if (j !== undefined) {
      movedA[e.a] = true
      movedB[j] = true
    }
  }

  const entries: Entry[] = []
  let pa: number[] = []
  let pb: number[] = []
  const flush = () => {
    if (pa.length || pb.length) {
      entries.push(
        ...alignGap(
          pa.map((i) => a[i]),
          pb.map((j) => b[j]),
          pa.map((i) => movedA[i]),
          pb.map((j) => movedB[j]),
        ),
      )
    }
    pa = []
    pb = []
  }
  for (const e of script) {
    if (e.op === 'eq') {
      flush()
      entries.push({ t: 'eq', b: b[e.b] })
    } else if (e.op === 'del') pa.push(e.a)
    else pb.push(e.b)
  }
  flush()

  const out: Node[] = []
  for (const e of entries) {
    switch (e.t) {
      case 'eq':
        out.push(e.b)
        break
      case 'del':
        out.push(setDiff(e.a, 'del', { move: e.move }))
        break
      case 'ins':
        out.push(setDiff(e.b, 'ins', { move: e.move }))
        break
      case 'pair': {
        const merged = mergePair(e.a, e.b)
        if (merged) out.push(merged)
        else out.push(setDiff(e.a, 'del'), setDiff(e.b, 'ins'))
      }
    }
  }
  return out
}

// --- one pair ---------------------------------------------------------------------

const DIFF_ATTRS = new Set(['diff', 'diffOld', 'diffMove'])
const plainAttrs = (n: Node): Record<string, unknown> => Object.fromEntries(Object.entries(n.attrs ?? {}).filter(([k]) => !DIFF_ATTRS.has(k)))
const sameAttrs = (a: Node, b: Node) => a.type === b.type && JSON.stringify(sorted(plainAttrs(a))) === JSON.stringify(sorted(plainAttrs(b)))
const sorted = (o: Record<string, unknown>) => Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]]))

// The merged node is the new one; when the old one was another type or had
// other attributes it is flagged, with what it was.
function flagged(merged: Node, a: Node, b: Node): Node {
  return sameAttrs(a, b) ? merged : setDiff(merged, 'fmt', { old: { type: a.type ?? '', attrs: plainAttrs(a) } })
}

// `null`: the pair is not worth comparing inside; the caller shows old then new.
function mergePair(a: Node, b: Node): Node | null {
  switch (family(b)) {
    case 'text':
      return mergeText(a, b, false)
    case 'list':
      return mergeList(a, b)
    case 'quote':
      return { ...b, content: diffBlocks(a.content ?? [], b.content ?? []) }
    case 'item':
      return flagged(mergeItem(a, b), a, b)
    case 'table':
      return mergeTable(a, b)
    case 'row':
      return mergeRow(a, b)
    default:
      return null
  }
}

// Paragraphs and headings. `force` keeps the pair together whatever the
// similarity (a table cell, the first line of a list item): a stretch that is
// almost all changed is then shown as one deletion and one insertion inside.
function mergeText(a: Node, b: Node, force: boolean): Node | null {
  const ta = tokenize(a.content)
  const tb = tokenize(b.content)
  let d = diffTokens(ta, tb)
  if (!d || d.kept < MIN_KEPT) {
    if (!force) return null
    d = replaceAll(ta, tb)
  }
  const content = opsToNodes(d.ops)
  const merged: Node = { ...b }
  if (content.length) merged.content = content
  else delete merged.content
  return flagged(merged, a, b)
}

const itemTypeOf = (list: Node) => (list.type === 'taskList' ? 'taskItem' : 'listItem')

function mergeList(a: Node, b: Node): Node {
  const want = itemTypeOf(b)
  const items = diffBlocks(a.content ?? [], b.content ?? []).map((it) => {
    // A list of another kind than the old one cannot hold the old items as
    // they were (a task list holds only task items): they are carried in the
    // new kind, with what they were.
    if (it.type === want) return it
    const attrs: Record<string, unknown> = { diffOld: JSON.stringify({ type: it.type, attrs: plainAttrs(it) }) }
    if (want === 'taskItem') attrs.checked = false
    if (it.attrs?.diff) attrs.diff = it.attrs.diff
    if (it.attrs?.diffMove) attrs.diffMove = true
    return { ...it, type: want, attrs }
  })
  return flagged({ ...b, content: items }, a, b)
}

function mergeItem(a: Node, b: Node): Node {
  const ca = a.content ?? []
  const cb = b.content ?? []
  if (ca[0]?.type === 'paragraph' && cb[0]?.type === 'paragraph') {
    const head = mergeText(ca[0], cb[0], true)!
    return { ...b, content: [head, ...diffBlocks(ca.slice(1), cb.slice(1))] }
  }
  return { ...b, content: diffBlocks(ca, cb) }
}

// Same number of columns: the header rows are compared with each other, the
// other rows are lined up like any list of blocks (a row can be added or
// removed), and the cells of two matching rows are compared one by one.
function mergeTable(a: Node, b: Node): Node | null {
  if (colCount(a) !== colCount(b) || !rectangular(a) || !rectangular(b) || rowCount(a) === 0 || rowCount(b) === 0) return null
  const ra = a.content!
  const rb = b.content!
  const head = mergeRow(ra[0], rb[0])
  return { ...b, content: [head, ...diffBlocks(ra.slice(1), rb.slice(1))] }
}

function mergeRow(a: Node, b: Node): Node {
  const ca = a.content ?? []
  const cb = b.content ?? []
  return { ...b, content: cb.map((cell, i) => mergeCell(ca[i], cell)) }
}

function mergeCell(a: Node, b: Node): Node {
  const ca = a.content ?? []
  const cb = b.content ?? []
  const content =
    ca.length === 1 && cb.length === 1 && ca[0].type === 'paragraph' && cb[0].type === 'paragraph'
      ? [mergeText(ca[0], cb[0], true)!]
      : diffBlocks(ca, cb)
  return flagged({ ...b, content }, a, b)
}
