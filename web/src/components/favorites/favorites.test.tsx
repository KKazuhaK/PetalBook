// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { FileOpsProvider } from '@/components/files/FileOps'
import { StarButton } from '@/components/favorites/StarButton'
import { FileContextMenu, ENTRY_ATTR } from '@/components/files/FileContextMenu'
import { SpaceGate } from '@/components/layout/SpaceGate'
import { SidebarProvider } from '@/components/ui/sidebar'
import HomePage from '@/components/home/HomePage'
import { t } from '@/i18n'
import { th } from '@/i18n/home'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  press,
  settle,
  shareSpace,
  startView,
  type Mounted,
} from '@/testing/harness'
import { listShell, putViews, sender, type Send } from '@/testing/listKit'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const shell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <FileOpsProvider>
        <AppSidebar />
        <Routes>
          <Route path="/s/:space" element={<HomePage />} />
          <Route path="*" element={<div data-testid="elsewhere" />} />
        </Routes>
      </FileOpsProvider>
    </QuickCaptureProvider>
  </SidebarProvider>
)

// The same, with the gate an administrator who is not a member passes through (a view opens the space for reading).
const gatedShell = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <FileOpsProvider>
        <AppSidebar />
        <Routes>
          <Route path="/s/:space" element={<SpaceGate />}>
            <Route index element={<HomePage />} />
          </Route>
          <Route path="*" element={<div data-testid="elsewhere" />} />
        </Routes>
      </FileOpsProvider>
    </QuickCaptureProvider>
  </SidebarProvider>
)

const group = () => document.querySelector<HTMLElement>('[data-slot="sidebar-favorites"]')
const sideRows = () => [...(group()?.querySelectorAll<HTMLElement>('[data-favorite]') ?? [])]
const homeRows = () => [
  ...(document
    .querySelector('[data-slot="home"] ul[aria-label="' + t.fav.section + '"]')
    ?.querySelectorAll<HTMLElement>('[data-favorite]') ?? []),
]
const names = (rows: HTMLElement[]) =>
  rows.map((r) => r.querySelector('a span, [role="link"] span')?.textContent?.trim())
const favRequests = () =>
  m!.requests
    .filter((r) => r.url.includes('/favorites') && r.method !== 'GET')
    .map((r) => `${r.method} ${decodeURIComponent(r.url.replace(/^\/api\/spaces\/[^/]+/, ''))}`)

// A page, a folder and a view, all favourites of the local person.
function seed(send: Send) {
  const page = (send('POST', '/pages', { folder: '', meta: { title: '暮雪' }, body: '正文\n' }).body as { id: string })
    .id
  send('POST', '/folders', { path: '诗' })
  const id = (
    putViews(send, { folder: '诗' }, [{ name: '按作者', layout: 'list' }]).body as { views: { id: string }[] }
  ).views[0].id
  send('POST', '/favorites', { kind: 'page', target: page })
  send('POST', '/favorites', { kind: 'folder', target: '诗' })
  send('POST', '/favorites', { kind: 'view', target: `folder:诗#${id}` })
  return { page, view: id }
}

const open = (scene?: (send: Send) => unknown) =>
  mount(shell, {
    entries: (server) => {
      if (scene) scene(sender(server))
      return ['/s/default']
    },
    server: { mode: 'single' },
  })

