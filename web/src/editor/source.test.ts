import { describe, expect, it } from 'vitest'
import type { Page } from '@/api/types'
import { buildSave, fieldsOf } from './saveController'

const base: Page = { id: 'P', path: 'a.md', folder: '', rev: 'r1', updated: '', meta: { id: 'P', title: '暮雪', source: '《雪国》' }, body: 'x\n' }
const same = () => base.body

describe('the source field', () => {
  it('is read from the header and written back only when edited', () => {
    expect(fieldsOf(base.meta).source).toBe('《雪国》')
    expect(buildSave(base, { ...fieldsOf(base.meta) }, same)).toBeNull()
    expect(buildSave(base, { ...fieldsOf(base.meta), source: ' 川端康成 ' }, same)?.meta.source).toBe('川端康成')
  })

  it('emptied, the key is removed rather than written empty', () => {
    const out = buildSave(base, { ...fieldsOf(base.meta), source: '' }, same)
    expect(out && 'source' in out.meta).toBe(false)
  })

  it('a draft from before the field existed (no source) leaves the header source alone', () => {
    const { source: _s, ...old } = fieldsOf(base.meta)
    void _s
    expect(buildSave(base, old, same)).toBeNull()
    expect(buildSave(base, { ...old, title: '暮雪二' }, same)?.meta.source).toBe('《雪国》')
  })
})
