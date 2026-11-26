// @vitest-environment jsdom
import { useState } from 'react'
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { DataTable, RowAction, type Column, type DataTableSort } from '@/components/ui/data-table'
import { readPrefs, shares, sanitize, useTablePrefs, orderIds, PAGE_SIZES } from '@/lib/tablePrefs'
import { tc } from '@/i18n/console'
import { click, installDomShims, mount, press, settle, type, type Mounted } from '@/testing/harness'

// The table that every long list of the administration is made of: its columns, widths, density and page size are
// the reader's, kept in this browser only; what does not fit leaves by priority, and the rest scrolls in its own box.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

interface Row {
  id: string
  name: string
  role: string
  mail: string
  seen: string
}
const ROWS: Row[] = [
  { id: 'a', name: 'Ann', role: 'admin', mail: 'ann@x.test', seen: 'today' },
  { id: 'b', name: 'Bo', role: 'member', mail: 'bo@x.test', seen: 'never' },
]
const COLUMNS: Column<Row>[] = [
  { id: 'name', header: 'Name', required: true, min: 120, sortKey: 'name', cell: (r) => r.name },
  { id: 'role', header: 'Role', min: 100, priority: 2, cell: (r) => r.role },
  { id: 'mail', header: 'Mail', min: 160, priority: 4, cell: (r) => r.mail },
  { id: 'seen', header: 'Seen', min: 100, priority: 3, sortKey: 'seen', cell: (r) => r.seen },
]

function Probe(props: {
  rows?: Row[]
  onSort?: (s: DataTableSort) => void
  onPage?: (d: 'next' | 'prev') => void
  hasNext?: boolean
  hasPrev?: boolean
  selected?: Set<string>
  onSelect?: (s: Set<string>) => void
  error?: string
}) {
  const prefs = useTablePrefs('probe')
  const [sort, setSort] = useState<DataTableSort>({ key: 'name', order: 'asc' })
  return (
    <>
      <output data-testid="size">{prefs.prefs.pageSize}</output>
      <DataTable
        prefs={prefs}
        columns={COLUMNS}
        rows={props.rows ?? ROWS}
        rowId={(r) => r.id}
        rowLabel={(r) => r.name}
        empty="Nobody."
        error={props.error}
        paging={{
          page: 1,
          hasPrev: props.hasPrev ?? false,
          hasNext: props.hasNext ?? false,
          onPrev: () => props.onPage?.('prev'),
          onNext: () => props.onPage?.('next'),
        }}
        sort={sort}
        onSort={(s) => {
          setSort(s)
          props.onSort?.(s)
        }}
        selection={props.onSelect ? { selected: props.selected ?? new Set(), onChange: props.onSelect } : undefined}
        bulk={<div data-slot="bulk">bulk</div>}
        actions={(r) => <button type="button">act {r.name}</button>}
      />
    </>
  )
}

const headers = () => [...document.querySelectorAll('thead th[data-column]')].map((h) => h.getAttribute('data-column'))
const open = async () => {
  await click(document.querySelector('[data-slot="table-settings"]'))
  await settle(() => document.querySelector('[data-slot="popover-content"]') !== null, 'the settings')
}
const saved = () => JSON.parse(localStorage.getItem('pb.table.probe') ?? 'null')

