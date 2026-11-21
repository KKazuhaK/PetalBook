// Markdown-first round trip over the corpus:
//   pass 1: source -> editor -> markdown (out1)
//   pass 2: out1   -> editor -> markdown (out2)
// Every case must be stable (out2 === out1, so a second save never changes the
// file again). The cases listed in __fixtures__/exact.json also come back
// byte for byte (out1 === source): the writer's own style survives untouched.

import { describe, expect, it } from 'vitest'
import { roundtrip } from './testHarness'
import casesText from './__fixtures__/cases.txt?raw'
import exactJson from './__fixtures__/exact.json?raw'

const norm = (md: string) => md.replace(/\n+$/, '') + '\n'

const cases = casesText
  .split(/^=== /m)
  .filter(Boolean)
  .map((chunk: string) => {
    const nl = chunk.indexOf('\n')
    return { name: chunk.slice(0, nl).trim(), source: chunk.slice(nl + 1).replace(/\n+$/, '') + '\n' }
  })

const exact = new Set<string>(JSON.parse(exactJson) as string[])

describe('Markdown-first round trip', () => {
  it('has the same case counts as the spike (63 cases, 49 exact)', () => {
    expect(cases).toHaveLength(63)
    expect(exact.size).toBe(49)
    for (const name of exact) expect(cases.some((c: { name: string }) => c.name === name)).toBe(true)
  })

  for (const c of cases) {
    it(`${c.name}: stable after one pass${exact.has(c.name) ? ' and exact' : ''}`, () => {
      const out1 = norm(roundtrip(c.source))
      const out2 = norm(roundtrip(out1))
      expect(out2).toBe(out1)
      if (exact.has(c.name)) expect(out1).toBe(c.source)
    })
  }
})
