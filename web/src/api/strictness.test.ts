import { describe, expect, it } from 'vitest'
import { isLooser } from './strictness.ts'

describe('isLooser', () => {
  it('compares booleans by the stricter value', () => {
    expect(isLooser('higher', 'boolean', undefined, false, true)).toBe(true)
    expect(isLooser('higher', 'boolean', undefined, true, false)).toBe(false)
    expect(isLooser('lower', 'boolean', undefined, true, false)).toBe(true)
    expect(isLooser('lower', 'boolean', undefined, true, true)).toBe(false)
  })
  it('compares numbers, none being the loosest', () => {
    expect(isLooser('lower', 'limit', undefined, 30, 20)).toBe(true)
    expect(isLooser('lower', 'limit', undefined, 20, 20)).toBe(false)
    expect(isLooser('lower', 'limit', undefined, 5, 20)).toBe(false)
    expect(isLooser('lower', 'limit', undefined, null, 20)).toBe(true)
    expect(isLooser('lower', 'limit', undefined, 5, null)).toBe(false)
    expect(isLooser('higher', 'limit', undefined, 5, 20)).toBe(true)
  })
  it('orders choices from loosest to strictest', () => {
    const c = ['open', 'invite', 'closed']
    expect(isLooser('order', 'choice', c, 'open', 'invite')).toBe(true)
    expect(isLooser('order', 'choice', c, 'closed', 'invite')).toBe(false)
    expect(isLooser('order', 'choice', c, 'invite', 'invite')).toBe(false)
  })
  it('holds a list to a subset, with * for everything', () => {
    expect(isLooser('subset', 'list', undefined, ['a'], ['a', 'b'])).toBe(false)
    expect(isLooser('subset', 'list', undefined, ['a', 'c'], ['a', 'b'])).toBe(true)
    expect(isLooser('subset', 'list', undefined, ['*'], ['a'])).toBe(true)
    expect(isLooser('subset', 'list', undefined, ['a'], ['*'])).toBe(false)
  })
  it('has nothing to compare without an order', () => {
    expect(isLooser(undefined, 'limit', undefined, 99, 1)).toBe(false)
  })
})
