// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { TopBar } from '@/components/layout/TopBar'
import { tn } from '@/i18n/notify'
import {
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  settle,
  shareSpace,
  type Mounted,
} from '@/testing/harness'
import type { MockServer } from '@/api/mock/server'
import NotificationsPage from './NotificationsPage'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route
        path="/"
        element={
          <>
            <TopBar />
            <p data-testid="home">home</p>
          </>
        }
      />
      <Route path="/notifications" element={<NotificationsPage />} />
      <Route path="/s/:space/check" element={<p data-testid="check">check</p>} />
      <Route path="/s/:space" element={<p data-testid="space">space</p>} />
      <Route path="/settings/:section" element={<p data-testid="settings">settings</p>} />
    </Routes>
  </SidebarProvider>
)

const bell = () => document.querySelector<HTMLElement>('[data-slot="bell"]')
const dot = () => document.querySelector<HTMLElement>('[data-slot="bell-dot"]')
const panel = () => document.querySelector<HTMLElement>('[data-slot="bell-panel"]')
const rows = () => [...document.querySelectorAll<HTMLElement>('[data-slot="notification"]')]
const text = () => document.body.textContent ?? ''
const filter = (f: string) => document.querySelector<HTMLElement>(`[data-filter="${f}"]`)
const dotShown = () => dot()?.className.includes('opacity-100') ?? false

// The signed-in administrator is added to a space by a person who owns it: that is one notification of the kind invite.
const invited = (server: MockServer, who = 'sharer') => shareSpace(server, 'editor', '团队', who)

async function open(entries: (server: MockServer) => string[]) {
  m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries })
  await settle(() => bell() !== null, 'the bell')
}

// jsdom has no media queries: this makes the phone-width one match.
function phone(on: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation(
    (q: string) =>
      ({
        matches: on && q.includes('max-width'),
        media: q,
        addEventListener() {},
        removeEventListener() {},
        addListener() {},
        removeListener() {},
        dispatchEvent: () => false,
        onchange: null,
      }) as unknown as MediaQueryList,
  )
}

describe('the bell', () => {
  it('has no dot while nothing is unread, and says how many when something is', async () => {
    await open(() => ['/'])
    await settle(() => bell()!.getAttribute('aria-label') === tn.bell.label, 'the plain label')
    expect(dotShown()).toBe(false)
    const { space } = invited(m!.server)
    expect(space).toBeTruthy()
    await m!.qc.invalidateQueries({ queryKey: ['notifications'] })
    await settle(() => bell()!.getAttribute('aria-label') === tn.bell.unread(1), 'one unread')
    expect(dotShown()).toBe(true)
  })

  it('opens a popover with the latest items: title, body, when, and a dot while unread', async () => {
    await open((server) => (invited(server), ['/']))
    await settle(() => dotShown(), 'the dot')
    await click(bell())
    await settle(() => rows().length === 1, 'the item')
    const row = rows()[0]
    expect(row.dataset.unread).toBe('true')
    expect(row.textContent).toContain('名 sharer把你加入了「团队」')
    expect(row.textContent).toContain('你在里面是编辑者')
    expect(row.querySelector('[data-slot="notification-dot"]')!.className).toContain('bg-destructive')
    expect(panel()!.textContent).toContain(tn.markAll)
    expect(panel()!.textContent).toContain(tn.viewAll)
  })

  it('"mark all as read" clears the dot and the unread marks', async () => {
    await open((server) => (invited(server), ['/']))
    await settle(() => dotShown(), 'the dot')
    await click(bell())
    await settle(() => rows().length === 1, 'the item')
    await click([...panel()!.querySelectorAll('button')].find((b) => b.textContent === tn.markAll))
    await settle(() => !dotShown() && rows()[0]?.dataset.unread === 'false', 'everything read')
    expect(bell()!.getAttribute('aria-label')).toBe(tn.bell.label)
    expect(m!.requests.some((r) => r.method === 'POST' && r.url === '/api/notifications/read-all')).toBe(true)
  })

  it('clicking an item goes to its target and marks it read', async () => {
    let space = ''
    await open((server) => ((space = invited(server).space), ['/']))
    await settle(() => dotShown(), 'the dot')
    await click(bell())
    await settle(() => rows().length === 1, 'the item')
    await click(rows()[0])
    await settle(() => locationText() === `/s/${space}/`, 'the space')
    const sent = m!.requests.find((r) => r.method === 'POST' && r.url === '/api/notifications/read')
    expect(sent).toBeTruthy()
    await settle(() => m!.qc.getQueryData<number>(['notifications', 'unread']) === 0, 'the count')
  })

  it('"see all" goes to the full page', async () => {
    await open((server) => (invited(server), ['/']))
    await click(bell())
    await settle(() => panel() !== null, 'the popover')
    await click([...panel()!.querySelectorAll('a')].find((a) => a.textContent === tn.viewAll))
    await settle(() => locationText() === '/notifications', 'the page')
  })

  it('says so when there is nothing, with what would be told here', async () => {
    await open(() => ['/'])
    await click(bell())
    await settle(() => panel()?.textContent?.includes(tn.empty) === true, 'the empty popover')
    expect(panel()!.textContent).toContain(tn.emptyHelp)
  })

  it('on a phone goes straight to the full page, with no popover', async () => {
    phone(true)
    await open((server) => (invited(server), ['/']))
    expect(bell()!.tagName).toBe('A')
    expect(bell()!.getAttribute('href')).toBe('/notifications')
    await click(bell())
    await settle(() => locationText() === '/notifications', 'the page')
    expect(panel()).toBeNull()
  })

  it('is not there for someone who is not signed in', async () => {
    m = await mount(routes, { server: { mode: 'accounts' }, entries: ['/'] })
    await settle(() => document.querySelector('[data-testid="home"]') !== null, 'the page')
    expect(bell()).toBeNull()
  })

  it('is 40 px: the tap area the owner asked for', async () => {
    await open(() => ['/'])
    expect(bell()!.className).toContain('size-10')
  })
})

