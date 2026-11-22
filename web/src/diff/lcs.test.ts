import { describe, expect, it } from 'vitest'
import { editScript, lcsPairs } from './lcs'
import { diffLines } from './lines'

// The reference: the textbook table.
function lcsLength(a: number[], b: number[]): number {
  const f = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0))
  for (let i = 1; i <= a.length; i++) for (let j = 1; j <= b.length; j++) f[i][j] = a[i - 1] === b[j - 1] ? f[i - 1][j - 1] + 1 : Math.max(f[i - 1][j], f[i][j - 1])
  return f[a.length][b.length]
}

// A small deterministic generator so a failure can be replayed.
function rng(seed: number) {
  let s = seed
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}

describe('lcsPairs', () => {
  it('finds a longest common subsequence, increasing in both', () => {
    const r = rng(7)
    for (let n = 0; n < 400; n++) {
      const a = Array.from({ length: Math.floor(r() * 14) }, () => Math.floor(r() * 4))
      const b = Array.from({ length: Math.floor(r() * 14) }, () => Math.floor(r() * 4))
      const pairs = lcsPairs(a, b)!
      expect(pairs.length).toBe(lcsLength(a, b))
      for (const [i, j] of pairs) expect(a[i]).toBe(b[j])
      for (let k = 1; k < pairs.length; k++) {
        expect(pairs[k][0]).toBeGreaterThan(pairs[k - 1][0])
        expect(pairs[k][1]).toBeGreaterThan(pairs[k - 1][1])
      }
    }
  })

  it('handles empty and identical input', () => {
    expect(lcsPairs([], [])).toEqual([])
    expect(lcsPairs([1, 2], [])).toEqual([])
    expect(lcsPairs([1, 2], [1, 2])).toEqual([[0, 0], [1, 1]])
  })

  it('gives up (null) beyond the cost cap', () => {
    const a = Array.from({ length: 300 }, (_, i) => i)
    const b = Array.from({ length: 300 }, (_, i) => 1000 + i)
    expect(lcsPairs(a, b, 100)).toBeNull()
  })
})

describe('editScript', () => {
  it('rebuilds both sides from the script', () => {
    const r = rng(11)
    for (let n = 0; n < 200; n++) {
      const a = Array.from({ length: Math.floor(r() * 12) }, () => 'abc'[Math.floor(r() * 3)])
      const b = Array.from({ length: Math.floor(r() * 12) }, () => 'abc'[Math.floor(r() * 3)])
      const s = editScript(a, b)
      expect(s.filter((e) => e.op !== 'ins').map((e) => a[(e as { a: number }).a])).toEqual(a)
      expect(s.filter((e) => e.op !== 'del').map((e) => b[(e as { b: number }).b])).toEqual(b)
    }
  })

  it('replaces the differing middle when the cap is hit', () => {
    const a = ['x', ...Array.from({ length: 50 }, (_, i) => `a${i}`), 'y']
    const b = ['x', ...Array.from({ length: 50 }, (_, i) => `b${i}`), 'y']
    const s = editScript(a, b, 10)
    expect(s[0]).toEqual({ op: 'eq', a: 0, b: 0 })
    expect(s.filter((e) => e.op === 'del')).toHaveLength(50)
    expect(s.filter((e) => e.op === 'ins')).toHaveLength(50)
    expect(s[s.length - 1].op).toBe('eq')
  })
})

describe('diffLines', () => {
  it('marks added and removed lines with their numbers', () => {
    const d = diffLines('a\nb\nc\n', 'a\nc\nd\n')
    expect(d.ops).toEqual([
      { type: 'eq', text: 'a', oldLine: 1, newLine: 1 },
      { type: 'del', text: 'b', oldLine: 2 },
      { type: 'eq', text: 'c', oldLine: 3, newLine: 2 },
      { type: 'ins', text: 'd', newLine: 3 },
    ])
    expect([d.inserted, d.deleted]).toEqual([1, 1])
  })
  it('is empty for equal texts and handles empty sides', () => {
    expect(diffLines('x\ny\n', 'x\ny\n').ops.every((o) => o.type === 'eq')).toBe(true)
    expect(diffLines('', 'a\nb').ops.map((o) => o.type)).toEqual(['ins', 'ins'])
    expect(diffLines('a\nb', '').ops.map((o) => o.type)).toEqual(['del', 'del'])
    expect(diffLines('', '').ops).toEqual([])
  })
  it('does not count the newline at the end as a line', () => {
    expect(diffLines('a\n', 'a').ops.every((o) => o.type === 'eq')).toBe(true)
  })
})
