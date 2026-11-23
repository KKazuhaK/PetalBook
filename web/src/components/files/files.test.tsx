// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { markSignedIn } from '@/api/client'
import { mockFetch } from '@/api/mock/fetch'
import { createMockServer } from '@/api/mock/server'
import { PageListView } from '@/components/PageListView'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ThemeProvider } from '@/providers/theme'
import { t } from '@/i18n'
import { Breadcrumbs } from './Breadcrumbs'
import { FileOpsProvider } from './FileOps'

// Menus and dialogs are built only when they open, so a mistake in how they
// are put together shows up only then: each one here is opened.

let host: HTMLElement
let root: Root
let restoreFetch: typeof fetch

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  // jsdom has no media queries; the sidebar and the breadcrumbs ask for one.
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false, onchange: null })) as typeof window.matchMedia
  window.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Element.prototype.scrollIntoView ??= () => {}
})

const settle = async (check: () => boolean, what: string) => {
  for (let i = 0; i < 100; i++) {
    if (check()) return
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20))
    })
  }
  throw new Error(`never happened: ${what}`)
}

const byLabel = (label: string) => document.querySelector<HTMLElement>(`[aria-label="${label}"]`)
const menuItems = () => [...document.querySelectorAll('[role="menuitem"]')].map((e) => e.textContent?.trim())

async function mount(initial: string, ui: (path: string) => React.ReactNode, options: Parameters<typeof createMockServer>[0] = {}) {
  const server = createMockServer({ now: () => Date.now(), ...options })
  restoreFetch = globalThis.fetch
  globalThis.fetch = mockFetch(server)
  markSignedIn()
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () =>
    root.render(
      <QueryClientProvider client={qc}>
        <ThemeProvider>
          <TooltipProvider>
            <MemoryRouter initialEntries={[initial]}>
              <SidebarProvider>
                <QuickCaptureProvider>
                  <Routes>
                    <Route path="/s/:space/f/*" element={<FileOpsProvider>{ui(initial)}</FileOpsProvider>} />
                  </Routes>
                </QuickCaptureProvider>
              </SidebarProvider>
            </MemoryRouter>
            <Toaster />
          </TooltipProvider>
        </ThemeProvider>
      </QueryClientProvider>,
    ),
  )
}

beforeEach(() => localStorage.clear())
afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  document.body.innerHTML = ''
  globalThis.fetch = restoreFetch
})

const entry = (key: string) => document.querySelector<HTMLElement>(`[data-file-key="${key}"]`)

