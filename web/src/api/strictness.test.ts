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
  it('holds a list of roles to a superset: more is stricter', () => {
    expect(isLooser('superset', 'list', undefined, ['admin', 'member'], ['admin'])).toBe(false)
    expect(isLooser('superset', 'list', undefined, ['member'], ['admin'])).toBe(true)
    expect(isLooser('superset', 'list', undefined, [], ['member'])).toBe(true)
    expect(isLooser('superset', 'list', undefined, ['admin'], [])).toBe(false)
  })
  it('compares the captcha forms one by one', () => {
    const on = { on: true, when: 'after_failures', failures: 3 }
    const off = { on: false, when: 'after_failures', failures: 3 }
    const always = { on: true, when: 'always', failures: 3 }
    const cmp = (v: unknown, f: unknown) => isLooser('document', 'doc', undefined, v, f)
    expect(cmp({ login: off }, { login: on })).toBe(true)
    expect(cmp({}, { login: on })).toBe(true)
    expect(cmp({ login: on }, { login: off })).toBe(false)
    expect(cmp({ login: on }, { login: always })).toBe(true)
    expect(cmp({ login: always }, { login: on })).toBe(false)
    expect(cmp({ login: { ...on, failures: 9 } }, { login: on })).toBe(true)
    expect(cmp({ login: { ...on, failures: 1 } }, { login: on })).toBe(false)
    expect(cmp({ forgot: on }, { login: off })).toBe(false)
  })
  it('has nothing to compare without an order', () => {
    expect(isLooser(undefined, 'limit', undefined, 99, 1)).toBe(false)
  })
})
