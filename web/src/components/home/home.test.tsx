// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { th } from '@/i18n/home'
import { adminRequests, click, installDomShims, locationText, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { sender, type Send } from '@/testing/listKit'
import HomePage from './HomePage'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  vi.useRealTimers()
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/s/:space" element={<HomePage />} />
      <Route path="/s/:space/p/:id" element={<div data-testid="editor" />} />
      <Route path="/settings/:section?" element={<div data-testid="settings" />} />
    </Routes>
  </SidebarProvider>
)

const home = () => document.querySelector<HTMLElement>('[data-slot="home"]')
const greeting = () => document.querySelector('[data-slot="greeting"]')?.textContent ?? ''
const daily = () => document.querySelector<HTMLElement>('[data-slot="daily"]')
const another = () => document.querySelector<HTMLButtonElement>('[data-slot="daily-another"]')
const section = (title: string) => [...document.querySelectorAll('section')].find((s) => s.querySelector('h2')?.textContent === title) ?? null
const spaceRequests = () => m!.requests.filter((r) => r.url.startsWith('/api/spaces/')).map((r) => `${r.method} ${r.url.replace(/^\/api\/spaces\/[^/]+/, '')}`)
const ready = () => settle(() => home() !== null && greeting() !== '' && document.querySelector('[aria-busy="true"]') === null, 'the home page')

// A folder of texts, pages in it, and the space's daily text coming from it.
function seedTexts(send: Send, count = 3) {
  send('POST', '/folders', { path: '文案' })
  const ids = Array.from({ length: count }, (_, i) => (send('POST', '/pages', { folder: '文案', meta: { title: `暮雪${i}`, subtitle: `副${i}` }, body: `第 ${i} 条文案\n` }).body as { id: string }).id)
  expect(send('PUT', '/daily/settings', { daily: { folder: '文案' } }).status).toBe(200)
  return ids
}

