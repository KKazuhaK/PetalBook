import { describe, expect, it } from 'vitest'
import { blockLines, blockToLine, lineToBlock } from './scrollMap'
import { markdownToDoc } from '@/markdown'

describe('blockLines', () => {
  const cases = [
    '# 标题\n\n第一段\n第二行\n\n- a\n- b\n\n> 引用\n\n```\ncode\n```\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\n---\n\n结尾\n',
    '- [ ] 待办\n- 普通\n- [x] 完成\n\n1. 一\n2. 二\n',
    '[a][ref]\n\n[ref]: https://example.com\n\n后一段\n',
    '脚注[^1]\n\n[^1]: 说明\n\n<div>raw</div>\n\n<br>\n\n完\n',
    '',
  ]
  it('has one line per top-level block of the editor document', () => {
    for (const md of cases) expect(blockLines(md).length, md).toBe(md.trim() === '' ? 0 : markdownToDoc(md).content!.length)
  })
  it('gives the line each block starts on', () => {
    expect(blockLines('# 标题\n\n第一段\n第二行\n\n- a\n- b\n')).toEqual([1, 3, 6])
    expect(blockLines('- [ ] 待办\n- 普通\n- [x] 完成\n')).toEqual([1, 2, 3])
  })
})

describe('line <-> block', () => {
  const lines = [1, 3, 6]
  it('finds the block and how far through it a line is', () => {
    expect(lineToBlock(lines, 1, 7)).toEqual({ index: 0, frac: 0 })
    expect(lineToBlock(lines, 2, 7)).toEqual({ index: 0, frac: 0.5 })
    expect(lineToBlock(lines, 4.5, 7)).toEqual({ index: 1, frac: 0.5 })
    expect(lineToBlock(lines, 7, 7)).toEqual({ index: 2, frac: 0.5 })
    expect(lineToBlock(lines, 99, 7)).toEqual({ index: 2, frac: 1 })
    expect(lineToBlock([], 5, 7)).toEqual({ index: 0, frac: 0 })
  })
  it('goes back to the same line', () => {
    for (const l of [1, 2, 3, 4.5, 6, 7.25]) expect(blockToLine(lines, lineToBlock(lines, l, 7), 7)).toBeCloseTo(l)
    expect(blockToLine(lines, { index: 9, frac: 0 }, 7)).toBe(6)
  })
})
