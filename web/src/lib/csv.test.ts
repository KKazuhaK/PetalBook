import { describe, expect, it } from 'vitest'
import { parseCsv, readPeopleCsv } from './csv'

describe('parseCsv', () => {
  it('reads quoted fields, doubled quotes, a byte order mark and any line ending', () => {
    expect(parseCsv('﻿a,"b, c","say ""hi"""\r\nd,e,f\n')).toEqual([
      ['a', 'b, c', 'say "hi"'],
      ['d', 'e', 'f'],
    ])
  })

  it('takes the delimiter from the first line, and skips blank lines', () => {
    expect(parseCsv('a;b\n\nc;d')).toEqual([
      ['a', 'b'],
      ['c', 'd'],
    ])
    expect(parseCsv('a\tb')).toEqual([['a', 'b']])
  })
})

describe('readPeopleCsv', () => {
  it('finds the columns by the words of the first row, in any order', () => {
    const { rows, headed } = readPeopleCsv('Role,Email,Name,Groups\neditor,a@x.test,Ann,"Staff; Ops"\n')
    expect(headed).toBe(true)
    expect(rows).toEqual([{ user: 'a@x.test', role: 'editor', name: 'Ann', groups: ['Staff', 'Ops'] }])
  })

  it('reads the Chinese column names too', () => {
    expect(readPeopleCsv('邮箱,角色\na@x.test,viewer').rows).toEqual([{ user: 'a@x.test', role: 'viewer' }])
  })

  it('takes a file with no header as user, role, name, groups', () => {
    const { rows, headed } = readPeopleCsv('tang,viewer\nwu')
    expect(headed).toBe(false)
    expect(rows).toEqual([{ user: 'tang', role: 'viewer' }, { user: 'wu' }])
  })

  it('has no rows for an empty file', () => {
    expect(readPeopleCsv('').rows).toEqual([])
  })
})
