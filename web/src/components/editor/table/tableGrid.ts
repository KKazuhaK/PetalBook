// The size picker of "insert table": a grid of cells where the size is the
// corner the pointer (or the arrow keys) is on.

export const MAX_COLS = 10
export const MAX_ROWS = 8

export interface GridSize {
  cols: number
  rows: number
}

// Where the keyboard starts: the size the old menu entry used to insert.
export const DEFAULT_SIZE: GridSize = { cols: 3, rows: 3 }

const clamp = (n: number, max: number) => Math.min(Math.max(Math.round(n), 1), max)

export const clampSize = ({ cols, rows }: GridSize): GridSize => ({
  cols: clamp(cols, MAX_COLS),
  rows: clamp(rows, MAX_ROWS),
})

// The size picked by pointing at the cell in column `col` and row `row` (both from 0).
export const sizeAtCell = (col: number, row: number): GridSize => clampSize({ cols: col + 1, rows: row + 1 })

// "3 × 4": columns, then rows. The first row is the header.
export const sizeLabel = ({ cols, rows }: GridSize) => `${cols} × ${rows}`

// An arrow key moves the picked corner; Home and End jump to the smallest and largest table.
export function moveSize(size: GridSize, key: string): GridSize {
  switch (key) {
    case 'ArrowRight':
      return clampSize({ ...size, cols: size.cols + 1 })
    case 'ArrowLeft':
      return clampSize({ ...size, cols: size.cols - 1 })
    case 'ArrowDown':
      return clampSize({ ...size, rows: size.rows + 1 })
    case 'ArrowUp':
      return clampSize({ ...size, rows: size.rows - 1 })
    case 'Home':
      return { cols: 1, rows: 1 }
    case 'End':
      return { cols: MAX_COLS, rows: MAX_ROWS }
    default:
      return size
  }
}
