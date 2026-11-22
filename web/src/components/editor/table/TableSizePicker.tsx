import { useState, type KeyboardEvent } from 'react'
import { t } from '@/i18n'
import { DEFAULT_SIZE, MAX_COLS, MAX_ROWS, moveSize, sizeAtCell, sizeLabel, type GridSize } from './tableGrid'

// A grid of cells; the table is as large as the corner the pointer is on.
// From the keyboard the grid takes the arrow keys and Enter, and says the
// current size aloud as it changes.
export function TableSizePicker({ onPick }: { onPick: (size: GridSize) => void }) {
  const [size, setSize] = useState<GridSize>(DEFAULT_SIZE)

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onPick(size)
    } else if (['ArrowRight', 'ArrowLeft', 'ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) {
      e.preventDefault()
      setSize((s) => moveSize(s, e.key))
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div
        tabIndex={0}
        role="group"
        aria-label={t.toolbar.tableGrid}
        autoFocus
        onKeyDown={onKeyDown}
        className="grid gap-0.5 rounded-md p-0.5 outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        style={{ gridTemplateColumns: `repeat(${MAX_COLS}, 1.25rem)` }}
      >
        {Array.from({ length: MAX_ROWS * MAX_COLS }, (_, i) => {
          const col = i % MAX_COLS
          const row = Math.floor(i / MAX_COLS)
          const inside = col < size.cols && row < size.rows
          return (
            <button
              key={i}
              type="button"
              tabIndex={-1}
              aria-label={t.toolbar.insertTableSized(col + 1, row + 1)}
              onMouseEnter={() => setSize(sizeAtCell(col, row))}
              onClick={() => onPick(sizeAtCell(col, row))}
              className={`size-5 rounded-[3px] border ${inside ? 'border-brand bg-brand/20' : 'border-border bg-background'}`}
            />
          )
        })}
      </div>
      <p role="status" aria-live="polite" className="text-center text-xs text-muted-foreground tabular-nums">
        {sizeLabel(size)}
      </p>
    </div>
  )
}
