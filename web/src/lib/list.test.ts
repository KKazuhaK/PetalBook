import { describe, expect, it } from 'vitest'
import { joinList } from './list'

describe('joinList', () => {
  it('joins the way each language writes a list', () => {
    expect(joinList(['a', 'b', 'c'], 'en')).toBe('a, b, and c')
    expect(joinList(['甲', '乙', '丙'], 'zh-CN')).toBe('甲、乙和丙')
    expect(joinList(['a'], 'en')).toBe('a')
    expect(joinList([], 'en')).toBe('')
  })
})