describe('the home page, single-user mode', () => {
  it('shows every block, in as few requests as five', async () => {
    m = await mount(routes, {
      entries: (server) => {
        for (let i = 0; i < 7; i++) sender(server)('POST', '/pages', { folder: 'Inbox', body: `收件${i}\n` })
        return ['/s/default']
      },
      server: { mode: 'single' },
    })
    await ready()
    await settle(() => section(th.favorites.title)?.textContent?.includes(th.favorites.empty) === true && section(th.inbox.title)?.querySelector('.animate-pulse') === null, 'the blocks')
    expect(section(th.quick.title)?.querySelector('textarea')).not.toBeNull()
    const recent = section(th.cont.title)!
    expect(recent.querySelectorAll('li').length).toBe(8)
    expect(recent.querySelector('a[href^="/s/default/p/"]')).not.toBeNull()
    // The inbox: the count from the tree, and its first pages.
    expect(section(th.inbox.title)!.textContent).toContain(th.inbox.count(7))
    expect(section(th.inbox.title)!.querySelectorAll('li')).toHaveLength(5)
    // No text of the day: nothing of it is drawn.
    expect(daily()).toBeNull()
    const asked = spaceRequests()
    expect(asked.length).toBeLessThanOrEqual(5)
    expect(asked).toEqual(expect.arrayContaining(['GET /pages?folder=&recursive=true&limit=8', 'GET /favorites', 'GET /tree']))
    expect(asked.filter((r) => r.includes('/daily?'))).toHaveLength(1)
    expect(asked.filter((r) => r.startsWith('GET /pages?'))).toHaveLength(2)
  })

  it('asks for no more of a big space: the number of requests is the same', async () => {
    m = await mount(routes, { entries: ['/s/default'], server: { mode: 'single' } })
    const send = sender(m.server)
    for (let i = 0; i < 40; i++) send('POST', '/pages', { folder: '', body: `n${i}\n` })
    await m.unmount()
    m = await mount(routes, { entries: ['/s/default'], server: { mode: 'single' } })
    await ready()
    expect(spaceRequests().length).toBeLessThanOrEqual(5)
  })

  it('the quick note goes to the inbox, empties the box and reads the inbox again', async () => {
    m = await mount(routes, { entries: ['/s/default'], server: { mode: 'single' } })
    await ready()
    await settle(() => section(th.quick.title)?.querySelector('textarea') != null, 'the box')
    const box = section(th.quick.title)!.querySelector('textarea')!
    const before = m.requests.filter((r) => r.url.includes('/pages?folder=Inbox')).length
    await type(box, '路上想到的一句')
    await click([...section(th.quick.title)!.querySelectorAll('button')].find((b) => b.textContent === th.quick.save))
    await settle(() => m!.requests.some((r) => r.method === 'POST' && r.url === '/api/spaces/default/pages'), 'the note is sent')
    const post = m.requests.find((r) => r.method === 'POST' && r.url === '/api/spaces/default/pages')!
    expect(JSON.parse(post.body!)).toEqual({ folder: 'Inbox', body: '路上想到的一句\n' })
    await settle(() => box.value === '', 'the box is empty')
    await settle(() => section(th.inbox.title)?.textContent?.includes('路上想到的一句') === true, 'the inbox shows it')
    expect(m.requests.filter((r) => r.url.includes('/pages?folder=Inbox')).length).toBeGreaterThan(before)
    expect(localStorage.getItem('pb.capture.local.default')).toBeNull()
  })

  it('opens a page from the list and has links to the whole list and the inbox', async () => {
    m = await mount(routes, { entries: ['/s/default'], server: { mode: 'single' } })
    await ready()
    await settle(() => section(th.cont.title)?.querySelector('li a') != null, 'the list')
    expect(section(th.cont.title)!.querySelector('a[href="/s/default/recent"]')).not.toBeNull()
    expect(section(th.inbox.title)!.querySelector('a[href="/s/default/f/Inbox"]')).not.toBeNull()
    await click(section(th.cont.title)!.querySelector('li a'))
    expect(locationText()).toMatch(/^\/s\/default\/p\//)
  })
})

describe('the text of the day', () => {
  it('a page of the space: its text and head, a link that opens it', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server))
        return ['/s/default']
      },
      server: { mode: 'single' },
    })
    await ready()
    await settle(() => daily() !== null, 'the text')
    expect(daily()!.textContent).toMatch(/第 \d 条文案/)
    expect(daily()!.textContent).toMatch(/《暮雪\d》｜副\d/)
    const link = daily()!.querySelector<HTMLAnchorElement>('a')!
    expect(link.getAttribute('href')).toMatch(/^\/s\/default\/p\//)
    const asked = m.requests.filter((r) => r.url.includes('/daily?'))
    expect(asked).toHaveLength(1)
    // Where the browser is: its day, language and offset travel with the request.
    expect(asked[0].url).toMatch(/date=\d{4}-\d{2}-\d{2}/)
    expect(asked[0].url).toContain('lang=zh-CN')
    expect(asked[0].url).toMatch(/tz=-?\d+/)
    await click(link)
    expect(locationText()).toMatch(/^\/s\/default\/p\//)
  })

  it('"another" asks for the next one with skip, and shows it only when it came', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server))
        return ['/s/default']
      },
      server: { mode: 'single' },
    })
    await ready()
    await settle(() => daily() !== null && another() !== null, 'the text and the button')
    const first = daily()!.textContent
    await click(another())
    await settle(() => m!.requests.some((r) => r.url.includes('skip=1')), 'skip=1 is asked')
    await settle(() => daily()!.textContent !== first, 'another text')
    // The page of the day is unchanged for everybody else: only this browser moved on.
    expect(m.requests.filter((r) => r.method !== 'GET' && !r.url.includes('/pages'))).toEqual([])
  })

  it('a single page has nothing to change to', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server), 1)
        return ['/s/default']
      },
      server: { mode: 'single' },
    })
    await ready()
    await settle(() => daily() !== null, 'the text')
    expect(another()).toBeNull()
  })

  it('a source that meets nothing says so to a manager, and opens the settings', async () => {
    m = await mount(routes, {
      entries: (server) => {
        const send = sender(server)
        send('POST', '/folders', { path: '空的' })
        send('PUT', '/daily/settings', { daily: { folder: '空的' } })
        return ['/s/default']
      },
      server: { mode: 'single' },
    })
    await ready()
    await settle(() => document.querySelector('[data-slot="daily-empty"]') !== null, 'the hint')
    expect(daily()).toBeNull()
    await click(document.querySelector('[data-slot="daily-empty"] button'))
    expect(locationText()).toBe('/settings/home')
  })

  describe('in accounts mode', () => {
    const open = (scene: (admin: ReturnType<typeof adminRequests>, space: string) => void, options: { entries?: string } = {}) =>
      mount(routes, {
        signIn: true,
        server: { mode: 'accounts' },
        entries: (server) => {
          const admin = adminRequests(server)
          const space = (admin('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
          scene(admin, space)
          localStorage.setItem('pb.space', space)
          return [`/s/${space}${options.entries ?? ''}`]
        },
      })

    it('a line the administrators pasted: no page, no link', async () => {
      m = await open((admin) => {
        admin('PUT', '/api/admin/daily', { default: { kind: 'list' } })
        admin('PUT', '/api/admin/daily/lines', { lang: '', text: '山色空蒙雨亦奇\t苏轼', format: 'lines' })
      })
      await ready()
      await settle(() => daily() !== null, 'the text')
      expect(daily()!.textContent).toContain('山色空蒙雨亦奇')
      expect(daily()!.textContent).toContain('苏轼')
      expect(daily()!.querySelector('a')).toBeNull()
    })

    const withApi = (id: string) => (admin: ReturnType<typeof adminRequests>, space: string) => {
      admin('PUT', `/api/admin/daily/apis/${id}`, { id, name: '一言', lang: 'zh', url: 'https://example.org/q', text: 'hitokoto', source: 'from', author: 'from_who', link: 'https://hitokoto.cn?uuid={uuid}', refresh: 'daily', rps: 5, enabled: true })
      admin('PUT', `/api/spaces/${space}/daily/prefs`, { inherit: false, apis: [id] })
    }

    it('an external text with who said it and a link back, drawn as a web link', async () => {
      m = await open(withApi('hitokoto'))
      await ready()
      await settle(() => daily() !== null, 'the text')
      expect(daily()!.textContent).toContain('一言')
      expect(daily()!.textContent).toContain('au')
      const a = daily()!.querySelector<HTMLAnchorElement>('a')!
      expect(a.href).toBe('https://hitokoto.cn/?uuid=u1')
      expect(a.rel).toContain('noopener')
      expect(a.target).toBe('_blank')
      expect(daily()!.querySelector('[data-slot="daily-stale"]')).toBeNull()
    })

    it('a text that is the last one it said is marked, with the day', async () => {
      m = await open(withApi('stale-one'))
      await ready()
      await settle(() => daily()?.querySelector('[data-slot="daily-stale"]') != null, 'the marker')
      expect(daily()!.querySelector('[data-slot="daily-stale"]')!.textContent).toContain('暂时没有回应')
    })

    it('another text from an interface is asked for with skip', async () => {
      m = await open(withApi('hitokoto'))
      await ready()
      await settle(() => another() !== null, 'the button')
      await click(another())
      await settle(() => daily()!.textContent!.includes('#1'), 'the next text')
    })

    it('nothing at all: the block is not there, and the greeting still is', async () => {
      m = await open(() => undefined)
      await ready()
      await settle(() => m!.requests.some((r) => r.url.includes('/daily?')), 'asked')
      await settle(() => document.querySelector('[data-slot="daily"]') === null && document.querySelector('.animate-pulse') === null, 'settled')
      expect(daily()).toBeNull()
      expect(greeting()).not.toBe('')
    })

    it('someone who may only read has no note box, and is not told what to set up', async () => {
      m = await mount(routes, {
        signIn: true,
        server: { mode: 'accounts' },
        entries: (server) => {
          const { space, owner } = shareSpace(server, 'viewer')
          owner.request('POST', `/api/spaces/${space}/folders`, { path: '空的' })
          owner.request('PUT', `/api/spaces/${space}/daily/settings`, { daily: { folder: '空的' } })
          localStorage.setItem('pb.space', space)
          return [`/s/${space}`]
        },
      })
      await ready()
      await settle(() => section(th.cont.title) !== null, 'the page')
      expect(section(th.quick.title)).toBeNull()
      expect(document.querySelector('textarea')).toBeNull()
      expect(document.querySelector('[data-slot="daily-empty"]')).toBeNull()
      // The rest is the same: continue writing, the inbox, the favourites.
      expect(section(th.inbox.title)).not.toBeNull()
      expect(section(th.favorites.title)).not.toBeNull()
    })
  })
})

