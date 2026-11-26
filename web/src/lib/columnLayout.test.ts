import { describe, expect, it } from 'vitest'
import {
  autoWidths,
  clampShares,
  dropColumn,
  dropGap,
  fitColumn,
  type FitColumn,
  moveColumn,
  pixelWidths,
  resetPair,
  resizePair,
  roomOf,
  stepColumn,
} from '@/lib/columnLayout'

const ORDER = ['a', 'b', 'c', 'd']

describe('moving a column', () => {
  it('puts it before or after another', () => {
    expect(moveColumn(ORDER, 'a', 'c', 'before')).toEqual(['b', 'a', 'c', 'd'])
    expect(moveColumn(ORDER, 'a', 'c', 'after')).toEqual(['b', 'c', 'a', 'd'])
    expect(moveColumn(ORDER, 'd', 'a', 'before')).toEqual(['d', 'a', 'b', 'c'])
  })
  it('leaves the order alone for itself, strangers and no-op moves', () => {
    expect(moveColumn(ORDER, 'a', 'a', 'before')).toEqual(ORDER)
    expect(moveColumn(ORDER, 'x', 'a', 'before')).toEqual(ORDER)
    expect(moveColumn(ORDER, 'a', 'x', 'after')).toEqual(ORDER)
    expect(moveColumn(ORDER, 'b', 'c', 'before')).toEqual(ORDER)
  })
  it('keeps pinned columns where they are and moves the others among themselves', () => {
    const pinned = ['a', 'd']
    expect(moveColumn(ORDER, 'b', 'c', 'after', pinned)).toEqual(['a', 'c', 'b', 'd'])
    expect(moveColumn(ORDER, 'a', 'c', 'after', pinned)).toEqual(ORDER)
    expect(moveColumn(ORDER, 'b', 'a', 'before', pinned)).toEqual(ORDER)
    expect(moveColumn(['a', 'b', 'p', 'c'], 'c', 'a', 'before', ['p'])).toEqual(['c', 'a', 'p', 'b'])
  })
  it('steps by one among the shown columns, over the hidden ones, and stops at the ends', () => {
    expect(stepColumn(ORDER, ORDER, 'b', -1)).toEqual(['b', 'a', 'c', 'd'])
    expect(stepColumn(ORDER, ORDER, 'b', 1)).toEqual(['a', 'c', 'b', 'd'])
    expect(stepColumn(ORDER, ORDER, 'a', -1)).toEqual(ORDER)
    expect(stepColumn(ORDER, ORDER, 'd', 1)).toEqual(ORDER)
    // b is hidden: a steps right past c, the next shown one.
    expect(stepColumn(ORDER, ['a', 'c', 'd'], 'a', 1)).toEqual(['b', 'c', 'a', 'd'])
  })
  it('finds the gap under the pointer and drops into it', () => {
    const mids = [50, 150, 250]
    expect(dropGap(mids, 10)).toBe(0)
    expect(dropGap(mids, 100)).toBe(1)
    expect(dropGap(mids, 400)).toBe(3)
    expect(dropColumn(ORDER, ORDER, 'a', 4)).toEqual(['b', 'c', 'd', 'a'])
    expect(dropColumn(ORDER, ORDER, 'a', 3)).toEqual(['b', 'c', 'a', 'd'])
    expect(dropColumn(ORDER, ORDER, 'd', 0)).toEqual(['d', 'a', 'b', 'c'])
    expect(dropColumn(ORDER, ORDER, 'b', 1)).toEqual(ORDER)
    expect(dropColumn(ORDER, ['a', 'c'], 'a', 2)).toEqual(['b', 'c', 'a', 'd'])
    expect(dropColumn(ORDER, [], 'a', 0)).toEqual(ORDER)
  })
})