describe('the table', () => {
  it('shows the columns and a row each, the actions at the right end, and a ghost icon for the settings', async () => {
    m = await mount(<Probe />)
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
    expect(document.querySelectorAll('tbody tr[data-row]')).toHaveLength(2)
    expect([...document.querySelectorAll('tbody tr')[0].querySelectorAll('button')].map((b) => b.textContent)).toEqual([
      'act Ann',
    ])
    const settings = document.querySelector('[data-slot="table-settings"]')!
    // No border or fill until hovered: a plain icon, 40px for a finger.
    const plain = settings.className.split(/\s+/).filter((c) => !c.includes(':'))
    expect(plain.filter((c) => /^(border|bg-|ring)/.test(c))).toEqual([])
    expect(settings.className).toContain('size-10')
    expect(settings.getAttribute('aria-label')).toBe(tc.table.settings)
  })

  it('says so when there is nothing, and what went wrong with a way to try again', async () => {
    m = await mount(<Probe rows={[]} />)
    expect(document.body.textContent).toContain('Nobody.')
    await m.unmount()
    m = await mount(<Probe rows={[]} error="Could not load." />)
    expect(document.body.textContent).toContain('Could not load.')
  })

  it('hides and shows a column from the settings, and keeps the choice in this browser only', async () => {
    m = await mount(<Probe />)
    await open()
    const box = (name: string) =>
      [...document.querySelectorAll<HTMLLabelElement>('[data-slot="popover-content"] label')]
        .find((l) => l.textContent === name)!
        .querySelector<HTMLInputElement>('input')!
    // The column that cannot go has a tick that cannot be taken away.
    expect(box('Name').disabled).toBe(true)
    await click(box('Mail'))
    expect(headers()).toEqual(['name', 'role', 'seen'])
    expect(saved().hidden).toEqual(['mail'])
    await click(box('Mail'))
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
    expect(saved().hidden).toEqual([])
  })

  it('reorders the columns, and a saved order is read again when the page opens', async () => {
    m = await mount(<Probe />)
    await open()
    await click(document.querySelector(`button[aria-label="${tc.table.moveUp('Seen')}"]`))
    expect(headers()).toEqual(['name', 'role', 'seen', 'mail'])
    expect(saved().order).toEqual(['name', 'role', 'seen', 'mail'])
    await m.unmount()
    m = await mount(<Probe />)
    expect(headers()).toEqual(['name', 'role', 'seen', 'mail'])
  })

  it('has a density and a page size, and goes back to how it was on reset', async () => {
    m = await mount(<Probe />)
    await open()
    await click(
      [...document.querySelectorAll('[data-slot="popover-content"] button')].find(
        (b) => b.textContent === tc.table.compact,
      ),
    )
    expect(document.querySelector('table')!.getAttribute('data-density')).toBe('compact')
    expect(document.querySelector('tbody tr')!.className).toContain('h-10')
    await type(document.querySelector<HTMLSelectElement>('[data-slot="popover-content"] select')!, '100')
    expect(document.querySelector('[data-testid="size"]')!.textContent).toBe('100')
    expect(saved()).toMatchObject({ density: 'compact', pageSize: 100 })
    await click(
      [...document.querySelectorAll('[data-slot="popover-content"] button')].find(
        (b) => b.textContent === tc.table.reset,
      ),
    )
    expect(document.querySelector('table')!.getAttribute('data-density')).toBe('comfortable')
    expect(document.querySelector('[data-testid="size"]')!.textContent).toBe('50')
    expect(localStorage.getItem('pb.table.probe')).toBeNull()
  })

  it('keeps working when this browser keeps nothing', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('full')
    })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    m = await mount(<Probe />)
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
    await open()
    await click(
      [...document.querySelectorAll<HTMLLabelElement>('[data-slot="popover-content"] label')]
        .find((l) => l.textContent === 'Mail')!
        .querySelector('input'),
    )
    // The choice holds for now, though it cannot be kept.
    expect(headers()).toEqual(['name', 'role', 'seen'])
  })

  it('keeps a width as a share of the table, never as pixels, and never below the least width', async () => {
    m = await mount(<Probe />)
    const handle = document.querySelector<HTMLElement>('[role="separator"]')!
    await press(handle, 'ArrowRight')
    await press(handle, 'ArrowRight')
    const widths = saved().widths as Record<string, number>
    expect(Object.keys(widths).sort()).toEqual(['mail', 'name', 'role', 'seen'])
    for (const v of Object.values(widths)) {
      expect(v).toBeGreaterThan(0)
      expect(v).toBeLessThan(1)
    }
    expect(Object.values(widths).reduce((a, b) => a + b, 0)).toBeCloseTo(1, 5)
    expect(widths.name).toBeGreaterThan(widths.role)
    expect(localStorage.getItem('pb.table.probe')).not.toMatch(/px/)
    // The columns have their widths as shares of the table's width.
    expect(document.querySelector('col:nth-child(1)')!.getAttribute('style')).toContain('%')
  })

  it('takes its listeners off the window when it goes away in the middle of a drag', async () => {
    const added: string[] = []
    const removed: string[] = []
    const add = window.addEventListener.bind(window)
    const remove = window.removeEventListener.bind(window)
    vi.spyOn(window, 'addEventListener').mockImplementation((type, listener, options) => {
      if (type.startsWith('pointer')) added.push(type)
      add(type, listener, options)
    })
    vi.spyOn(window, 'removeEventListener').mockImplementation((type, listener, options) => {
      if (type.startsWith('pointer')) removed.push(type)
      remove(type, listener, options)
    })
    m = await mount(<Probe />)
    const handle = document.querySelector<HTMLElement>('[role="separator"]')!
    await act(async () => {
      handle.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientX: 100 }))
    })
    expect(added.sort()).toEqual(['pointercancel', 'pointermove', 'pointerup'])
    await m.unmount()
    m = undefined
    expect(removed.sort()).toEqual(['pointercancel', 'pointermove', 'pointerup'])
  })

  it('takes the least important columns away first when the box is narrow, and scrolls inside its own box', async () => {
    const width = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(660)
    m = await mount(<Probe />)
    // 660 less the 148 + 48 of the actions and the settings leaves 464: the mail (160) is the one that does not fit.
    expect(headers()).toEqual(['name', 'role', 'seen'])
    expect(document.querySelector('[data-slot="columns-hidden"]')!.textContent).toBe(tc.table.hiddenNarrow(1))
    // What is left still has its least width, and the box is the one that scrolls.
    expect(document.querySelector('table')!.style.minWidth).toBe(`${120 + 100 + 100 + 148 + 48}px`)
    expect(document.querySelector('[data-slot="data-table"] > div.overflow-x-auto')).not.toBeNull()
    width.mockReturnValue(2000)
    window.dispatchEvent(new Event('resize'))
  })

  it('sorts by a header that can be sorted by, and says which way', async () => {
    const seen: DataTableSort[] = []
    m = await mount(<Probe onSort={(s) => seen.push(s)} />)
    const th = (id: string) => document.querySelector(`thead th[data-column="${id}"]`)!
    expect(th('name').getAttribute('aria-sort')).toBe('ascending')
    expect(th('role').getAttribute('aria-sort')).toBeNull()
    await click(th('name').querySelector('button'))
    await click(th('seen').querySelector('button'))
    expect(seen).toEqual([
      { key: 'name', order: 'desc' },
      { key: 'seen', order: 'asc' },
    ])
    expect(th('seen').getAttribute('aria-sort')).toBe('ascending')
  })

  it('ticks rows and the whole page, and shows the bulk bar while something is ticked', async () => {
    const picks: Set<string>[] = []
    function Picking() {
      const [sel, setSel] = useState(new Set<string>())
      return (
        <Probe
          selected={sel}
          onSelect={(s) => {
            picks.push(s)
            setSel(s)
          }}
        />
      )
    }
    m = await mount(<Picking />)
    expect(document.querySelector('[data-slot="bulk"]')).toBeNull()
    await click(document.querySelector(`input[aria-label="${tc.table.selectRow('Bo')}"]`))
    expect([...picks.at(-1)!]).toEqual(['b'])
    expect(document.querySelector('[data-slot="bulk"]')).not.toBeNull()
    await click(document.querySelector(`input[aria-label="${tc.table.selectAll}"]`))
    expect([...picks.at(-1)!].sort()).toEqual(['a', 'b'])
    await click(document.querySelector(`input[aria-label="${tc.table.selectAll}"]`))
    expect(picks.at(-1)!.size).toBe(0)
  })

  it('moves between pages with previous and next, and the buttons are off at the ends', async () => {
    const moves: string[] = []
    m = await mount(<Probe hasNext onPage={(d) => moves.push(d)} />)
    const button = (label: string) =>
      [...document.querySelectorAll('[data-slot="table-footer"] button')].find(
        (b) => b.textContent === label,
      ) as HTMLButtonElement
    expect(button(tc.table.previous).disabled).toBe(true)
    await click(button(tc.table.next))
    expect(moves).toEqual(['next'])
    expect(document.querySelector('[data-slot="table-footer"]')!.textContent).toContain(tc.table.page(1))
    expect(
      [...document.querySelectorAll('[data-slot="table-footer"] option')].map((o) => Number(o.textContent)),
    ).toEqual([...PAGE_SIZES])
  })
})