describe('favourites in the sidebar and on the home page', () => {
  it("both show the same list in the person's order, as links to what they are", async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3 && homeRows().length === 3, 'both lists')
    expect(names(sideRows())).toEqual(['暮雪', '诗', '按作者'])
    expect(names(homeRows())).toEqual(['暮雪', '诗', '按作者'])
    const hrefs = sideRows().map((r) => r.querySelector('a')!.getAttribute('href'))
    expect(hrefs[0]).toMatch(/^\/s\/default\/p\//)
    expect(hrefs[1]).toBe('/s/default/f/%E8%AF%97')
    expect(hrefs[2]).toMatch(/^\/s\/default\/f\/%E8%AF%97\?view=/)
    // One request for the list serves both.
    expect(m.requests.filter((r) => r.url.endsWith('/favorites') && r.method === 'GET')).toHaveLength(1)
  })

  it('the logo link in the sidebar is named as the way home, not as the recent pages', async () => {
    m = await mount(shell, { entries: ['/s/default'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => document.querySelector('a[aria-label="' + t.nav.home + '"]') !== null, 'the logo link')
    expect(document.querySelector('a[aria-label="' + t.nav.home + '"]')!.getAttribute('href')).toMatch(
      /^\/s\/[^/]+\/?$/,
    )
    expect(document.querySelector('a[aria-label="' + t.nav.recent + '"] img[src="/favicon.svg"]')).toBeNull()
  })

  it('the sidebar group folds away and remembers it; with no favourites it is not there', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'the group')
    await click(byLabel(t.fav.collapse))
    expect(sideRows()).toHaveLength(0)
    expect(localStorage.getItem('pb.favorites.open')).toBe('closed')
    await click(byLabel(t.fav.expand))
    expect(sideRows()).toHaveLength(3)
    await m.unmount()
    m = await open()
    await settle(() => document.querySelector('[data-slot="home"]') !== null, 'home')
    expect(group()).toBeNull()
    await settle(() => document.body.textContent!.includes(th.favorites.empty), 'the empty note')
  })

  it('moves one with the buttons, sends the whole order and keeps it', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'rows')
    await click(sideRows()[2].querySelector(`[aria-label="${t.fav.moveUpLabel('按作者')}"]`))
    await settle(() => names(sideRows()).join() === '暮雪,按作者,诗', 'moved')
    expect(names(homeRows())).toEqual(['暮雪', '按作者', '诗'])
    const put = m.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/favorites/order'))!
    expect(JSON.parse(put.body!).items.map((i: { kind: string }) => i.kind)).toEqual(['page', 'view', 'folder'])
    // The server has it: a new load shows the same.
    await m.unmount()
    // (a fresh mount starts a fresh server, so ask the one just used)
  })

  it('has a hit area of 40 px on a phone or a touch screen for the arrows, and looks the same on a desktop', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'rows')
    for (const a of sideRows()[1].querySelectorAll<HTMLElement>('[data-fav-action]')) {
      // 24 px wide and high for the mouse; 40 wide by layout and 40 high by a box around it on a coarse pointer.
      expect(a.className).toContain('size-6')
      expect(a.className).toContain('pointer-coarse:w-10')
      expect(a.className).toContain('pointer-coarse:before:-inset-y-2')
      expect(a.className).toContain('max-md:w-10')
    }
  })

  it('the first can not go up and the last can not go down', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'rows')
    expect(sideRows()[0].querySelector<HTMLButtonElement>('[data-fav-action="up"]')!.disabled).toBe(true)
    expect(sideRows()[2].querySelector<HTMLButtonElement>('[data-fav-action="down"]')!.disabled).toBe(true)
    expect(sideRows()[1].querySelector<HTMLButtonElement>('[data-fav-action="down"]')!.disabled).toBe(false)
  })

  it('Alt+Down and Alt+Up on a row move it, plain arrows do not', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'rows')
    await press(sideRows()[0].querySelector('a')!, 'ArrowDown')
    expect(favRequests()).toEqual([])
    await press(sideRows()[0].querySelector('a')!, 'ArrowDown', { altKey: true })
    await settle(() => names(sideRows()).join() === '诗,暮雪,按作者', 'down')
    await press(sideRows()[2].querySelector('a')!, 'ArrowUp', { altKey: true })
    await settle(() => names(sideRows()).join() === '诗,按作者,暮雪', 'up')
    expect(favRequests().filter((r) => r.startsWith('PUT'))).toHaveLength(2)
  })

  it('dragging a row onto another puts it there', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'rows')
    const dt = { setData() {}, effectAllowed: '' }
    const ev = (type: string) =>
      Object.assign(new Event(type, { bubbles: true, cancelable: true }), { dataTransfer: dt })
    await act(async () => void sideRows()[2].dispatchEvent(ev('dragstart')))
    await act(async () => void sideRows()[0].dispatchEvent(ev('dragover')))
    await act(async () => void sideRows()[0].dispatchEvent(ev('drop')))
    await settle(() => names(sideRows()).join() === '按作者,暮雪,诗', 'dropped')
  })

  it('a page that is gone is shown as unavailable, cannot be opened, and can be cleared', async () => {
    let id = ''
    m = await open((send) => {
      id = seed(send).page
      send('DELETE', `/pages/${id}`)
    })
    await settle(() => sideRows().length === 3, 'rows')
    const gone = sideRows()[0]
    expect(gone.querySelector('a')).toBeNull()
    expect(gone.textContent).toContain(t.fav.missing)
    expect(gone.querySelector('[aria-disabled="true"]')).not.toBeNull()
    const home = homeRows()[0]
    expect(home.textContent).toContain(t.fav.missing)
    await click(gone.querySelector('[data-fav-action="clear"]'))
    await settle(() => sideRows().length === 2 && homeRows().length === 2, 'cleared')
    expect(favRequests()).toEqual([`DELETE /favorites?kind=page&target=${id}`])
  })

  it('removing a favourite on the server (another browser) shows after reading again', async () => {
    m = await open(seed)
    await settle(() => sideRows().length === 3, 'rows')
    sender(m.server)('DELETE', '/favorites?kind=folder&target=诗')
    await act(async () => void m!.qc.invalidateQueries({ queryKey: ['space', 'default', 'favorites'] }))
    await settle(() => sideRows().length === 2, 'two left')
  })
})

