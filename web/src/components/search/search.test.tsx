// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { FileOpsProvider } from '@/components/files/FileOps'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import {
  byLabel,
  click,
  installDomShims,
  locationText,
  mount,
  press,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { SearchDialogProvider } from './SearchDialog'
import { TIME_GROUPS } from './searchModel'

// The dialog is built only when it opens, and every filter is a popover that is
// built only when it opens: each is opened here, and each request is read back.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  vi.restoreAllMocks()
  await m?.unmount()
  m = undefined
})

const app = (
  <SearchDialogProvider>
    <Routes>
      <Route path="/s/:space/*" element={<div data-testid="page">page</div>} />
    </Routes>
  </SearchDialogProvider>
)

const dialog = () => document.querySelector<HTMLElement>('[data-slot="search-dialog"]')
const input = () => dialog()!.querySelector<HTMLInputElement>('input[role="combobox"]')!
const options = () => [...(dialog()?.querySelectorAll<HTMLElement>('[role="option"][id^="search-option-"]') ?? [])]
const optionText = () => options().map((o) => o.querySelector('span.truncate')?.textContent)
const chip = (label: string) =>
  [...dialog()!.querySelectorAll<HTMLElement>('[aria-label="' + t.search.filters + '"] button')].find((b) =>
    b.textContent?.startsWith(label),
  )!
const listRequests = () =>
  m!.requests.filter((r) => r.method === 'GET' && /^\/api\/spaces\/default\/pages\?/.test(r.url))
const lastList = () => new URL(listRequests().at(-1)!.url, 'http://x').searchParams
const popover = () => document.querySelector<HTMLElement>('[data-slot="popover-content"]')

async function openSearch(entries = ['/s/default/recent']) {
  m = await mount(app, { entries, server: { mode: 'single' } })
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true, cancelable: true }))
  })
  await settle(() => dialog() !== null && options().length > 0, 'dialog with results')
}

// Types, then waits out the debounce and the answer.
async function search(text: string) {
  const before = listRequests().length
  await type(input(), text)
  await settle(() => listRequests().length > before, 'request after typing')
  await settle(
    () => dialog()!.getAttribute('aria-busy') !== 'true' && !dialog()!.querySelector('[aria-busy="true"]'),
    'answered',
  )
}

