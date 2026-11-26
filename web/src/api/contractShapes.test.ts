import { describe, expect, it } from 'vitest'
import { join, misfits, shapeOf } from './contractShapes'

describe('shapes of answers', () => {
  it('keeps keys and types and no values', () => {
    const s = shapeOf({ name: 'x', n: 1, ok: true, none: null, tags: ['a', 'b'], empty: [], in: { id: 'z' } })
    expect(s).toEqual({
      empty: [],
      in: { id: 'string' },
      n: 'number',
      name: 'string',
      none: 'null',
      ok: 'boolean',
      tags: ['string'],
    })
    expect(JSON.stringify(s)).not.toContain('"x"')
  })

  it('joins answers: a key some lacked is optional, a type that varied is a union', () => {
    const s = join(
      shapeOf({ a: 'x', b: null, l: [{ k: 1 }] }),
      shapeOf({ a: 'y', b: { c: 1 }, d: 2, l: [{ k: 2, m: 3 }] }),
    )
    expect(s).toEqual({
      a: 'string',
      b: { $or: ['null', { c: 'number' }] },
      'd?': 'number',
      l: [{ k: 'number', 'm?': 'number' }],
    })
  })

  it('finds a new key, a missing key and a changed type', () => {
    const shape = shapeOf({ a: 'x', items: [{ id: 'i' }] })
    expect(misfits({ a: 'q', items: [{ id: 'j' }, { id: 'k' }] }, shape)).toEqual([])
    expect(misfits({ a: 'q', items: [], extra: 1 }, shape)).toEqual(['$.extra: a new key'])
    expect(misfits({ items: [{ id: 1 }] }, shape)).toEqual(['$.a: missing', '$.items[0].id: number, recorded string'])
  })
})