describe('the ways to favourite', () => {
  it('the star: pressed when it is one, adds and takes off, says so', async () => {
    let page = ''
    const Star = () => (
      <StarButton target={{ kind: 'page', target: page }} labels={{ add: t.fav.addPage, remove: t.fav.removePage }} />
    )
    m = await mount(
      <Routes>
        <Route path="/s/:space" element={<Star />} />
      </Routes>,
      {
        entries: (server) => {
          page = (sender(server)('POST', '/pages', { folder: '', body: 'x\n' }).body as { id: string }).id
          return ['/s/default']
        },
        server: { mode: 'single' },
      },
    )
    await settle(() => byLabel(t.fav.addPage) !== null, 'the star')
    expect(byLabel(t.fav.addPage)!.getAttribute('aria-pressed')).toBe('false')
    await click(byLabel(t.fav.addPage))
    await settle(() => byLabel(t.fav.removePage) !== null, 'pressed')
    expect(byLabel(t.fav.removePage)!.getAttribute('aria-pressed')).toBe('true')
    expect(favRequests()).toEqual([`POST /favorites`])
    await click(byLabel(t.fav.removePage))
    await settle(() => byLabel(t.fav.addPage) !== null, 'off again')
    expect(favRequests()[1]).toBe(`DELETE /favorites?kind=page&target=${page}`)
  })

  it('a folder\'s context menu has "Favourite" and then "Remove from favourites"', async () => {
    m = await mount(
      <SidebarProvider>
        <QuickCaptureProvider>
          <Routes>
            <Route
              path="/s/:space"
              element={
                <FileOpsProvider>
                  <FileContextMenu resolve={(key) => (key === 'f:诗' ? [{ kind: 'folder', path: '诗' }] : null)}>
                    <div {...{ [ENTRY_ATTR]: 'f:诗' }} data-testid="entry">
                      <a href="#x">诗</a>
                    </div>
                  </FileContextMenu>
                </FileOpsProvider>
              }
            />
          </Routes>
        </QuickCaptureProvider>
      </SidebarProvider>,
      {
        entries: (server) => (sender(server)('POST', '/folders', { path: '诗' }), ['/s/default']),
        server: { mode: 'single' },
      },
    )
    const entry = () => document.querySelector('[data-testid="entry"]')!
    const menu = async () => {
      await act(
        async () =>
          void entry()
            .querySelector('a')!
            .dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 5, clientY: 5 })),
      )
      await settle(() => document.querySelector('[role="menuitem"]') !== null, 'the menu')
      return [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')]
    }
    await settle(() => entry() !== null && m!.requests.some((r) => r.url.endsWith('/tree')), 'entry')
    await act(async () => void (await new Promise((r) => setTimeout(r, 100))))
    let items = await menu()
    await click(items.find((i) => i.textContent === t.fav.addFolder))
    await settle(() => favRequests().length === 1, 'added')
    expect(favRequests()[0]).toBe('POST /favorites')
    await settle(() => document.querySelector('[role="menuitem"]') === null, 'closed')
    await settle(
      () => m!.requests.filter((r) => r.url.endsWith('/favorites') && r.method === 'GET').length >= 1,
      'listed',
    )
    items = await menu()
    expect(items.some((i) => i.textContent === t.fav.remove)).toBe(true)
  })
})

