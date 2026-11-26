// The arithmetic behind a table's columns, kept apart from the component so it can be tested without a browser:
// where a dragged column lands, which neighbour a keyboard move swaps with, and how a width changes while a column's
// edge is dragged. Widths are shares of the room the columns share (0 to 1), never pixels.

// Takes `id` out of `order` and puts it before or after `ref`. A column that is not in the list, or is `ref` itself,
// leaves the order as it was. Ids in `pinned` stay where they are: they cannot be moved, nor be the reference.
export function moveColumn(
  order: readonly string[],
  id: string,
  ref: string,
  side: 'before' | 'after',
  pinned: readonly string[] = [],
): string[] {
  if (id === ref || !order.includes(id) || !order.includes(ref)) return [...order]
  if (pinned.includes(id) || pinned.includes(ref)) return [...order]
  // The movable ones are moved among themselves; the pinned keep their own places.
  const free = order.filter((c) => !pinned.includes(c))
  const rest = free.filter((c) => c !== id)
  const at = rest.indexOf(ref) + (side === 'after' ? 1 : 0)
  rest.splice(at, 0, id)
  let k = 0
  return order.map((c) => (pinned.includes(c) ? c : rest[k++]))
}

// One step by keyboard: `id` swaps places with the next column to its left or right among those `shown` (the order
// holds the hidden ones too, which stay out of the way). At either end nothing moves.
export function stepColumn(
  order: readonly string[],
  shown: readonly string[],
  id: string,
  by: -1 | 1,
  pinned: readonly string[] = [],
): string[] {
  const i = shown.indexOf(id)
  const next = shown[i + by]
  if (i < 0 || next === undefined) return [...order]
  return moveColumn(order, id, next, by < 0 ? 'before' : 'after', pinned)
}

// Where a dragged column would drop, as a gap among the shown ones: 0 is before the first, n after the last. `mids`
// are the horizontal middles of the shown headers; the gap is the first whose middle is right of the pointer.
export function dropGap(mids: readonly number[], x: number): number {
  const i = mids.findIndex((m) => x < m)
  return i < 0 ? mids.length : i
}

// The full order after dropping `id` into gap `gap` of the `shown` columns (see dropGap).
export function dropColumn(
  order: readonly string[],
  shown: readonly string[],
  id: string,
  gap: number,
  pinned: readonly string[] = [],
): string[] {
  if (shown.length === 0) return [...order]
  if (gap >= shown.length) return moveColumn(order, id, shown[shown.length - 1], 'after', pinned)
  return moveColumn(order, id, shown[Math.max(0, gap)], 'before', pinned)
}

// The room the shares are of: the table's width less the columns that have a width of their own.
export const roomOf = (tableWidth: number, fixed: number) => Math.max(0, tableWidth - fixed)

// Whole pixels for each share, the last taking what rounding left, so the columns add up to the room exactly.
export function pixelWidths(
  ids: readonly string[],
  have: Record<string, number>,
  room: number,
): Record<string, number> {
  const out: Record<string, number> = {}
  let used = 0
  ids.forEach((id, i) => {
    const w = i === ids.length - 1 ? Math.max(0, room - used) : Math.round((have[id] ?? 0) * room)
    out[id] = w
    used += w
  })
  return out
}

// Drags the edge between two neighbours by `deltaPx`: one grows, the other shrinks by the same, so the table's width
// and the other columns do not change. Neither goes below its least width (a column already below it is not pushed).
export function resizePair(
  have: Record<string, number>,
  left: string,
  right: string,
  deltaPx: number,
  room: number,
  minLeftPx: number,
  minRightPx: number,
): Record<string, number> {
  if (room <= 0) return { ...have }
  const lo = Math.min(0, minLeftPx / room - have[left])
  const hi = Math.max(0, have[right] - minRightPx / room)
  const d = Math.max(lo, Math.min(hi, deltaPx / room))
  return { ...have, [left]: have[left] + d, [right]: have[right] - d }
}

// Puts two neighbours back to the proportion of their default weights, keeping what they share between them.
export function resetPair(
  have: Record<string, number>,
  left: string,
  right: string,
  weightLeft: number,
  weightRight: number,
): Record<string, number> {
  const both = have[left] + have[right]
  const w = weightLeft + weightRight || 1
  return { ...have, [left]: (both * weightLeft) / w, [right]: (both * weightRight) / w }
}

