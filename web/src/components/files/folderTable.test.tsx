// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { FolderList } from '@/pages/Lists'
import { FileOpsProvider } from '@/components/files/FileOps'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { click, installDomShims, mount, settle, type Mounted } from '@/testing/harness'
import { browserPrefs, prefsStore, setPrefsStore, type PrefsStore } from '@/lib/prefsStore'
import { readPrefs } from '@/lib/tablePrefs'

// The folder as a table: subfolders first or mixed in, a column for each thing a page has, sort by a header, the filters
// above it as chips, a star that fades in, a phone's rows, and where the person's choices are kept.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())
let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  setPrefsStore(browserPrefs)
})

const shell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <Routes>
        <Route
          path="/s/:space/f/*"
          element={
            <FileOpsProvider>
              <FolderList />
            </FileOpsProvider>
          }
        />
      </Routes>
    </QuickCaptureProvider>
  </SidebarProvider>
)
const send = (server: MockServer, method: string, path: string, body?: unknown) =>
  server.handle(
    method,
    `/api/spaces/default${path}`,
    body === undefined ? undefined : JSON.stringify(body),
    'application/json',
  ) as {
    body: any
  }

function seed(server: MockServer) {
  send(server, 'POST', '/folders', { path: 'Notes' })
  send(server, 'POST', '/folders', { path: 'Notes/Sub' })
  const make = (title: string, meta: Record<string, unknown>) =>
    send(server, 'POST', '/pages', { folder: 'Notes', meta: { title, ...meta }, body: `${title} 正文\n` })
  make('Bravo', { source: ['书', '电影'], tags: ['x'], subtitle: '副' })
  make('Alpha', { source: '音乐', created: '2026-01-05' })
  make('Charlie', { tags: ['y'], icon: 'flower', color: 'pink' })
}
const open = async (entries = '/s/default/f/Notes') => {
  m = await mount(shell, { entries: (server) => (seed(server), [entries]) })
  await settle(() => document.querySelectorAll('tr[data-row]').length === 4, 'the rows')
}
const rowKeys = () => [...document.querySelectorAll('tr[data-row]')].map((r) => r.getAttribute('data-row'))
const headers = () => [...document.querySelectorAll('thead th')].map((h) => h.getAttribute('data-column'))
const calls = () =>
  m!.requests
    .filter((r) => r.method === 'GET' && /\/pages\?/.test(r.url))
    .map((r) => new URL(r.url, 'http://x').searchParams)

