import { describe, expect, it } from 'vitest'
import type { Page } from '@/api/types'
import { buildSave, fieldsOf } from './saveController'

const base: Page = {
  id: 'P',
  path: 'a.md',
  folder: '',
  rev: 'r1',
  updated: '',
  meta: { id: 'P', title: '暮雪', source: '《雪国》' },
  body: 'x\n',
}
const same = () => base.body

describe('the source field', () => {
  it('is a list; one text written by hand reads as one source, and is written back only when edited', () => {
    expect(fieldsOf(base.meta).source).toEqual(['《雪国》'])
    expect(buildSave(base, { ...fieldsOf(base.meta) }, same)).toBeNull()
    expect(buildSave(base, { ...fieldsOf(base.meta), source: [' 川端康成 ', '书'] }, same)?.meta.source).toEqual([
      '川端康成',
      '书',
    ])
  })

  it('reads a list, drops blanks and repeats when written, and keeps what is not text', () => {
    const listed = { ...base, meta: { id: 'P', source: ['书', '电影', null] } }
    expect(fieldsOf(listed.meta).source).toEqual(['书', '电影'])
    const out = buildSave(listed, { ...fieldsOf(listed.meta), source: ['书', ' 音乐 ', '音乐', ''] }, same)
    expect(out?.meta.source).toEqual(['书', '音乐', null])
  })

  it('emptied, the key is removed rather than written empty', () => {
    const out = buildSave(base, { ...fieldsOf(base.meta), source: [] }, same)
    expect(out && 'source' in out.meta).toBe(false)
  })
})