describe('the saved preferences', () => {
  it('ignores what it cannot read, field by field', () => {
    expect(sanitize(null)).toMatchObject({ hidden: [], order: [], widths: {}, density: 'comfortable', pageSize: 50 })
    expect(
      sanitize({ hidden: 'x', order: ['a', 3], widths: { a: 0.4, b: 7, c: '1', d: 0 }, density: 'huge', pageSize: 77 }),
    ).toMatchObject({ hidden: [], order: [], widths: { a: 0.4 }, density: 'comfortable', pageSize: 50 })
    localStorage.setItem('pb.table.x', '{not json')
    expect(readPrefs('x').pageSize).toBe(50)
  })
  it('orders columns by the saved order, new ones after', () => {
    expect(orderIds(['a', 'b', 'c'], ['c', 'gone', 'a'])).toEqual(['c', 'a', 'b'])
  })
  it('shares the width out: the saved shares, the others by weight, adding up to one', () => {
    const out = shares(
      [
        { id: 'a', weight: 1 },
        { id: 'b', weight: 1 },
        { id: 'c', weight: 2 },
      ],
      {},
    )
    expect(out).toEqual({ a: 0.25, b: 0.25, c: 0.5 })
    const kept = shares(
      [
        { id: 'a', weight: 1 },
        { id: 'b', weight: 1 },
      ],
      { a: 0.75, b: 0.25 },
    )
    expect(kept).toEqual({ a: 0.75, b: 0.25 })
    // A column with no saved share (it was hidden, or a wider screen shows it) gets its weight's share, not its weight.
    const wider = shares(
      [
        { id: 'a', weight: 1 },
        { id: 'b', weight: 1 },
        { id: 'c', weight: 2 },
      ],
      { a: 0.5, b: 0.5 },
    )
    expect(wider.a).toBeCloseTo(0.5 / 1.5)
    expect(wider.c).toBeCloseTo(0.5 / 1.5)
    expect(wider.a + wider.b + wider.c).toBeCloseTo(1)
  })
})