describe('the folder listing', () => {
  const listing = () => mount('/s/default/f', () => <PageListView source={{ kind: 'folder', folder: '' }} />)

  it('lists the subfolders before the pages, with the pages in and below each', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null && document.querySelectorAll('[data-file-key^="p:"]').length > 0, 'listing')
    const keys = [...document.querySelectorAll('[data-file-key]')].map((e) => e.getAttribute('data-file-key')!)
    expect(keys.findIndex((k) => k.startsWith('p:'))).toBeGreaterThan(keys.lastIndexOf('f:随笔'))
    // 随笔 holds 5 pages, 2026 four more below it.
    expect(entry('f:随笔')!.textContent).toContain(t.manage.folderPages(9))
    // The inbox is a folder like any other here.
    expect(entry('f:收件箱')).not.toBeNull()
  })

  it('opens the context menu on a folder, and on a page', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    await act(async () => {
      entry('f:随笔')!.querySelector('a')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 40, clientY: 40 }))
    })
    await settle(() => menuItems().length > 0, 'folder menu')
    expect(menuItems()).toEqual([t.manage.open, t.fav.addFolder, t.manage.rename, t.manage.setType, t.manage.select, t.manage.move, t.manage.delete])
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    await settle(() => menuItems().length === 0, 'menu closed')

    const page = document.querySelector<HTMLElement>('[data-file-key^="p:"]')!
    await act(async () => {
      page.querySelector('a')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 40, clientY: 40 }))
    })
    await settle(() => menuItems().length > 0, 'page menu')
    // A page is not renamed here (that is its title, in the editor).
    expect(menuItems()).toEqual([t.manage.open, t.fav.addFolder, t.manage.select, t.manage.move, t.manage.delete])
  })

  it('opens a menu on empty space with New page and New folder', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    await act(async () => {
      document.querySelector('[data-slot="context-menu-trigger"]')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 300, clientY: 500 }))
    })
    await settle(() => menuItems().length > 0, 'empty-space menu')
    expect(menuItems()).toEqual([t.manage.newPage, t.manage.newFolder])
  })

  it('the "…" button of an entry opens the same menu', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    await act(async () => {
      byLabel(`${t.manage.more}: 随笔`)!.click()
    })
    await settle(() => menuItems().length > 0, 'more menu')
    expect(menuItems()).toContain(t.manage.rename)
  })

  it('shows the selection bar once something is selected, and Clear removes it', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    expect(document.querySelector('[role="toolbar"]')).toBeNull()
    await act(async () => byLabel(t.manage.selectItem('随笔'))!.click())
    await act(async () => byLabel(t.manage.selectItem('归档'))!.click())
    const bar = document.querySelector('[role="toolbar"]')!
    expect(bar.textContent).toContain(t.manage.selected(2))
    expect(bar.textContent).toContain(t.manage.move)
    expect(bar.textContent).toContain(t.manage.delete)
    await act(async () => byLabel(t.manage.clearSelection)!.click())
    expect(document.querySelector('[role="toolbar"]')).toBeNull()
  })

  it('shift-click selects the run between two entries', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    const keys = [...document.querySelectorAll('[data-file-key]')].map((e) => e.getAttribute('data-file-key')!)
    await act(async () => byLabel(t.manage.selectItem('读书'))!.click())
    const last = entry(keys[3])!.querySelector<HTMLElement>('[role="checkbox"]')!
    await act(async () => last.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, shiftKey: true })))
    const from = keys.indexOf('f:读书')
    expect(document.querySelector('[role="toolbar"]')!.textContent).toContain(t.manage.selected(Math.abs(from - 3) + 1))
  })

  it('Delete asks to delete the selection (with the page count of its folders), Escape lets go', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    await act(async () => byLabel(t.manage.selectItem('随笔'))!.click())
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete', bubbles: true, cancelable: true }))
    })
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'delete dialog')
    const dialog = document.querySelector('[role="alertdialog"]')!.textContent!
    expect(dialog).toContain(t.manage.deleteTitleOne('随笔'))
    expect(dialog).toContain(t.manage.deleteFolderPages(9))
    await act(async () => {
      ;[...document.querySelectorAll<HTMLElement>('[role="alertdialog"] button')].find((b) => b.textContent === t.manage.cancel)!.click()
    })
    await settle(() => document.querySelector('[role="alertdialog"]') === null, 'dialog closed')
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    })
    expect(document.querySelector('[role="toolbar"]')).toBeNull()
  })

  it('Move opens the folder picker, which refuses the folder itself and what is inside it', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    await act(async () => byLabel(t.manage.selectItem('随笔'))!.click())
    await act(async () => {
      ;[...document.querySelectorAll<HTMLElement>('[role="toolbar"] button')].find((b) => b.textContent === t.manage.move)!.click()
    })
    await settle(() => document.querySelector('[role="radiogroup"]') !== null, 'picker')
    const options = [...document.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
    const disabled = options.filter((o) => o.disabled).map((o) => o.textContent)
    // The space it is already in, itself, and its subfolders.
    expect(disabled.join(' ')).toContain('随笔')
    expect(disabled.join(' ')).toContain('2026')
    const free = options.filter((o) => !o.disabled).map((o) => o.textContent)
    expect(free.join(' ')).toContain('归档')
    expect(free.join(' ')).not.toContain('2026')
  })

  it('the empty-space menu runs New folder: a dialog that asks for a name only', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'folders')
    await act(async () => {
      document.querySelector('[data-slot="context-menu-trigger"]')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 300, clientY: 500 }))
    })
    await settle(() => menuItems().length > 0, 'menu')
    await act(async () => {
      ;[...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === t.manage.newFolder)!.click()
    })
    await settle(() => document.querySelector('[role="dialog"]') !== null, 'new folder dialog')
    const dialog = document.querySelector('[role="dialog"]')!
    expect(dialog.querySelectorAll('input').length).toBe(1)
    expect(dialog.textContent).toContain(t.folder.nameLabel)
  })
})

