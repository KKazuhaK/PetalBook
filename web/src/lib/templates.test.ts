import { describe, expect, it } from 'vitest'
import { defaultFor, draftOf, metaOf, otherKeys, tagList, templateLabel, templateNameProblem, tzOffset } from './templates'

describe('the time zone sent to the server', () => {
  it('is minutes east of UTC, the opposite of what Date reports', () => {
    expect(tzOffset() + new Date().getTimezoneOffset()).toBe(0)
  })
})

describe('the name of a template', () => {
  it('follows the rules of a page name', () => {
    expect(templateNameProblem('', [])).toBe('empty')
    expect(templateNameProblem('a'.repeat(81), [])).toBe('long')
    expect(templateNameProblem('a/b', [])).toBe('chars')
    expect(templateNameProblem('.hidden', [])).toBe('chars')
    expect(templateNameProblem('end.', [])).toBe('chars')
    expect(templateNameProblem('defaults', [])).toBe('reserved')
    expect(templateNameProblem('nul', [])).toBe('reserved')
    expect(templateNameProblem('日记', ['日记'])).toBe('exists')
    expect(templateNameProblem('Diary', ['diary'])).toBe('exists')
    expect(templateNameProblem('周报', ['日记'])).toBeNull()
  })
})

describe('the header of a template as a form', () => {
  it('reads the fields the form has, and keeps the others as they are', () => {
    const meta = { title: 'T', tags: ['a', 'b'], type: 'task', status: 'todo', id: 'x', created: '2026-01-01' }
    expect(draftOf(meta)).toEqual({ title: 'T', subtitle: '', type: 'task', tags: 'a, b' })
    expect(otherKeys(meta)).toEqual(['status'])
    expect(metaOf(meta, { title: ' N ', subtitle: '', type: '', tags: 'x，y, x,' })).toEqual({ id: 'x', created: '2026-01-01', status: 'todo', title: 'N', tags: ['x', 'y'] })
  })
  it('leaves empty fields out', () => {
    expect(metaOf(undefined, draftOf(undefined))).toEqual({})
    expect(tagList(' , ')).toEqual([])
  })
})

describe('the template a folder starts from', () => {
  const list = [
    { name: '日记', path: '', rev: '', meta: { title: '日' } },
    { name: '会议', path: '', rev: '', meta: {} },
  ]
  it('is its default, when that template is still there', () => {
    const defaults = { folders: { 随笔: '日记', 旧: '没了' }, quickNote: '' }
    expect(defaultFor('随笔', defaults, list)).toBe('日记')
    expect(defaultFor('旧', defaults, list)).toBe('')
    expect(defaultFor('别处', defaults, list)).toBe('')
    expect(defaultFor('随笔', undefined, list)).toBe('')
  })
  it('is shown by its name, with its title when it has another', () => {
    expect(templateLabel(list[0])).toBe('日记 · 日')
    expect(templateLabel(list[1])).toBe('会议')
  })
})