// Dragging, resizing and moving by keyboard. jsdom has no layout, so the headers are given places: column i is 100
// wide from i * 100, and the box and table 1000 wide where a width is wanted.
const pointer = (type: string, target: EventTarget, x: number, kind: 'mouse' | 'touch' = 'mouse') =>
  act(async () => {
    const e = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: 0, button: 0 })
    Object.defineProperty(e, 'pointerId', { value: 1 })
    Object.defineProperty(e, 'pointerType', { value: kind })
    target.dispatchEvent(e)
  })
const th = (id: string) => document.querySelector<HTMLElement>(`thead th[data-column="${id}"]`)!
const placeHeaders = () => {
  const at = (el: HTMLElement) => [...document.querySelectorAll('thead th[data-column]')].indexOf(el)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const i = this.matches('thead th[data-column]') ? at(this) : 0
    return {
      left: i * 100,
      right: i * 100 + 100,
      top: 0,
      bottom: 40,
      width: 100,
      height: 40,
      x: i * 100,
      y: 0,
    } as DOMRect
  })
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(100)
}

describe('moving a column by dragging its header', () => {
  it('drags with a mouse: a ghost and a drop indicator while it goes, the new order kept when it lands', async () => {
    const onSort = vi.fn()
    m = await mount(<Probe onSort={onSort} />)
    placeHeaders()
    // Seen (the fourth, at 300 to 400) is taken by its middle and dropped before Role (100 to 200).
    await pointer('pointerdown', th('seen'), 350)
    expect(document.querySelector('[data-slot="column-ghost"]')).toBeNull()
    await pointer('pointermove', window, 340)
    expect(document.querySelector('[data-slot="column-ghost"]')?.textContent).toBe('Seen')
    await pointer('pointermove', window, 120)
    expect(document.querySelectorAll('[data-slot="drop-indicator"]')).toHaveLength(1)
    expect(th('role').querySelector('[data-slot="drop-indicator"]')).not.toBeNull()
    // (The browser makes the click right after the release, before anything else runs.)
    await act(async () => {
      const up = new MouseEvent('pointerup', { bubbles: true, clientX: 120 })
      Object.defineProperty(up, 'pointerId', { value: 1 })
      window.dispatchEvent(up)
      th('seen')
        .querySelector('button')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    })
    expect(document.querySelector('[data-slot="column-ghost"]')).toBeNull()
    expect(document.querySelector('[data-slot="drop-indicator"]')).toBeNull()
    expect(headers()).toEqual(['name', 'seen', 'role', 'mail'])
    expect(saved().order).toEqual(['name', 'seen', 'role', 'mail'])
    // The click that lets go of the header is not a sort.
    expect(onSort).not.toHaveBeenCalled()
    await m.unmount()
    m = await mount(<Probe />)
    expect(headers()).toEqual(['name', 'seen', 'role', 'mail'])
  })

  it('leaves a click alone: a press with no travel still sorts, and a drop where it was changes nothing', async () => {
    const onSort = vi.fn()
    m = await mount(<Probe onSort={onSort} />)
    placeHeaders()
    await pointer('pointerdown', th('seen'), 350)
    await pointer('pointermove', window, 352)
    await pointer('pointerup', window, 352)
    await click(th('seen').querySelector('button'))
    expect(onSort).toHaveBeenCalledTimes(1)
    await pointer('pointerdown', th('seen'), 350)
    await pointer('pointermove', window, 380)
    await pointer('pointerup', window, 380)
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
    expect(saved()).toBeNull()
  })

  it('stops at Escape, and a cancelled touch changes nothing', async () => {
    m = await mount(<Probe />)
    placeHeaders()
    await pointer('pointerdown', th('seen'), 350)
    await pointer('pointermove', window, 100)
    expect(document.querySelector('[data-slot="column-ghost"]')).not.toBeNull()
    await press(document, 'Escape')
    expect(document.querySelector('[data-slot="column-ghost"]')).toBeNull()
    await pointer('pointerup', window, 100)
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
  })

  it('takes a finger only after a press, so a swipe still scrolls and the page is held still once it has hold', async () => {
    m = await mount(<Probe />)
    placeHeaders()
    vi.useFakeTimers()
    try {
      // A swipe before the press is long enough: no drag.
      await pointer('pointerdown', th('seen'), 350, 'touch')
      await pointer('pointermove', window, 300, 'touch')
      await act(async () => void vi.advanceTimersByTime(400))
      expect(document.querySelector('[data-slot="column-ghost"]')).toBeNull()
      await pointer('pointerup', window, 300, 'touch')
      // A press that stays put: the column is taken, the page does not scroll, and it can be dropped.
      await pointer('pointerdown', th('seen'), 350, 'touch')
      await act(async () => void vi.advanceTimersByTime(349))
      expect(document.querySelector('[data-slot="column-ghost"]')).toBeNull()
      await act(async () => void vi.advanceTimersByTime(2))
      expect(document.querySelector('[data-slot="column-ghost"]')).not.toBeNull()
      const scroll = new Event('touchmove', { bubbles: true, cancelable: true })
      window.dispatchEvent(scroll)
      expect(scroll.defaultPrevented).toBe(true)
      await pointer('pointermove', window, 120, 'touch')
      await pointer('pointerup', window, 120, 'touch')
      expect(headers()).toEqual(['name', 'seen', 'role', 'mail'])
      // (The guard that eats the click after a drop takes itself off on the next tick.)
      await act(async () => void vi.advanceTimersByTime(10))
    } finally {
      vi.useRealTimers()
    }
  })

  it('moves with Alt and an arrow on a header, keeps focus on it, and says where it went', async () => {
    m = await mount(<Probe />)
    await press(th('role'), 'ArrowLeft', { altKey: true })
    expect(headers()).toEqual(['role', 'name', 'mail', 'seen'])
    expect(saved().order).toEqual(['role', 'name', 'mail', 'seen'])
    expect(document.activeElement).toBe(th('role'))
    expect(document.querySelector('[role="status"]')?.textContent).toBe(tc.table.moved('Role', 1, 4))
    // At the start nothing moves; without Alt the arrow is not a move.
    await press(th('role'), 'ArrowLeft', { altKey: true })
    await press(th('name'), 'ArrowRight')
    expect(headers()).toEqual(['role', 'name', 'mail', 'seen'])
    // On a header that sorts, focus is on its button, and that moves too.
    const sortable = th('seen').querySelector('button')!
    sortable.focus()
    await press(sortable, 'ArrowLeft', { altKey: true })
    expect(headers()).toEqual(['role', 'name', 'seen', 'mail'])
    expect(document.activeElement).toBe(th('seen').querySelector('button'))
    expect(th('role').getAttribute('aria-keyshortcuts')).toContain('Alt+ArrowLeft')
  })

  it('has no tick column or actions column to move: they are not headers of the order', async () => {
    m = await mount(<Probe selected={new Set()} onSelect={() => {}} />)
    placeHeaders()
    await press(th('name'), 'ArrowLeft', { altKey: true })
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
    expect(document.querySelector('thead th:first-child input[type="checkbox"]')).not.toBeNull()
    expect(document.querySelector('thead th:last-child [data-slot="table-settings"]')).not.toBeNull()
    expect(document.querySelector('thead th:first-child')?.hasAttribute('data-column')).toBe(false)
    await pointer('pointerdown', document.querySelector('thead th:first-child')!, 20)
    await pointer('pointermove', window, 500)
    expect(document.querySelector('[data-slot="column-ghost"]')).toBeNull()
    await pointer('pointerup', window, 500)
  })

  it('puts the columns back from the settings without touching the density', async () => {
    m = await mount(<Probe />)
    await press(th('seen'), 'ArrowLeft', { altKey: true })
    await open()
    await click(
      [...document.querySelectorAll('[data-slot="popover-content"] button')].find(
        (b) => b.textContent === tc.table.compact,
      ),
    )
    await click(
      [...document.querySelectorAll('[data-slot="popover-content"] button')].find(
        (b) => b.textContent === tc.table.resetColumns,
      ),
    )
    expect(headers()).toEqual(['name', 'role', 'mail', 'seen'])
    expect(saved()).toMatchObject({ order: [], hidden: [], widths: {}, custom: false, density: 'compact' })
  })
})