// The shares with every column raised to its least width (`mins`, in pixels, of `room`) and the others scaled down to
// pay for it, so what was saved before a column's least width changed, or by a screen of another size, still fits.
export function clampShares(
  have: Record<string, number>,
  mins: Record<string, number>,
  room: number,
): Record<string, number> {
  const ids = Object.keys(have)
  if (room <= 0 || ids.length === 0) return { ...have }
  const out = { ...have }
  const locked = new Set<string>()
  for (let guard = 0; guard <= ids.length; guard++) {
    const kept = [...locked].reduce((n, id) => n + mins[id] / room, 0)
    const free = ids.filter((id) => !locked.has(id))
    const freeSum = free.reduce((n, id) => n + have[id], 0)
    const left = 1 - kept
    if (free.length === 0 || left <= 0 || freeSum <= 0) break
    for (const id of free) out[id] = (have[id] * left) / freeSum
    const low = free.filter((id) => out[id] < mins[id] / room - 1e-9)
    if (low.length === 0) break
    low.forEach((id) => locked.add(id))
  }
  for (const id of locked) out[id] = mins[id] / room
  return out
}

// A column as the auto fit sees it: `content` is the widest of its header and its cells on this page, in pixels with
// the cell's padding (0 when not measured), `min` the least width, `max` the most an auto width gives (a long text is
// cut there and ends in an ellipsis), `weight` its claim on whatever room is left over.
export interface FitColumn {
  id: string
  content: number
  min: number
  max: number
  weight: number
}

// The starting widths of columns nobody has resized: each as wide as its content, never below its least width nor
// above its cap (a cap below the least width gives way to it). What is left of `room` is shared by the weights. When
// the content is wider than the room the columns over their least width give up the difference in proportion to what
// they have over it (their text ends in an ellipsis), so the table scrolls only when the least widths alone do not
// fit. Whole pixels, adding up to `total`: `room`, or the least widths' sum when that is more.
export function autoWidths(
  cols: readonly FitColumn[],
  room: number,
): { widths: Record<string, number>; total: number } {
  const base = cols.map((c) => Math.ceil(Math.min(Math.max(c.content, c.min), Math.max(c.max, c.min))))
  const used = base.reduce((n, w) => n + w, 0)
  const whole = Math.floor(room)
  const widths: Record<string, number> = {}
  if (used > whole) {
    const least = cols.map((c) => Math.ceil(c.min))
    const floor = least.reduce((n, w) => n + w, 0)
    const target = Math.max(whole, floor)
    const over = base.map((w, i) => Math.max(0, w - least[i]))
    const spare = over.reduce((n, w) => n + w, 0) || 1
    const out = base.map((w, i) => w - Math.floor(((used - target) * over[i]) / spare))
    // What rounding left over comes off the widest of those still above their least width.
    let rest = out.reduce((n, w) => n + w, 0) - target
    while (rest > 0) {
      let at = -1
      out.forEach((w, i) => {
        if (w > least[i] && (at < 0 || w > out[at])) at = i
      })
      if (at < 0) break
      out[at]--
      rest--
    }
    cols.forEach((c, i) => (widths[c.id] = out[i]))
    return { widths, total: target }
  }
  const left = whole - used
  const weights = cols.reduce((n, c) => n + c.weight, 0) || 1
  let given = 0
  cols.forEach((c, i) => {
    const extra = i === cols.length - 1 ? left - given : Math.floor((left * c.weight) / weights)
    given += extra
    widths[c.id] = base[i] + extra
  })
  return { widths, total: whole }
}

// The shares after one column is set to `target` pixels of `room`: the others keep their proportions among
// themselves and pay (or take) the difference. A column that is alone keeps what it has.
export function fitColumn(
  have: Record<string, number>,
  id: string,
  target: number,
  room: number,
  mins: Record<string, number>,
): Record<string, number> {
  const others = Object.keys(have).filter((k) => k !== id)
  if (room <= 0 || others.length === 0) return { ...have }
  const keep = others.reduce((n, k) => n + mins[k], 0)
  const px = Math.max(mins[id], Math.min(target, room - keep))
  const rest = others.reduce((n, k) => n + have[k], 0) || 1
  const out: Record<string, number> = { [id]: px / room }
  for (const k of others) out[k] = (have[k] / rest) * (1 - px / room)
  return clampShares(out, mins, room)
}

// The least width of each column: what it declares, unless the box is narrow and those together do not fit the room,
// when each may go down to its narrow least width (never above its declared one).
export function leastWidths(
  cols: readonly { id: string; min: number; narrowMin: number }[],
  room: number,
  narrow: boolean,
): Record<string, number> {
  const fits = cols.reduce((n, c) => n + c.min, 0) <= room
  const out: Record<string, number> = {}
  for (const c of cols) out[c.id] = narrow && !fits ? Math.min(c.min, c.narrowMin) : c.min
  return out
}
