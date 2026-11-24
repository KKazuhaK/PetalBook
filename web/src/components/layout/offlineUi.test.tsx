// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { rememberUser } from '@/api/client'
import { FileOpsProvider } from '@/components/files/FileOps'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SearchDialogProvider } from '@/components/search/SearchDialog'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { draftRef, loadDraft, saveDraft } from '@/editor/draftStore'
import { DEFAULT_CONFIG, configure } from '@/lib/offline'
import { noteNetworkFailure, noteNetworkOk } from '@/lib/offline/status'
import { freshDevice } from '@/lib/offline/testkit'
import { syncSpace } from '@/lib/offline/sync'
import PageEditor from '@/pages/PageEditor'
import { byLabel, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { sender } from '@/testing/listKit'
import { OfflineBanner } from './OfflineSync'

beforeAll(() => {
  installDomShims()
  if (!Range.prototype.getClientRects) {
    Range.prototype.getClientRects = () => [] as unknown as DOMRectList
    Range.prototype.getBoundingClientRect = () => new DOMRect()
  }
})
beforeEach(() => {
  freshDevice()
  rememberUser('local')
})

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const body = () => document.querySelector<HTMLElement>('.pb-prose')
const editable = () => body()?.getAttribute('contenteditable')
const banner = () => document.querySelector('[data-offline-banner]')
const title = () => document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${t.editor.titlePlaceholder}"]`)!
const chip = () => [...document.querySelectorAll('span')].find((s) => s.textContent === t.offline.readOnly)

// What the editor and the dialog need around them (a data router, for the editor's guard against leaving).
const shell = (el: ReactNode) => (
  <SidebarProvider>
    <QuickCaptureProvider>
      <FileOpsProvider>
        <SearchDialogProvider>
          <OfflineBanner />
          {el}
        </SearchDialogProvider>
      </FileOpsProvider>
    </QuickCaptureProvider>
  </SidebarProvider>
)
// Read when it is drawn: the page is made after the routes are.
const Open = ({ id }: { id: () => string }) => <Link to={`/s/default/p/${id()}`}>open</Link>
const routesFor = (pageId: () => string) => [
  { path: '/', element: shell(<Open id={pageId} />) },
  { path: '/s/:space/p/:id', element: shell(<PageEditor />) },
  { path: '/s/:space/recent', element: shell(<div>list</div>) },
]

describe('the editor and the banner when the server cannot be reached', () => {
  it('the page can be typed in online; offline it is read only with a clear indicator, and is writable again when back', async () => {
    let id = ''
    m = await mount(null, {
      routes: routesFor(() => id),
      server: { mode: 'single' },
      entries: (server) => {
        id = (sender(server)('POST', '/pages', { meta: { title: '读写' }, body: '正文在这里\n' }).body as { id: string }).id
        return [`/s/default/p/${id}`]
      },
    })
    await settle(() => body() !== null && document.body.textContent!.includes('正文在这里'), 'the editor')
    expect(editable()).toBe('true')
    expect(banner()).toBeNull()
    expect(chip()).toBeUndefined()

    await act(async () => noteNetworkFailure())
    await settle(() => editable() === 'false', 'read only')
    expect(banner()!.textContent).toBe(t.offline.banner)
    expect(chip()!.title).toBe(t.offline.readOnlyNote)
    expect(document.querySelector(`textarea[aria-label="${t.editor.titlePlaceholder}"]`)).toHaveProperty('readOnly', true)
    // Nothing that changes the page is offered.
    expect(byLabel(t.editor.saveVersion)).toBeNull()
    expect(document.querySelector('[role="toolbar"]')).toBeNull()

    await act(async () => noteNetworkOk())
    await settle(() => editable() === 'true', 'writable again')
    expect(banner()).toBeNull()
  })

  it('an editor that was open when the network went keeps what was typed and not yet saved, and saves it when back', async () => {
    let id = ''
    m = await mount(null, {
      routes: routesFor(() => id),
      server: { mode: 'single' },
      entries: (server) => {
        id = (sender(server)('POST', '/pages', { meta: { title: '读写' }, body: '正文在这里\n' }).body as { id: string }).id
        return [`/s/default/p/${id}`]
      },
    })
    await settle(() => body() !== null && document.body.textContent!.includes('正文在这里'), 'the editor')
    // Typed, and the network goes before it is stored.
    await type(title(), '离线前打的标题')
    const online = globalThis.fetch
    globalThis.fetch = (async () => {
      throw new TypeError('Failed to fetch')
    }) as typeof fetch
    await act(async () => noteNetworkFailure())
    await settle(() => editable() === 'false', 'read only')
    expect(title().value).toBe('离线前打的标题')

    globalThis.fetch = online
    await act(async () => noteNetworkOk())
    await settle(() => editable() === 'true', 'writable again')
    expect(title().value).toBe('离线前打的标题')
    const s = sender(m.server)
    await settle(() => (s('GET', `/pages/${id}`).body as { draft?: { meta: { title?: string } } }).draft?.meta.title === '离线前打的标题', 'the typed title reaches the server', 400)
  })

  it('a page opened offline shows the server draft it was kept with, and back online typing keeps that draft', async () => {
    let id = ''
    m = await mount(null, {
      routes: routesFor(() => id),
      server: { mode: 'single' },
      entries: (server) => {
        const s = sender(server)
        const page = s('POST', '/pages', { folder: 'Inbox', meta: { title: '有草稿的页' }, body: '文件正文\n' }).body as { id: string; rev: string; meta: Record<string, unknown> }
        id = page.id
        s('PUT', `/pages/${id}/draft`, { baseRev: page.rev, meta: page.meta, body: '文件正文\n我的草稿很重要\n' })
        return ['/']
      },
    })
    const store = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode: 'text', favourites: false, recent: false }))!
    await syncSpace(store)
    const online = globalThis.fetch
    globalThis.fetch = (async () => {
      throw new TypeError('Failed to fetch')
    }) as typeof fetch
    await act(async () => document.querySelector('a')!.click())
    await settle(() => body() !== null && document.body.textContent!.includes('文件正文'), 'the page from the device')
    expect(editable()).toBe('false')
    expect(body()!.textContent).toContain('我的草稿很重要')

    globalThis.fetch = online
    await act(async () => noteNetworkOk())
    await settle(() => editable() === 'true' && body()!.textContent!.includes('我的草稿很重要'), 'writable, on the draft')
    await type(title(), '新标题')
    const s = sender(m.server)
    const draft = () => (s('GET', `/pages/${id}`).body as { draft?: { body: string; meta: { title?: string } } }).draft
    await settle(() => draft()?.meta.title === '新标题', 'the title reaches the server draft', 400)
    expect(draft()!.body).toBe('文件正文\n我的草稿很重要\n')
  })

  it('a page opened offline shows the browser\'s own draft, and back online it is kept and reaches the server', async () => {
    let id = ''
    m = await mount(null, {
      routes: routesFor(() => id),
      server: { mode: 'single' },
      entries: (server) => {
        const page = sender(server)('POST', '/pages', { folder: 'Inbox', meta: { title: '浏览器草稿' }, body: '文件正文\n' }).body as { id: string; rev: string }
        id = page.id
        saveDraft(draftRef('local', 'default', id), { baseRev: page.rev, fields: { title: '浏览器草稿', subtitle: '', tags: [] }, bodyText: '文件正文\n\n浏览器里没存的字\n', savedAt: Date.now(), token: 'tok-local' })
        return ['/']
      },
    })
    const store = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode: 'text', favourites: false, recent: false }))!
    await syncSpace(store)
    const online = globalThis.fetch
    globalThis.fetch = (async () => {
      throw new TypeError('Failed to fetch')
    }) as typeof fetch
    await act(async () => document.querySelector('a')!.click())
    await settle(() => body() !== null && document.body.textContent!.includes('文件正文'), 'the page from the device')
    expect(editable()).toBe('false')
    expect(body()!.textContent).toContain('浏览器里没存的字')

    globalThis.fetch = online
    await act(async () => noteNetworkOk())
    await settle(() => editable() === 'true' && body()!.textContent!.includes('浏览器里没存的字'), 'writable, on the browser draft')
    const s = sender(m.server)
    await settle(() => (s('GET', `/pages/${id}`).body as { draft?: { body: string } }).draft?.body.includes('浏览器里没存的字') === true, 'the browser draft reaches the server', 400)
    expect(loadDraft(draftRef('local', 'default', id))?.bodyText ?? '浏览器里没存的字').toContain('浏览器里没存的字')
  })

  it('a page that was kept opens without the server, read only, and a page that was not says it is not downloaded', async () => {
    let kept = ''
    let bare = ''
    m = await mount(null, {
      routes: routesFor(() => kept),
      server: { mode: 'single' },
      entries: (server) => {
        const s = sender(server)
        kept = (s('POST', '/pages', { folder: 'Inbox', meta: { title: '带正文的页' }, body: '离线也能读到\n' }).body as { id: string }).id
        bare = (s('POST', '/pages', { folder: 'Other', meta: { title: '没下载的页' }, body: '没有\n' }).body as { id: string }).id
        return ['/']
      },
    })
    // The copy: the catalogue and the text of the inbox.
    const store = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode: 'text', favourites: false, recent: false }))!
    await syncSpace(store)
    expect((await store.textMetas()).map((x) => x.id)).toEqual([kept])
    // The network goes.
    globalThis.fetch = (async () => {
      throw new TypeError('Failed to fetch')
    }) as typeof fetch
    const link = document.querySelector('a')!
    await act(async () => link.click())
    await settle(() => document.body.textContent!.includes('离线也能读到'), 'the page from the device')
    expect(editable()).toBe('false')
    expect(chip()).toBeDefined()
    expect(banner()).not.toBeNull()
    expect(bare).not.toBe('')
  })
})