describe('the full page', () => {
  // Another person's note for each kind there is a producer for, and a problem of the space.
  const scene = (server: MockServer) => {
    invited(server)
    const owner = makePerson(server, 'second')
    const sp = (owner.request('POST', '/api/spaces', { name: '第二个' }).body as { id: string }).id
    owner.request('POST', `/api/spaces/${sp}/members`, { username: 'admin', role: 'owner' })
    return sp
  }

  it('lists everything newest first, and the filters narrow it by kind', async () => {
    await open((server) => (scene(server), ['/notifications']))
    await settle(() => rows().length === 2, 'two items')
    expect(rows()[0].textContent).toContain('第二个')
    expect(rows()[1].textContent).toContain('团队')
    expect(filter('all')!.getAttribute('aria-pressed')).toBe('true')
    await click(filter('invite'))
    await settle(() => filter('invite')!.getAttribute('aria-pressed') === 'true' && rows().length === 2, 'invitations')
    await click(filter('sync'))
    await settle(() => text().includes(tn.emptyKind), 'no sync notices yet')
    await click(filter('mention'))
    await settle(() => text().includes(tn.emptyKind), 'mentions are reserved: nothing yet')
    expect(locationText()).toContain('show=mention')
  })

  it('shows the problems of a space the person writes in, and leads to the page that lists them', async () => {
    let space = ''
    await open((server) => {
      space = invited(server).space
      // A file that is not a page: the check page lists it.
      server.addOrphan(space, { path: 'stray.md', kind: 'no_id', meta: { title: '无 id' }, body: 'x\n' })
      return ['/notifications?show=problems']
    })
    await settle(() => rows().length === 1, 'the problem')
    expect(rows()[0].textContent).toContain('「团队」有 1 个问题')
    expect(rows()[0].textContent).toContain(tn.ongoing)
    await click(rows()[0])
    await settle(() => locationText() === `/s/${space}/check`, 'the check page')
  })

  it('pages the history: a button brings the earlier ones', async () => {
    await open((server) => {
      for (let i = 0; i < 23; i++) {
        const owner = makePerson(server, `owner${i}`)
        const sp = (owner.request('POST', '/api/spaces', { name: `空间${i}` }).body as { id: string }).id
        owner.request('POST', `/api/spaces/${sp}/members`, { username: 'admin', role: 'viewer' })
      }
      return ['/notifications']
    })
    await settle(() => rows().length === 20, 'the first page')
    const more = [...document.querySelectorAll('button')].find((b) => b.textContent === tn.more)
    expect(more).toBeTruthy()
    await click(more)
    await settle(() => rows().length === 23, 'the rest')
    expect([...document.querySelectorAll('button')].some((b) => b.textContent === tn.more)).toBe(false)
  })

  it('has a way back on a phone, and the filters scroll sideways in their own row', async () => {
    phone(true)
    await open((server) => (invited(server), ['/notifications']))
    await settle(() => rows().length === 1, 'the item')
    expect(document.querySelector('a[aria-label="' + tn.back + '"]')!.getAttribute('href')).toBe('/')
    const group = document.querySelector<HTMLElement>('[role="group"]')!
    expect(group.className).toContain('overflow-x-auto')
    for (const f of document.querySelectorAll<HTMLElement>('[data-filter]')) expect(f.className).toContain('h-10')
  })

  it('"mark all as read" on the page marks them', async () => {
    await open((server) => (scene(server), ['/notifications']))
    await settle(() => rows().length === 2, 'two items')
    await click([...document.querySelectorAll('button')].find((b) => b.textContent === tn.markAll))
    await settle(() => rows().every((r) => r.dataset.unread === 'false'), 'all read')
    await click(filter('unread'))
    await settle(() => text().includes(tn.emptyUnread), 'nothing unread')
  })
})
