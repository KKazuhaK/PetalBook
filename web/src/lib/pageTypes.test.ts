import { describe, expect, it } from 'vitest'
import { bindingSource, fieldKeyProblem, filterOps, fits, folderTypeName, hasValue, isDay, propsOfMeta, typeNameProblem } from './pageTypes'

describe('the type a folder has', () => {
  const bindings = [
    { folder: 'Work', type: 'task' },
    { folder: 'Work/Notes/Deep', type: 'memo' },
  ]
  it('is its own binding, else the nearest one above it', () => {
    expect(folderTypeName(bindings, 'Work')).toBe('task')
    expect(folderTypeName(bindings, 'Work/Sub')).toBe('task')
    expect(folderTypeName(bindings, 'Work/Notes/Deep/Deeper')).toBe('memo')
    expect(folderTypeName(bindings, 'Work/Notes')).toBe('task')
    expect(folderTypeName(bindings, 'Play')).toBe('')
    expect(folderTypeName(bindings, '')).toBe('')
  })
  it('takes the root into account', () => {
    const root = [{ folder: '', type: 'all' }, ...bindings]
    expect(folderTypeName(root, 'Play/Deep')).toBe('all')
    expect(folderTypeName(root, '')).toBe('all')
    expect(folderTypeName(root, 'Work')).toBe('task')
  })
  it('says which folder the binding comes from', () => {
    expect(bindingSource(bindings, 'Work/Sub')).toBe('Work')
    expect(bindingSource(bindings, 'Work')).toBe('Work')
    expect(bindingSource(bindings, 'Play')).toBeNull()
    expect(bindingSource([{ folder: '', type: 'all' }], 'A/B')).toBe('')
  })
})

describe('whether a value fits its field', () => {
  const select = { kind: 'select' as const, options: ['a', 'b'] }
  it('checks each kind', () => {
    expect(fits({ kind: 'text' }, 'x')).toBe(true)
    expect(fits({ kind: 'text' }, 5)).toBe(false)
    expect(fits(select, 'a')).toBe(true)
    expect(fits(select, 'c')).toBe(false)
    expect(fits({ kind: 'multiselect', options: ['a', 'b'] }, ['a', 'b'])).toBe(true)
    expect(fits({ kind: 'multiselect', options: ['a', 'b'] }, ['a', 'z'])).toBe(false)
    expect(fits({ kind: 'multiselect', options: ['a', 'b'] }, 'a')).toBe(false)
    expect(fits({ kind: 'date' }, '2026-02-28')).toBe(true)
    expect(fits({ kind: 'date' }, '2026-02-30')).toBe(false)
    expect(fits({ kind: 'date' }, 'tomorrow')).toBe(false)
    expect(fits({ kind: 'number' }, 0)).toBe(true)
    expect(fits({ kind: 'number' }, '3')).toBe(false)
    expect(fits({ kind: 'checkbox' }, false)).toBe(true)
    expect(fits({ kind: 'checkbox' }, 'yes')).toBe(false)
    expect(fits({ kind: 'url' }, 'https://example.com')).toBe(true)
    expect(fits({ kind: 'url' }, 'example')).toBe(false)
    expect(fits({ kind: 'other' }, { any: 'thing' })).toBe(true)
  })
  it('counts false and 0 as values, and blank text and empty lists as none', () => {
    expect(hasValue(false)).toBe(true)
    expect(hasValue(0)).toBe(true)
    expect(hasValue('')).toBe(false)
    expect(hasValue([])).toBe(false)
    expect(hasValue(null)).toBe(false)
    expect(hasValue(undefined)).toBe(false)
  })
  it('knows a day when it sees one', () => {
    expect(isDay('2026-11-22')).toBe(true)
    expect(isDay('2026-13-01')).toBe(false)
    expect(isDay(20261122)).toBe(false)
  })
})

describe('the properties a summary holds', () => {
  it('are the header keys that are not built in, as plain values', () => {
    expect(
      propsOfMeta({
        id: 'X',
        title: 'T',
        tags: ['a'],
        created: '2026-01-01',
        type: 'task',
        status: 'done',
        points: 0,
        done: false,
        labels: ['a', { x: 1 }, 'b'],
        nested: { a: 1 },
        blank: '  ',
        none: null,
        empty: [],
      }),
    ).toEqual({ status: 'done', points: 0, done: false, labels: ['a', 'b'] })
  })
  it('cut long text at 300 characters', () => {
    expect((propsOfMeta({ long: 'あ'.repeat(400) }).long as string).length).toBe(300)
  })
})

describe('names and keys', () => {
  it('a type name follows the server\'s rules, and two names that differ in case are one', () => {
    expect(typeNameProblem('task', [])).toBeNull()
    expect(typeNameProblem('文案', [])).toBeNull()
    expect(typeNameProblem('  ', [])).toBe('empty')
    expect(typeNameProblem('a:b', [])).toBe('chars')
    expect(typeNameProblem('.hidden', [])).toBe('chars')
    expect(typeNameProblem('end.', [])).toBe('chars')
    expect(typeNameProblem('x'.repeat(101), [])).toBe('long')
    expect(typeNameProblem('Task', ['task'])).toBe('exists')
  })
  it('a field key may be Chinese but not built in, reserved, or hold a colon', () => {
    expect(fieldKeyProblem('状态')).toBeNull()
    expect(fieldKeyProblem('')).toBe('empty')
    expect(fieldKeyProblem('tags')).toBe('builtin')
    expect(fieldKeyProblem('slug')).toBe('builtin')
    expect(fieldKeyProblem('a:b')).toBe('chars')
    expect(fieldKeyProblem('k'.repeat(65))).toBe('long')
  })
})

describe('the filters a kind offers', () => {
  it('suit what can be said about it', () => {
    expect(filterOps('select')).toEqual(['in', 'empty', 'notEmpty'])
    expect(filterOps('number')).toContain('range')
    expect(filterOps('date')).toContain('range')
    expect(filterOps('text')).toContain('contains')
    expect(filterOps('other')).toEqual(['empty', 'notEmpty'])
  })
})
