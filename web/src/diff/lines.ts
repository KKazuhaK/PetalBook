// A plain line-by-line comparison of two Markdown texts, for people who would
// rather read text than the marked-up page.

import { editScript } from './lcs'

export type LineOp = { type: 'eq' | 'ins' | 'del'; text: string; oldLine?: number; newLine?: number }
export interface LineDiff {
  ops: LineOp[]
  inserted: number
  deleted: number
}

const lines = (s: string): string[] => {
  if (s === '') return []
  const out = s.split('\n')
  // A final newline ends the last line; it does not start another.
  if (out[out.length - 1] === '') out.pop()
  return out
}

export function diffLines(a: string, b: string): LineDiff {
  const la = lines(a)
  const lb = lines(b)
  const ops: LineOp[] = []
  let inserted = 0
  let deleted = 0
  for (const e of editScript(la, lb, 4000)) {
    if (e.op === 'eq') ops.push({ type: 'eq', text: lb[e.b], oldLine: e.a + 1, newLine: e.b + 1 })
    else if (e.op === 'del') {
      deleted++
      ops.push({ type: 'del', text: la[e.a], oldLine: e.a + 1 })
    } else {
      inserted++
      ops.push({ type: 'ins', text: lb[e.b], newLine: e.b + 1 })
    }
  }
  return { ops, inserted, deleted }
}