describe('resizing a column', () => {
  it('follows the pointer in pixels of the room the columns share, and every column keeps its least width', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
    m = await mount(<Probe />)
    const widthOf = (i: number) => parseFloat(document.querySelectorAll('col')[i].style.width)
    // 1000 less the actions (148) and the settings (48): 804 to share, as pixels the browser can lay out.
    expect(document.querySelectorAll('col')[0].style.width).toMatch(/px$/)
    const sum = () => [0, 1, 2, 3, 4].reduce((n, i) => n + widthOf(i), 0)
    expect(sum()).toBe(804 + 196)
    const edge = document.querySelector<HTMLElement>('[role="separator"]')!
    const before = [widthOf(0), widthOf(1)]
    await pointer('pointerdown', edge, 100)
    await pointer('pointermove', window, 140)
    expect(widthOf(0)).toBeCloseTo(before[0] + 40, 0)
    expect(widthOf(1)).toBeCloseTo(before[1] - 40, 0)
    // Pulled far past the neighbour's least width (100), it stops there and the others stay as they were.
    await pointer('pointermove', window, 5000)
    expect(widthOf(1)).toBeCloseTo(100, 0)
    await pointer('pointerup', window, 5000)
    expect(saved().widths.role).toBeCloseTo(100 / 804, 3)
    expect(Object.values(saved().widths as Record<string, number>).reduce((a, b) => a + b, 0)).toBeCloseTo(1, 5)
    expect(sum()).toBe(804 + 196)
    // The other way: the first column stops at its own least width (120).
    const again = document.querySelector<HTMLElement>('[role="separator"]')!
    await pointer('pointerdown', again, 400)
    await pointer('pointermove', window, -5000)
    await pointer('pointerup', window, -5000)
    expect(widthOf(0)).toBeCloseTo(120, 0)
  })

  it('lifts a saved width that is below the least, and shares what it took from the others', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
    localStorage.setItem(
      'pb.table.probe',
      JSON.stringify({ widths: { name: 0.6, role: 0.02, mail: 0.2, seen: 0.18 }, custom: true }),
    )
    m = await mount(<Probe />)
    const w = [...document.querySelectorAll('col')].slice(0, 4).map((c) => parseFloat(c.style.width))
    expect(w[1]).toBeGreaterThanOrEqual(100)
    expect(w[0]).toBeGreaterThanOrEqual(120)
    expect(w[2]).toBeGreaterThanOrEqual(160)
    expect(w[3]).toBeGreaterThanOrEqual(100)
  })

  it('goes back to the usual proportion of the two columns on a double click', async () => {
    m = await mount(<Probe />)
    const edge = document.querySelector<HTMLElement>('[role="separator"]')!
    await press(edge, 'ArrowRight')
    await press(edge, 'ArrowRight')
    const moved = saved().widths
    expect(moved.name).toBeGreaterThan(moved.role)
    await act(async () => {
      edge.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    })
    const back = saved().widths
    // Equal weights: the two share what they had between them evenly again; the others are as they were.
    expect(back.name).toBeCloseTo(back.role, 5)
    expect(back.name + back.role).toBeCloseTo(moved.name + moved.role, 5)
    expect(back.mail).toBeCloseTo(moved.mail, 5)
  })

  it('takes a double press on an edge for a double click, and does not save a press with no travel', async () => {
    m = await mount(<Probe />)
    const edge = document.querySelector<HTMLElement>('[role="separator"]')!
    await pointer('pointerdown', edge, 100)
    await pointer('pointerup', window, 100)
    expect(saved()).toBeNull()
  })

  it('has a hit area a finger can use, takes no scroll of the page, and shows its grip only on hover or focus', async () => {
    m = await mount(<Probe />)
    const edge = document.querySelector<HTMLElement>('[role="separator"]')!
    expect(edge.className).toContain('touch-none')
    expect(edge.className).toContain('w-4')
    expect(edge.className).toContain('pointer-coarse:w-6')
    expect(edge.className).toContain('cursor-col-resize')
    const grip = edge.firstElementChild as HTMLElement
    expect(grip.className).toContain('opacity-0')
    expect(grip.className).toContain('group-hover/th:opacity-100')
    expect(grip.className).toContain('group-focus-within/th:opacity-100')
  })
})