describe('column widths', () => {
  it('shares out the room, never more than there is', () => {
    expect(roomOf(1000, 236)).toBe(764)
    expect(roomOf(100, 236)).toBe(0)
    const px = pixelWidths(['a', 'b', 'c'], { a: 0.5, b: 0.25, c: 0.25 }, 333)
    expect(px.a + px.b + px.c).toBe(333)
  })
  it('moves an edge: one grows, the other shrinks, the rest stay', () => {
    const have = { a: 0.4, b: 0.3, c: 0.3 }
    const next = resizePair(have, 'a', 'b', 100, 1000, 100, 100)
    expect(next.a).toBeCloseTo(0.5)
    expect(next.b).toBeCloseTo(0.2)
    expect(next.c).toBe(0.3)
    expect(next.a + next.b + next.c).toBeCloseTo(1)
  })
  it('stops at the least width of either side', () => {
    const have = { a: 0.4, b: 0.3, c: 0.3 }
    expect(resizePair(have, 'a', 'b', 900, 1000, 100, 150).b).toBeCloseTo(0.15)
    expect(resizePair(have, 'a', 'b', -900, 1000, 250, 100).a).toBeCloseTo(0.25)
  })
  it('does not push a column that is already below its least width', () => {
    const have = { a: 0.05, b: 0.95 }
    expect(resizePair(have, 'a', 'b', -50, 1000, 100, 100)).toEqual(have)
    expect(resizePair(have, 'a', 'b', 50, 1000, 100, 100).a).toBeCloseTo(0.1)
    expect(resizePair(have, 'a', 'b', 50, 0, 100, 100)).toEqual(have)
  })
  it('puts two neighbours back to their weights, keeping what they share', () => {
    const next = resetPair({ a: 0.5, b: 0.1, c: 0.4 }, 'a', 'b', 1, 2)
    expect(next.a).toBeCloseTo(0.2)
    expect(next.b).toBeCloseTo(0.4)
    expect(next.c).toBe(0.4)
  })
})

describe('clamping shares', () => {
  it('raises a column below its least width and takes the cost from the others, still adding up to one', () => {
    const out = clampShares({ a: 0.05, b: 0.45, c: 0.5 }, { a: 100, b: 100, c: 100 }, 1000)
    expect(out.a).toBeCloseTo(0.1)
    expect(out.b + out.c).toBeCloseTo(0.9)
    expect(out.b / out.c).toBeCloseTo(0.9)
    expect(out.a + out.b + out.c).toBeCloseTo(1)
  })
  it('settles when raising one pushes another below its own', () => {
    const out = clampShares({ a: 0.02, b: 0.18, c: 0.8 }, { a: 100, b: 170, c: 100 }, 1000)
    expect(out.a).toBeCloseTo(0.1)
    expect(out.b).toBeCloseTo(0.17)
    expect(out.c).toBeCloseTo(0.73)
  })
  it('leaves shares that already fit, and an unmeasured room, alone', () => {
    const have = { a: 0.5, b: 0.5 }
    expect(clampShares(have, { a: 100, b: 100 }, 1000)).toEqual(have)
    expect(clampShares(have, { a: 100, b: 100 }, 0)).toEqual(have)
  })
})

describe('auto fit', () => {
  const col = (id: string, content: number, over: Partial<FitColumn> = {}): FitColumn => ({
    id,
    content,
    min: 100,
    max: 320,
    weight: 1,
    ...over,
  })
  it('gives each column its content, within its least width and its cap, and shares what is left by weight', () => {
    const out = autoWidths([col('a', 150), col('b', 40), col('c', 900, { weight: 3 })], 800)
    // 150, 100 (the least) and 320 (the cap) leave 230: a quarter to each of the first two (57), the rest to c.
    expect(out.widths).toEqual({ a: 150 + 46, b: 100 + 46, c: 320 + 138 })
    expect(out.total).toBe(800)
    expect(Object.values(out.widths).reduce((n, w) => n + w, 0)).toBe(800)
  })
  it('keeps the content widths, and reports the wider total, when they do not fit the room', () => {
    const out = autoWidths([col('a', 250), col('b', 300)], 400)
    expect(out.widths).toEqual({ a: 250, b: 300 })
    expect(out.total).toBe(550)
  })
  it('lets a cap below the least width give way, and takes a column not measured as its least width', () => {
    const out = autoWidths([col('a', 500, { max: 50 }), col('b', 0)], 0)
    expect(out.widths).toEqual({ a: 100, b: 100 })
  })
  it('sets one column to a width and the others keep their proportions, never below their least', () => {
    const out = fitColumn({ a: 0.25, b: 0.25, c: 0.5 }, 'a', 400, 1000, { a: 100, b: 100, c: 100 })
    expect(out.a).toBeCloseTo(0.4)
    expect(out.c / out.b).toBeCloseTo(2)
    expect(out.a + out.b + out.c).toBeCloseTo(1)
    const tight = fitColumn({ a: 0.5, b: 0.25, c: 0.25 }, 'a', 900, 1000, { a: 100, b: 100, c: 100 })
    expect(tight.a).toBeCloseTo(0.8)
    expect(tight.b).toBeCloseTo(0.1)
  })
})