describe('the greeting', () => {
  const fixed = (y: number, mo: number, d: number, h: number) => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(y, mo - 1, d, h, 0))
  }
  const opens = () => mount(routes, { entries: ['/s/default'], server: { mode: 'single', empty: true } })

  it('a festival of the table is greeted by name of the day', async () => {
    fixed(2026, 2, 17, 9)
    m = await opens()
    await ready()
    expect(greeting()).toMatch(/春节|过年/)
  })

  it('says the part of the day, and is the same each time the page is opened that day', async () => {
    fixed(2026, 11, 25, 23)
    m = await opens()
    await ready()
    const first = greeting()
    expect(first).toMatch(/夜|深夜/)
    await m.unmount()
    fixed(2026, 11, 25, 23)
    m = await opens()
    await ready()
    expect(greeting()).toBe(first)
  })

  it('a long absence is noticed on the first visit of the day, and the first visit is remembered for the day', async () => {
    localStorage.setItem('pb.home.visit.local.default', JSON.stringify({ date: '2026-10-01', first: true, absentDays: 0 }))
    fixed(2026, 11, 25, 15)
    m = await opens()
    await ready()
    expect(greeting()).toMatch(/55 天|好久不见|你回来啦/)
    expect(JSON.parse(localStorage.getItem('pb.home.visit.local.default')!)).toEqual({ date: '2026-11-25', first: true, absentDays: 55 })
    const first = greeting()
    await m.unmount()
    fixed(2026, 11, 25, 16)
    m = await opens()
    await ready()
    expect(greeting()).toBe(first)
  })

  it('having just written a page', async () => {
    fixed(2026, 11, 25, 15)
    m = await opens()
    await m.unmount()
    // The mock's clock is the fixed one: the page was written just now.
    m = await mount(routes, { entries: (s) => { sender(s)('POST', '/pages', { folder: '', body: '刚写的\n' }); return ['/s/default'] }, server: { mode: 'single', empty: true } })
    await ready()
    expect(greeting()).toMatch(/刚写完|写得不错/)
  })

  it('the plain greeting when variety is off', async () => {
    localStorage.setItem('pb.home.variety', 'off')
    fixed(2026, 2, 17, 9)
    m = await opens()
    await ready()
    expect(greeting()).toBe('早上好')
  })

  it('names the person in accounts mode', async () => {
    fixed(2026, 11, 25, 9)
    localStorage.setItem('pb.home.variety', 'off')
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const space = ((adminRequests(server)('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0]).id
        localStorage.setItem('pb.space', space)
        return [`/s/${space}`]
      },
    })
    await ready()
    expect(greeting()).toBe('早上好，管理员')
  })
})