describe('the folder as a table', () => {
  it('has a row for the subfolder and each page, subfolders first, with the columns of a page', async () => {
    await open()
    expect(rowKeys()[0]).toBe('f:Notes/Sub')
    expect(rowKeys().slice(1)).toHaveLength(3)
    expect(headers()).toEqual(expect.arrayContaining(['title', 'updated', 'tags', 'source', 'created', 'subtitle']))
    const bravo =
      document.querySelector('tr[data-row^="p:"]:has([title])') ?? document.querySelector('tr[data-row^="p:"]')!
    expect(document.body.textContent).toContain('Bravo')
    expect(document.body.textContent).toContain('电影')
    expect(bravo).not.toBeNull()
    // The excerpt is a muted line under the bold title; a folder says how many pages it holds.
    expect(document.querySelector('tr[data-row="f:Notes/Sub"]')!.textContent).toContain(t.ftable.pages(0))
    // A page with an icon of its own shows it.
    expect(document.querySelector('svg[data-icon="flower"]')).not.toBeNull()
  })

  it('sorts by a header, and by the source, and says so in the address of the listing', async () => {
    await open()
    const header = (name: string) =>
      [...document.querySelectorAll('button')].find((b) => b.getAttribute('aria-label') === tc.table.sortBy(name))
    await click(header(t.ftable.columns.source))
    await settle(() => calls().some((q) => q.get('sort') === 'prop:source'), 'sorted by source')
    await click(header(t.ftable.columns.title))
    await settle(() => calls().some((q) => q.get('sort') === 'title'), 'sorted by title')
  })

  it('mixes the folders in among the pages by name when asked, and remembers that', async () => {
    await open()
    await click(document.querySelector('[data-slot="folders-toggle"]'))
    await settle(
      () => document.querySelector('[data-slot="folders-toggle"]')!.textContent === t.ftable.foldersMixed,
      'mixed',
    )
    expect(JSON.parse(localStorage.getItem('pb.files.mixed')!)).toBe(true)
  })

  it('filters by a source and by a tag behind the filter button, as chips that can be taken away', async () => {
    await open()
    await click(document.querySelector('[data-slot="filter-button"]'))
    await settle(() => document.querySelector('[role="dialog"], [data-slot="popover-content"]') !== null, 'the popover')
    const pill = (text: string) =>
      [...document.querySelectorAll('button[aria-pressed]')].find((b) => b.textContent === text) as HTMLElement
    await settle(() => !!pill('书'), 'the used sources')
    await click(pill('书'))
    await settle(() => calls().some((q) => (q.get('filter') ?? '').includes('"source"')), 'filtered by source')
    await settle(() => document.querySelectorAll('tr[data-row]').length === 1, 'one page is left')
    expect(document.querySelector('tr[data-row]')!.textContent).toContain('Bravo')
    const chip = document.querySelector('[data-slot="filter-chip"]')!
    expect(chip.textContent).toContain('书')
    // The folders are not listed while the pages are filtered; taking the chip away brings everything back.
    await click(chip.querySelector('button'))
    await settle(() => document.querySelectorAll('tr[data-row]').length === 4, 'everything again')
    expect(document.querySelector('[data-slot="filter-chip"]')).toBeNull()
  })

  it('has a star at the end of a row that fades in over 150 ms, always on a touch screen, and keeps a favourite lit', async () => {
    await open()
    const star = document.querySelector<HTMLElement>('tr[data-row^="p:"] [data-slot="row-star"]')!
    expect(star.className).toMatch(/opacity-0/)
    expect(star.className).toMatch(/duration-150/)
    expect(star.className).toMatch(/transition-opacity/)
    expect(star.className).toMatch(/group-hover\/row:opacity-100/)
    expect(star.className).toMatch(/pointer-coarse:opacity-40/)
    await click(star)
    await settle(
      () => document.querySelector('tr[data-row^="p:"] [data-slot="row-star"][aria-pressed="true"]') !== null,
      'favourited',
    )
    expect(document.querySelector('[data-slot="row-star"][aria-pressed="true"]')!.className).not.toMatch(
      /\bopacity-0\b/,
    )
  })

  it('keeps the columns, their order and widths in the preferences store, never a server', async () => {
    const kept: Record<string, unknown> = {}
    const store: PrefsStore = {
      read: (k) => kept[k] ?? null,
      write: (k, v) => void (kept[k] = v),
      remove: (k) => void delete kept[k],
    }
    setPrefsStore(store)
    await open()
    await click(document.querySelector('[data-slot="table-settings"]'))
    await settle(
      () =>
        document.querySelector(
          'input[type="checkbox"][aria-label], [role="dialog"] input[type="checkbox"], [data-slot="popover-content"] input[type="checkbox"]',
        ) !== null,
      'the settings',
    )
    const box = [...document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find(
      (i) => i.closest('label')?.textContent === t.ftable.columns.subtitle,
    )!
    await click(box)
    await settle(() => !headers().includes('subtitle'), 'the subtitle column is gone')
    expect(readPrefs('files:Notes')).toMatchObject({ hidden: ['subtitle'], custom: true })
    expect(prefsStore()).toBe(store)
    expect(Object.keys(kept)).toEqual(['pb.table.files:Notes'])
    expect(localStorage.length).toBe(0)
    expect(m!.requests.some((r) => r.method !== 'GET' && /prefs/.test(r.url))).toBe(false)
  })
})

describe('on a phone', () => {
  const phone = () => {
    const real = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: /max-width/.test(query),
      media: query,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
      onchange: null,
    })) as typeof window.matchMedia
    return () => void (window.matchMedia = real)
  }

  it('is rows of a title with a small line of time, tags and sources, and sort and filter behind one button', async () => {
    const restore = phone()
    try {
      m = await mount(shell, { entries: (server) => (seed(server), ['/s/default/f/Notes']) })
      await settle(() => document.querySelector('[data-slot="phone-rows"]') !== null, 'the rows')
      expect(document.querySelector('table')).toBeNull()
      const rows = [...document.querySelectorAll('[data-slot="phone-rows"] > li')]
      expect(rows).toHaveLength(4)
      const bravo = rows.find((r) => r.textContent?.includes('Bravo'))!
      expect(bravo.querySelector('time')).not.toBeNull()
      expect(bravo.textContent).toContain('x')
      expect(bravo.textContent).toContain('电影')
      // The star is there, quiet, and the one button says what it holds.
      expect(bravo.querySelector('[data-slot="row-star"]')!.className).toMatch(/pointer-coarse:opacity-40/)
      const button = document.querySelector('[data-slot="filter-button"]')!
      expect(button.textContent).toContain(t.ftable.sortAndFilter)
      await click(button)
      await settle(() => document.querySelector(`select[aria-label="${t.list.sort}"]`) !== null, 'the sort inside it')
    } finally {
      restore()
    }
  })
})