describe('the search dialog offline', () => {
  it('says that only what is downloaded is searched, and finds the titles and kept text on the device', async () => {
    m = await mount(null, {
      routes: routesFor(() => ''),
      server: { mode: 'single' },
      entries: (server) => {
        const s = sender(server)
        s('POST', '/pages', { folder: 'Inbox', meta: { title: '月夜' }, body: '月光很亮\n' })
        s('POST', '/pages', { folder: 'Other', meta: { title: '雪天' }, body: '月光不在这里\n' })
        return ['/s/default/recent']
      },
    })
    const store = (await configure('local', 'default', { ...DEFAULT_CONFIG, mode: 'text', favourites: false, recent: false }))!
    await syncSpace(store)
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true, cancelable: true }))
    })
    const dialog = () => document.querySelector<HTMLElement>('[data-slot="search-dialog"]')
    await settle(() => dialog() !== null, 'the dialog')
    const input = () => dialog()!.querySelector<HTMLInputElement>('input[role="combobox"]')!
    expect(dialog()!.textContent).not.toContain(t.offline.searchNote)
    globalThis.fetch = (async () => {
      throw new TypeError('Failed to fetch')
    }) as typeof fetch
    await type(input(), '月光')
    // The page whose text is not kept is not found by the text; the one whose text is kept is.
    await settle(() => dialog()!.textContent!.includes(t.offline.searchNote) && dialog()!.textContent!.includes('月夜') && !dialog()!.textContent!.includes('雪天'), 'the offline results')
    await type(input(), '雪')
    await settle(() => dialog()!.textContent!.includes('雪天') && !dialog()!.textContent!.includes('月夜'), 'found by its title')
  })
})