describe('the search dialog', () => {
  it('opens with ⌘K and Ctrl+K and lists the recent pages grouped by time', async () => {
    await openSearch()
    expect(options().length).toBeGreaterThan(5)
    // Newest first, in groups from today back to older, with the groups in that order.
    const headings = [...dialog()!.querySelectorAll('[role="listbox"] > li > div[role="presentation"]')].map(
      (h) => h.textContent,
    )
    const order = TIME_GROUPS.map((g) => t.search.groups[g])
    expect(headings.length).toBeGreaterThan(0)
    expect(headings.map((h) => order.indexOf(h!))).toEqual(
      [...headings.map((h) => order.indexOf(h!))].sort((a, b) => a - b),
    )
    expect(headings.every((h) => order.includes(h!))).toBe(true)
    // Each row has a label, a folder path and a snippet.
    const row = options()[0]
    expect(row.textContent).toBeTruthy()
    expect(row.querySelector('svg')).not.toBeNull()
    // The recent request: no text, newest first, one page of 40.
    expect(lastList().get('sort')).toBe('-updated')
    expect(lastList().get('limit')).toBe('40')
    expect(lastList().has('q')).toBe(false)
    // Escape closes it; Ctrl+K opens it again.
    await press(dialog()!, 'Escape')
    await settle(() => dialog() === null, 'closed')
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, cancelable: true }))
    })
    await settle(() => dialog() !== null, 'reopened')
  })

  it('does not open over another dialog', async () => {
    m = await mount(app, { entries: ['/s/default/recent'], server: { mode: 'single' } })
    const other = document.createElement('div')
    other.setAttribute('role', 'dialog')
    document.body.appendChild(other)
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true, cancelable: true }))
    })
    expect(dialog()).toBeNull()
    other.remove()
  })

  it('searches as you type, by relevance, with the matches marked', async () => {
    await openSearch()
    await search('慢')
    expect(lastList().get('q')).toBe('慢')
    expect(lastList().has('sort')).toBe(false)
    expect(optionText()).toContain('关于慢')
    expect(dialog()!.querySelector('mark')?.textContent).toBe('慢')
    // By relevance there are no time groups, and the list says what it is.
    expect(dialog()!.querySelector('[role="listbox"] > li > div[role="presentation"]')!.textContent).toBe(
      t.search.results,
    )
    // Time order brings the groups back.
    await type(dialog()!.querySelector<HTMLSelectElement>(`select[aria-label="${t.search.sortLabel}"]`)!, 'updated')
    await settle(() => lastList().get('sort') === '-updated', 'sorted by time')
    await settle(() => TIME_GROUPS.some((g) => dialog()!.textContent!.includes(t.search.groups[g])), 'groups')
  })

  it('says so when nothing matches, and offers the full results page', async () => {
    await openSearch()
    await search('这个词不会出现xyz')
    await settle(() => dialog()!.textContent!.includes(t.search.empty), 'empty text')
    expect(options()).toHaveLength(0)
  })

  it('arrows move, Enter opens the page, and the dialog closes', async () => {
    await openSearch()
    expect(options()[0].getAttribute('aria-selected')).toBe('true')
    await press(input(), 'ArrowDown')
    expect(options()[1].getAttribute('aria-selected')).toBe('true')
    expect(input().getAttribute('aria-activedescendant')).toBe(options()[1].id)
    await press(input(), 'ArrowUp')
    await press(input(), 'ArrowUp')
    // Wraps to the last one.
    expect(options().at(-1)!.getAttribute('aria-selected')).toBe('true')
    await press(input(), 'ArrowDown')
    const id = options()[0].id.replace('search-option-', '')
    await press(input(), 'Enter')
    await settle(() => dialog() === null, 'closed')
    expect(locationText()).toBe(`/s/default/p/${id}`)
  })

  it('⌘Enter and Ctrl+Enter open in a new tab and leave the dialog open', async () => {
    await openSearch()
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const id = options()[0].id.replace('search-option-', '')
    await press(input(), 'Enter', { metaKey: true })
    expect(open).toHaveBeenCalledWith(`/s/default/p/${id}`, '_blank', 'noopener')
    await press(input(), 'Enter', { ctrlKey: true })
    expect(open).toHaveBeenCalledTimes(2)
    expect(dialog()).not.toBeNull()
    expect(locationText()).toBe('/s/default/recent')
  })

  it('does not take Enter that confirms an IME candidate', async () => {
    await openSearch()
    await press(input(), 'Enter', { isComposing: true })
    expect(dialog()).not.toBeNull()
  })

  it('clicking a result opens it', async () => {
    await openSearch()
    const id = options()[2].id.replace('search-option-', '')
    await click(options()[2])
    await settle(() => dialog() === null, 'closed')
    expect(locationText()).toBe(`/s/default/p/${id}`)
  })

  it('shows a preview of the highlighted page from the listing it already has', async () => {
    await openSearch()
    const preview = dialog()!.querySelector<HTMLElement>(`aside[aria-label="${t.search.preview}"]`)!
    expect(preview.textContent).toContain(t.search.previewUpdated)
    const before = listRequests().length
    await press(input(), 'ArrowDown')
    const second = options()[1].querySelector('span.truncate')!.textContent
    expect(preview.querySelector('h3')!.textContent).toBe(second)
    // No request of its own.
    expect(listRequests().length).toBe(before)
    expect(m!.requests.some((r) => /\/pages\/[^/?]+$/.test(r.url))).toBe(false)
  })

  it('"see all results" goes to the results page with the text and the filters', async () => {
    await openSearch()
    await search('慢')
    await click(chip(t.search.chips.titleOnly))
    await click([...dialog()!.querySelectorAll('button')].find((b) => b.textContent === t.search.seeAll))
    await settle(() => dialog() === null, 'closed')
    expect(locationText()).toBe('/s/default/search?q=%E6%85%A2&in=title')
  })

  it('cancels a request that is still on its way when the text changes', async () => {
    m = await mount(app, {
      entries: ['/s/default/recent'],
      server: { mode: 'single' },
      // Search requests take a while, so the first is still pending when the text changes.
      delay: (method, url) => (method === 'GET' && url.includes('q=') ? 400 : 0),
    })
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true, cancelable: true }))
    })
    await settle(() => options().length > 0, 'recent pages')
    await type(input(), '慢')
    await settle(() => listRequests().some((r) => r.url.includes('q=%E6%85%A2')), 'first search sent')
    const first = listRequests().find((r) => r.url.includes('q=%E6%85%A2'))!
    expect(first.signal?.aborted).toBe(false)
    await type(input(), '慢走')
    await settle(() => listRequests().some((r) => r.url.includes('q=%E6%85%A2%E8%B5%B0')), 'second search sent')
    await settle(() => first.signal?.aborted === true, 'first search aborted', 60)
    // What is shown is the answer to the last text.
    await settle(
      () => dialog()!.getAttribute('aria-busy') !== 'true' && !dialog()!.querySelector('[aria-busy="true"]'),
      'answered',
      80,
    )
  })

  it('a burst of typing makes one request, not one per key', async () => {
    await openSearch()
    const before = listRequests().length
    for (const text of ['慢', '慢走', '慢走一']) await type(input(), text)
    await settle(() => listRequests().length > before, 'request')
    await act(async () => {
      await new Promise((r) => setTimeout(r, 300))
    })
    expect(listRequests().length - before).toBe(1)
    expect(lastList().get('q')).toBe('慢走一')
  })
})

