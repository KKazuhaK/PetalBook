import { describe, expect, it } from 'vitest'
import { clampSize, DEFAULT_SIZE, MAX_COLS, MAX_ROWS, moveSize, sizeAtCell, sizeLabel } from './tableGrid'

describe('picking a table size', () => {
  it('is the corner pointed at, counted from 1', () => {
    expect(sizeAtCell(0, 0)).toEqual({ cols: 1, rows: 1 })
    expect(sizeAtCell(2, 3)).toEqual({ cols: 3, rows: 4 })
    expect(sizeAtCell(MAX_COLS - 1, MAX_ROWS - 1)).toEqual({ cols: 10, rows: 8 })
  })
  it('never leaves 1 to 10 columns and 1 to 8 rows', () => {
    expect(clampSize({ cols: 0, rows: -3 })).toEqual({ cols: 1, rows: 1 })
    expect(clampSize({ cols: 40, rows: 40 })).toEqual({ cols: 10, rows: 8 })
    expect(sizeAtCell(99, 99)).toEqual({ cols: 10, rows: 8 })
  })
  it('is labelled columns × rows', () => {
    expect(sizeLabel({ cols: 3, rows: 4 })).toBe('3 × 4')
  })
})

describe('the arrow keys', () => {
  it('move one cell and stop at the edges', () => {
    expect(moveSize({ cols: 3, rows: 3 }, 'ArrowRight')).toEqual({ cols: 4, rows: 3 })
    expect(moveSize({ cols: 3, rows: 3 }, 'ArrowDown')).toEqual({ cols: 3, rows: 4 })
    expect(moveSize({ cols: 1, rows: 1 }, 'ArrowLeft')).toEqual({ cols: 1, rows: 1 })
    expect(moveSize({ cols: 1, rows: 1 }, 'ArrowUp')).toEqual({ cols: 1, rows: 1 })
    expect(moveSize({ cols: 10, rows: 8 }, 'ArrowRight')).toEqual({ cols: 10, rows: 8 })
    expect(moveSize({ cols: 10, rows: 8 }, 'ArrowDown')).toEqual({ cols: 10, rows: 8 })
  })
  it('Home and End jump to the smallest and the largest', () => {
    expect(moveSize(DEFAULT_SIZE, 'Home')).toEqual({ cols: 1, rows: 1 })
    expect(moveSize(DEFAULT_SIZE, 'End')).toEqual({ cols: 10, rows: 8 })
  })
  it('ignore other keys', () => {
    expect(moveSize(DEFAULT_SIZE, 'a')).toBe(DEFAULT_SIZE)
  })
})