describe('no vertical lines', () => {
  const VERTICAL = /(^|:)(border-(x|l|r|s|e)(-|$)|divide-x|shadow-\[-?\d+px_0|ring-x|after:w-px|before:w-px)/
  it('draws no line between columns, in the header, the rows, the columns or the sticky one', async () => {
    m = await mount(<Probe selected={new Set()} onSelect={() => {}} />)
    const cells = document.querySelectorAll('table, table > *, thead th, tbody td, tbody th, col, tr')
    expect(cells.length).toBeGreaterThan(10)
    for (const el of cells) {
      const bad = el.className
        .toString()
        .split(/\s+/)
        .filter((c) => VERTICAL.test(c))
      expect(bad, el.outerHTML.slice(0, 80)).toEqual([])
    }
    // The grips (hover and focus only) are not a line at rest either.
    for (const grip of document.querySelectorAll('[role="separator"]')) {
      expect(grip.className).not.toMatch(/(^|\s)(after|before):/)
      expect(grip.className).not.toMatch(/(^|\s)border/)
    }
    // The rows keep their horizontal lines.
    expect(document.querySelector('tbody')!.className).toContain('divide-y')
    expect(document.querySelector('thead')!.className).toContain('border-b')
  })

  it('shades the sticky column softly instead of cutting it with a line, only while there is more to scroll', async () => {
    m = await mount(<Probe />)
    const cell = document.querySelector<HTMLElement>('tbody td:last-child')!
    expect(cell.className).toContain('before:bg-linear-to-l')
    expect(cell.className).toContain('before:opacity-(--sticky-shade,0)')
    expect(cell.className).not.toContain('shadow-')
    // Nothing to scroll in a box that was never measured: the shade is off.
    expect(
      document.querySelector<HTMLElement>('[data-slot="data-table"] > div')!.style.getPropertyValue('--sticky-shade'),
    ).toBe('0')
  })
})

describe('fitting the columns to their content', () => {
  // A text is 10px a letter; the layout of jsdom has no widths of its own.
  const measure = () => {
    const real = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (!this.hasAttribute('data-fit')) return real.call(this)
      return { width: (this.textContent?.length ?? 0) * 10, height: 0 } as DOMRect
    })
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
  }
  const long: Row[] = [
    { id: 'a', name: 'Ann', role: 'r'.repeat(20), mail: 'm'.repeat(60), seen: 'today' },
    { id: 'b', name: 'Bo', role: 'member', mail: 'bo@x.test', seen: 'never' },
  ]
  const widths = () => [...document.querySelectorAll('col')].slice(0, 4).map((c) => parseFloat(c.style.width))
  const open = async () => {
    await click(document.querySelector('[data-slot="table-settings"]'))
    await settle(() => document.querySelector('[data-slot="popover-content"]') !== null, 'the settings')
  }

  it('starts each column at its content, cuts a long one at its cap, and shares the rest', async () => {
    measure()
    m = await mount(<Probe rows={long} />)
    // Content with the padding: name and seen below their least (120, 100), role 224, mail 624 capped at 320.
    // 804 of room less 764 leaves 40, ten to each column.
    expect(widths()).toEqual([130, 234, 330, 110])
    expect(widths().reduce((n, w) => n + w, 0)).toBe(804)
    expect(saved()).toBeNull()
  })

  it('shrinks the long columns toward their least width before it scrolls', async () => {
    measure()
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(700)
    m = await mount(<Probe rows={long} />)
    // 504 of room against 764 of content: the least widths (480) fit, so the table does not overflow.
    const w = widths()
    expect(w.reduce((n, x) => n + x, 0)).toBe(504)
    expect(w[0]).toBe(120)
    expect(w[3]).toBe(100)
    expect(w[2]).toBeGreaterThan(w[1])
    expect(document.querySelector('table')!.style.minWidth).toBe(`${480 + 196}px`)
  })

  it('has no scrollbar of its own, and fades its edges instead, for a mouse as for a finger', async () => {
    m = await mount(<Probe rows={long} />)
    const box = document.querySelector<HTMLElement>('[data-slot="data-table"] > div')!
    expect(box.className).toContain('[scrollbar-width:none]')
    expect(box.className).not.toContain('pointer-coarse:[scrollbar-width:none]')
  })

  it('keeps a width the person set over the content, and goes back to it on reset', async () => {
    measure()
    localStorage.setItem(
      'pb.table.probe',
      JSON.stringify({ widths: { name: 0.25, role: 0.25, mail: 0.25, seen: 0.25 }, custom: true }),
    )
    m = await mount(<Probe rows={long} />)
    expect(widths()).toEqual([201, 201, 201, 201])
    await open()
    await click(
      [...document.querySelectorAll('[data-slot="popover-content"] button')].find(
        (b) => b.textContent === tc.table.resetColumns,
      ),
    )
    expect(widths()).toEqual([130, 234, 330, 110])
    expect(saved().widths).toEqual({})
  })

  it('fits a column to its content on a double click of its edge, and on Enter', async () => {
    measure()
    localStorage.setItem(
      'pb.table.probe',
      JSON.stringify({ widths: { name: 0.25, role: 0.25, mail: 0.25, seen: 0.25 }, custom: true }),
    )
    m = await mount(<Probe rows={long} />)
    const edges = () => document.querySelectorAll<HTMLElement>('[role="separator"]')
    await act(async () => {
      edges()[1].dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    })
    expect(saved().widths.role * 804).toBeCloseTo(224, 0)
    expect(widths()[1]).toBe(224)
    await press(edges()[2], 'Enter')
    // The mail is cut at its cap.
    expect(saved().widths.mail * 804).toBeCloseTo(320, 0)
  })

  it('gives a cut cell its full text as a tooltip, once the pointer is on it', async () => {
    measure()
    m = await mount(<Probe rows={long} />)
    const cell = document.querySelector<HTMLElement>('tbody td[data-column="mail"] > div')!
    vi.spyOn(cell, 'scrollWidth', 'get').mockReturnValue(624)
    vi.spyOn(cell, 'clientWidth', 'get').mockReturnValue(306)
    expect(cell.title).toBe('')
    await act(async () => {
      cell.dispatchEvent(new MouseEvent('pointerover', { bubbles: true }))
    })
    expect(cell.title).toBe('m'.repeat(60))
  })
})

describe('row actions in a narrow box', () => {
  function Acting() {
    const prefs = useTablePrefs('acting')
    return (
      <DataTable
        prefs={prefs}
        columns={COLUMNS}
        rows={ROWS}
        rowId={(r) => r.id}
        rowLabel={(r) => r.name}
        empty="Nobody."
        actions={(r) => (
          <RowAction icon={<i data-icon />} label="View" ariaLabel={`View ${r.name}`} onClick={() => {}} />
        )}
      />
    )
  }
  const action = () => document.querySelector<HTMLElement>('tbody tr button[aria-label="View Ann"]')!

  it('keeps the label of an action while the box is wide', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
    m = await mount(<Acting />)
    expect(action().textContent).toBe('View')
    expect(document.querySelectorAll('col')[4].style.width).toBe('196px')
  })

  it('shows only the icon, with its name and a tooltip and a 40px target, once the box is under 640px', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(500)
    m = await mount(<Acting />)
    expect(action().textContent).toBe('')
    expect(action().getAttribute('title')).toBe('View')
    expect(action().className).toContain('size-10')
    // The column of the actions shrinks with them.
    expect(parseFloat(document.querySelector<HTMLElement>('colgroup > col:last-child')!.style.width)).toBeLessThan(120)
  })
})
