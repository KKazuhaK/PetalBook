// Longest common subsequence of two sequences of small integers (Myers'
// O(ND) algorithm). Written here rather than pulled in: it is short, it has no
// dependencies, and the caller needs the cost cap below, which a general
// library does not offer.

// Index pairs [i, j] with a[i] === b[j], increasing in both, of one longest
// common subsequence. `null` when the sequences differ by more than `maxD`
// insertions plus deletions: the caller then treats the differing middle as
// replaced instead of paying for an exact answer.
export function lcsPairs(a: ArrayLike<number>, b: ArrayLike<number>, maxD = 1000): [number, number][] | null {
  const n = a.length
  const m = b.length
  // Equal beginnings and ends are matched without any search: most edits are small.
  let lo = 0
  while (lo < n && lo < m && a[lo] === b[lo]) lo++
  let hiA = n
  let hiB = m
  while (hiA > lo && hiB > lo && a[hiA - 1] === b[hiB - 1]) {
    hiA--
    hiB--
  }
  const out: [number, number][] = []
  for (let i = 0; i < lo; i++) out.push([i, i])
  if (hiA > lo && hiB > lo) {
    const mid = middle(a, lo, hiA, b, lo, hiB, maxD)
    if (!mid) return null
    for (const p of mid) out.push(p)
  }
  for (let k = 0; k < n - hiA; k++) out.push([hiA + k, hiB + k])
  return out
}

function middle(
  a: ArrayLike<number>,
  a0: number,
  a1: number,
  b: ArrayLike<number>,
  b0: number,
  b1: number,
  maxD: number,
): [number, number][] | null {
  const N = a1 - a0
  const M = b1 - b0
  const max = Math.min(N + M, maxD)
  const offset = max + 1
  const v = new Int32Array(2 * max + 3)
  // v as it was at the start of each round, to walk back from the end.
  const trace: Int32Array[] = []
  for (let d = 0; d <= max; d++) {
    trace.push(v.slice())
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1]) ? v[offset + k + 1] : v[offset + k - 1] + 1
      let y = x - k
      while (x < N && y < M && a[a0 + x] === b[b0 + y]) {
        x++
        y++
      }
      v[offset + k] = x
      if (x >= N && y >= M) return walkBack(trace, d, N, M, offset, a0, b0)
    }
  }
  return null
}

function walkBack(
  trace: Int32Array[],
  dEnd: number,
  N: number,
  M: number,
  offset: number,
  a0: number,
  b0: number,
): [number, number][] {
  const out: [number, number][] = []
  let x = N
  let y = M
  for (let d = dEnd; d >= 0; d--) {
    const v = trace[d]
    const k = x - y
    const prevK = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1]) ? k + 1 : k - 1
    const prevX = v[offset + prevK]
    const prevY = prevX - prevK
    while (x > prevX && y > prevY) {
      x--
      y--
      out.push([a0 + x, b0 + y])
    }
    x = prevX
    y = prevY
  }
  return out.reverse()
}

// Turns strings into integers so the comparisons above are single-word.
export function intern(a: readonly string[], b: readonly string[]): [Int32Array, Int32Array] {
  const ids = new Map<string, number>()
  const id = (s: string) => {
    let n = ids.get(s)
    if (n === undefined) {
      n = ids.size
      ids.set(s, n)
    }
    return n
  }
  return [Int32Array.from(a, id), Int32Array.from(b, id)]
}

// One step of an edit script.
export type Edit = { op: 'eq'; a: number; b: number } | { op: 'del'; a: number } | { op: 'ins'; b: number }

// The matches as a full script: deletions of what is only in `a`, insertions
// of what is only in `b`, in an order that keeps both sequences in order
// (deletions before insertions between two matches). When the cost cap is
// hit, everything between the equal ends is a deletion followed by an insertion.
export function editScript(a: readonly string[], b: readonly string[], maxD = 1000): Edit[] {
  const [ia, ib] = intern(a, b)
  let pairs = lcsPairs(ia, ib, maxD)
  if (!pairs) {
    let lo = 0
    while (lo < ia.length && lo < ib.length && ia[lo] === ib[lo]) lo++
    let hiA = ia.length
    let hiB = ib.length
    while (hiA > lo && hiB > lo && ia[hiA - 1] === ib[hiB - 1]) {
      hiA--
      hiB--
    }
    pairs = []
    for (let i = 0; i < lo; i++) pairs.push([i, i])
    for (let k = 0; k < ia.length - hiA; k++) pairs.push([hiA + k, hiB + k])
  }
  const out: Edit[] = []
  let i = 0
  let j = 0
  for (const [pi, pj] of pairs) {
    while (i < pi) out.push({ op: 'del', a: i++ })
    while (j < pj) out.push({ op: 'ins', b: j++ })
    out.push({ op: 'eq', a: i++, b: j++ })
  }
  while (i < ia.length) out.push({ op: 'del', a: i++ })
  while (j < ib.length) out.push({ op: 'ins', b: j++ })
  return out
}
