// @vitest-environment jsdom
import { useState } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { DataTable, type Column, type DataTableSort } from '@/components/ui/data-table'
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
    expect(document.querySelector('col:nth-child(1)')!.getAttribute('style')).toContain('calc(')
  })

  it('takes the least important columns away first when the box is narrow, and scrolls inside its own box', async () => {
    const width = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(480)
    m = await mount(<Probe />)
    // 480 less the 148 + 48 of the actions and the settings leaves 284: the name (120) and one more of 100 fit.
    expect(headers()).toEqual(['name', 'role'])
    expect(document.querySelector('[data-slot="columns-hidden"]')!.textContent).toBe(tc.table.hiddenNarrow(2))
    // What is left still has its least width, and the box is the one that scrolls.
    expect(document.querySelector('table')!.style.minWidth).toBe(`${120 + 100 + 148 + 48}px`)
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
  })
})