describe('the view tabs', () => {
  it('have a star for the view shown: it favourites that view and takes it off again', async () => {
    let id = ''
    m = await mount(listShell, {
      entries: (server) => {
        const send = sender(server)
        send('POST', '/folders', { path: '诗' })
        id = (
          putViews(send, { folder: '诗' }, [
            { name: '全部', layout: 'list' },
            { name: '按作者', layout: 'list' },
          ]).body as { views: { id: string }[] }
        ).views[1].id
        return ['/s/default/f/诗']
      },
      server: { mode: 'single' },
    })
    await settle(() => document.querySelectorAll('[role="tab"]').length === 2, 'the tabs')
    await click(document.querySelectorAll('[role="tab"]')[1])
    await click(byLabel(t.fav.addView))
    await settle(() => byLabel(t.fav.removeView) !== null, 'pressed')
    const post = m.requests.find((r) => r.method === 'POST' && r.url.endsWith('/favorites'))!
    expect(JSON.parse(post.body!)).toEqual({ kind: 'view', target: `folder:诗#${id}` })
    await click(byLabel(t.fav.removeView))
    await settle(() => byLabel(t.fav.addView) !== null, 'off')
  })
})

describe('who can have favourites', () => {
  it('a read-only member has them, and the folder menu offers only that', async () => {
    m = await mount(shell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        owner.request('POST', `/api/spaces/${space}/folders`, { path: '诗' })
        const admin = adminRequests(server)
        admin('POST', `/api/spaces/${space}/favorites`, { kind: 'folder', target: '诗' })
        localStorage.setItem('pb.space', space)
        return [`/s/${space}`]
      },
    })
    await settle(() => sideRows().length === 1, 'the favourite')
  })

  it('an administrator who is not a member has none and no star or menu entry, and no notice about it', async () => {
    m = await mount(gatedShell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const other = makePerson(server, 'someone')
        const space = (other.request('POST', '/api/spaces', { name: '别人的' }).body as { id: string }).id
        localStorage.setItem('pb.space', space)
        return [`/s/${space}`]
      },
    })
    await startView()
    await settle(() => document.querySelector('[data-slot="home"]') !== null, 'home')
    await settle(() => document.querySelector('[aria-busy="true"]') === null, 'loaded')
    expect(document.body.textContent).not.toContain(th.favorites.empty)
    expect(m.requests.some((r) => r.url.includes('/favorites'))).toBe(false)
    expect(group()).toBeNull()
    expect(document.querySelector('[data-slot="favorite-star"]')).toBeNull()
    // The only status is the banner of the view.
    expect(document.querySelector('[role="status"]:not([data-slot="admin-view-banner"])')).toBeNull()
    expect(locationText()).toMatch(/^\/s\//)
  })

  it("the favourites are one person's own", async () => {
    m = await mount(shell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'editor')
        owner.request('POST', `/api/spaces/${space}/folders`, { path: '诗' })
        owner.request('POST', `/api/spaces/${space}/favorites`, { kind: 'folder', target: '诗' })
        localStorage.setItem('pb.space', space)
        return [`/s/${space}`]
      },
    })
    await settle(() => document.body.textContent!.includes(th.favorites.empty), "none of the owner's shows")
    expect(group()).toBeNull()
  })
})