describe('breadcrumbs', () => {
  it('fold a long path into a "…" menu that lists the hidden folders', async () => {
    await mount('/s/default/f/a/b/c/d/e', () => <Breadcrumbs folder="a/b/c/d/e" />)
    await settle(() => document.querySelector('nav') !== null, 'nav')
    const nav = document.querySelector('nav')!
    // The space, "…", and the last two.
    expect([...nav.querySelectorAll('a')].map((a) => a.textContent)).toEqual([expect.any(String), 'd', 'e'])
    expect(nav.querySelector('a[aria-current="page"]')!.textContent).toBe('e')
    await act(async () => {
      const more = byLabel(t.manage.trailMore)!
      more.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      more.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      more.click()
    })
    await settle(() => menuItems().length > 0, 'trail menu')
    expect(menuItems()).toEqual(['a', 'b', 'c'])
  })
})

describe('the sidebar tree', () => {
  const sidebar = () =>
    mount(
      '/s/default/f',
      () => <AppSidebar />,
      // The seed's inbox folder is called 收件箱.
      { inbox: '收件箱' },
    )
  const rowFor = (path: string) => document.querySelector<HTMLElement>(`[data-file-key="f:${path}"]`)

  it('starts with the top level, then its folders, without the inbox', async () => {
    await sidebar()
    await settle(() => rowFor('随笔') !== null, 'tree')
    expect(rowFor('收件箱')).toBeNull()
    expect(rowFor('归档')).not.toBeNull()
    const heading = [...document.querySelectorAll<HTMLAnchorElement>('a')].find((a) => a.textContent === t.nav.folders)!
    expect(heading.getAttribute('href')).toBe('/s/default/f')
    // The top level has a row of its own at the top of the tree, linking to it:
    // the pages that are in no folder are found there.
    const root = [...document.querySelectorAll<HTMLAnchorElement>('a')].find((a) => a.textContent === t.nav.rootFolder)
    expect(root?.getAttribute('href')).toBe('/s/default/f')
  })

  it('has the same context menu on a folder, without Select', async () => {
    await sidebar()
    await settle(() => rowFor('随笔') !== null, 'tree')
    await act(async () => {
      rowFor('随笔')!.querySelector('a')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 20, clientY: 200 }))
    })
    await settle(() => menuItems().length > 0, 'tree menu')
    expect(menuItems()).toEqual([t.manage.open, t.fav.addFolder, t.manage.rename, t.manage.setType, t.manage.move, t.manage.delete])
  })
})

// jsdom has no DataTransfer; the code only reads its types and sets two fields.
const dragEvent = (type: string) => {
  const dataTransfer = { types: ['application/x-petalbook-items'], setData() {}, effectAllowed: '', dropEffect: '' }
  return Object.assign(new Event(type, { bubbles: true, cancelable: true }), { dataTransfer })
}

describe('dragging in the listing', () => {
  const listing = () => mount('/s/default/f', () => <PageListView source={{ kind: 'folder', folder: '' }} />)
  const lit = (el: HTMLElement) => el.className.includes('ring-primary')

  it('lights up only the folders that can take what is dragged, and a drop moves it with an undo', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null && document.querySelector('[data-file-key^="p:"]') !== null, 'listing')
    const page = document.querySelector<HTMLElement>('[data-file-key^="p:"]')!
    const target = entry('f:归档')!
    await act(async () => void page.querySelector('a')!.dispatchEvent(dragEvent('dragstart')))
    await act(async () => void target.dispatchEvent(dragEvent('dragenter')))
    expect(lit(target)).toBe(true)
    await act(async () => void target.dispatchEvent(dragEvent('drop')))
    await settle(() => document.body.textContent!.includes(t.manage.moved(1, '归档')), 'moved toast')
    expect(document.body.textContent).toContain(t.manage.undo)
    // The page left the root listing.
    await settle(() => document.querySelector(`[data-file-key="${page.getAttribute('data-file-key')}"]`) === null, 'page gone from the root')
    await act(async () => void page.querySelector('a')?.dispatchEvent(dragEvent('dragend')))
  })

  it('does not light up a folder dragged onto itself', async () => {
    await listing()
    await settle(() => entry('f:随笔') !== null, 'listing')
    const folder = entry('f:随笔')!
    await act(async () => void folder.querySelector('a')!.dispatchEvent(dragEvent('dragstart')))
    await act(async () => void folder.dispatchEvent(dragEvent('dragenter')))
    expect(lit(folder)).toBe(false)
    // Another folder takes it.
    const other = entry('f:归档')!
    await act(async () => void other.dispatchEvent(dragEvent('dragenter')))
    expect(lit(other)).toBe(true)
    await act(async () => void folder.querySelector('a')!.dispatchEvent(dragEvent('dragend')))
  })
})