describe('the filters', () => {
  it('title only: a chip that toggles, sent as in=title with text', async () => {
    await openSearch()
    const titleOnly = chip(t.search.chips.titleOnly)
    expect(titleOnly.getAttribute('aria-pressed')).toBe('false')
    await search('慢')
    await click(titleOnly)
    expect(titleOnly.getAttribute('aria-pressed')).toBe('true')
    await settle(() => lastList().get('in') === 'title', 'in=title')
    // Off again: the same request as before, which is already known.
    await click(titleOnly)
    expect(titleOnly.getAttribute('aria-pressed')).toBe('false')
  })

  it('folder: pick one, subfolders included, and clear it', async () => {
    await openSearch()
    await click(chip(t.search.chips.folder))
    await settle(
      () => popover() !== null && popover()!.querySelectorAll('[role="option"]').length > 3,
      'folders listed',
    )
    const names = [...popover()!.querySelectorAll('[role="option"]')].map((o) => o.textContent)
    expect(names.some((n) => n?.startsWith('随笔'))).toBe(true)
    // Filtering narrows the list.
    await type(popover()!.querySelector('input')!, '随笔')
    await settle(() => popover()!.querySelectorAll('[role="option"]').length <= 3, 'filtered')
    await click([...popover()!.querySelectorAll('[role="option"]')].find((o) => o.textContent?.startsWith('随笔')))
    await settle(() => lastList().get('folder') === '随笔', 'folder sent')
    expect(lastList().get('recursive')).toBe('true')
    // The chip says which folder, and has its own clear button.
    await settle(() => chip(t.search.chips.folder).textContent!.includes('随笔'), 'chip value')
    await click(byLabel(t.search.clear(t.search.chips.folder)))
    // Back to the recent pages (already known, so asked for no more).
    await settle(() => !chip(t.search.chips.folder).textContent!.includes('随笔'), 'folder cleared')
    await settle(() => options().length > 10, 'all pages again')
  })

  it('tags: pick more than one, all must match', async () => {
    await openSearch()
    await click(chip(t.search.chips.tags))
    await settle(() => popover() !== null && popover()!.querySelectorAll('[role="option"]').length > 2, 'tags listed')
    const pick = (name: string) =>
      [...popover()!.querySelectorAll('[role="option"]')].find((o) => o.textContent?.startsWith(name))
    await click(pick('随笔'))
    await settle(() => lastList().getAll('tag').length === 1, 'first tag')
    await click(pick('日常'))
    await settle(() => lastList().getAll('tag').length === 2, 'second tag')
    expect(lastList().getAll('tag')).toEqual(['随笔', '日常'])
    expect(pick('随笔')!.getAttribute('aria-selected')).toBe('true')
    // Picking one again takes it off.
    await click(pick('随笔'))
    await settle(() => lastList().getAll('tag').length === 1, 'one tag')
    expect(lastList().getAll('tag')).toEqual(['日常'])
  })

  it('updated: a preset', async () => {
    await openSearch()
    await click(chip(t.search.chips.updated))
    await settle(() => popover() !== null, 'popover')
    expect([...popover()!.querySelectorAll('button[aria-pressed]')].map((b) => b.textContent)).toEqual([
      t.search.presets.today,
      t.search.presets.week,
      t.search.presets.month,
    ])
    await click([...popover()!.querySelectorAll('button')].find((b) => b.textContent === t.search.presets.week))
    await settle(() => lastList().has('updatedFrom'), 'updatedFrom sent')
    expect(lastList().get('updatedFrom')).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    const from = Date.parse(lastList().get('updatedFrom')!)
    expect(Math.round((Date.now() - from) / 86_400_000)).toBeGreaterThanOrEqual(7)
    expect(lastList().has('createdFrom')).toBe(false)
    await settle(() => chip(t.search.chips.updated).textContent!.includes(t.search.presets.week), 'chip value')
  })

  it('updated: a custom range sends both ends', async () => {
    await openSearch()
    await click(chip(t.search.chips.updated))
    await settle(() => popover() !== null, 'popover')
    const [from, to] = [...popover()!.querySelectorAll<HTMLInputElement>('input[type="date"]')]
    const apply = [...popover()!.querySelectorAll('button')].find(
      (b) => b.textContent === t.search.apply,
    ) as HTMLButtonElement
    expect(apply.disabled).toBe(true)
    await type(from, '2026-11-01')
    await type(to, '2026-11-09')
    await click(apply)
    await settle(() => lastList().get('updatedFrom') === '2026-11-01', 'range sent')
    expect(lastList().get('updatedTo')).toBe('2026-11-09')
    await settle(() => chip(t.search.chips.updated).textContent!.includes('2026-11-01 – 2026-11-09'), 'chip value')
  })

  it('created: the same, with its own parameters', async () => {
    await openSearch()
    await click(chip(t.search.chips.created))
    await settle(() => popover() !== null, 'popover')
    await click([...popover()!.querySelectorAll('button')].find((b) => b.textContent === t.search.presets.month))
    await settle(() => lastList().has('createdFrom'), 'createdFrom')
    expect(lastList().has('updatedFrom')).toBe(false)
    await click(byLabel(t.search.clear(t.search.chips.created)))
    await settle(() => byLabel(t.search.clear(t.search.chips.created)) === null, 'cleared')
    await settle(() => options().length > 10, 'all pages again')
  })

  it('filters work without text, and several together', async () => {
    await openSearch()
    await click(chip(t.search.chips.tags))
    await settle(() => popover() !== null && popover()!.querySelectorAll('[role="option"]').length > 0, 'tags')
    await click([...popover()!.querySelectorAll('[role="option"]')].find((o) => o.textContent?.startsWith('随笔')))
    await settle(() => lastList().get('tag') === '随笔', 'tag sent')
    expect(lastList().has('q')).toBe(false)
    expect(lastList().get('sort')).toBe('-updated')
    // 随笔-tagged pages only.
    await settle(() => options().length > 0 && options().length < 10, 'filtered list')
  })
})

describe('where the dialog is opened from', () => {
  it('the sidebar search box opens it', async () => {
    m = await mount(
      <SearchDialogProvider>
        <SidebarProvider>
          <QuickCaptureProvider>
            <FileOpsProvider>
              <AppSidebar />
            </FileOpsProvider>
          </QuickCaptureProvider>
        </SidebarProvider>
      </SearchDialogProvider>,
      { entries: ['/s/default/recent'], server: { mode: 'single' } },
    )
    await settle(() => byLabel(t.nav.search) !== null, 'sidebar')
    expect(dialog()).toBeNull()
    await click(byLabel(t.nav.search))
    await settle(() => dialog() !== null && options().length > 0, 'opened')
  })
})
