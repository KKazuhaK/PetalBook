// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { shareSpace } from '@/api/fixtures/data/scenes'
import type { FixtureServer } from '@/api/fixtures/server'
import type { DailyAnswer } from '@/api/types'
import { SidebarProvider } from '@/components/ui/sidebar'
import { th } from '@/i18n/home'
import { click, installDomShims, locationText, mount, settle, type, type FxMounted } from '@/testing/harness'
import { sender, type Send } from '@/testing/listKit'
import { noteWrote, wroteKey } from '@/lib/homePrefs'
import { tzOffset } from '@/lib/templates'
import HomePage from './HomePage'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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
const section = (title: string) =>
  [...document.querySelectorAll('section')].find((s) => s.querySelector('h2')?.textContent === title) ?? null
const spaceRequests = () =>
  m!.requests
    .filter((r) => r.url.startsWith('/api/spaces/'))
    .map((r) => `${r.method} ${r.url.replace(/^\/api\/spaces\/[^/]+/, '')}`)
const ready = () =>
  settle(
    () => home() !== null && greeting() !== '' && document.querySelector('[aria-busy="true"]') === null,
    'the home page',
  )

// A folder of texts, pages in it, and the space's daily text coming from it.
function seedTexts(send: Send, count = 3) {
  send('POST', '/folders', { path: '文案' })
  const ids = Array.from(
    { length: count },
    (_, i) =>
      (
        send('POST', '/pages', {
          folder: '文案',
          meta: { title: `暮雪${i}`, subtitle: `副${i}` },
          body: `第 ${i} 条文案\n`,
        }).body as { id: string }
      ).id,
  )
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
      fixtures: { mode: 'single' },
    })
    await ready()
    await settle(
      () =>
        section(th.favorites.title)?.textContent?.includes(th.favorites.empty) === true &&
        section(th.inbox.title)?.querySelector('.animate-pulse') === null,
      'the blocks',
    )
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
    expect(asked).toEqual(
      expect.arrayContaining(['GET /pages?folder=&recursive=true&limit=8', 'GET /favorites', 'GET /tree']),
    )
    expect(asked.filter((r) => r.includes('/daily?'))).toHaveLength(1)
    expect(asked.filter((r) => r.startsWith('GET /pages?'))).toHaveLength(2)
  })

  it('asks for no more of a big space: the number of requests is the same', async () => {
    m = await mount(routes, { entries: ['/s/default'], fixtures: { mode: 'single' } })
    const send = sender(m.fx)
    for (let i = 0; i < 40; i++) send('POST', '/pages', { folder: '', body: `n${i}\n` })
    await m.unmount()
    m = await mount(routes, { entries: ['/s/default'], fixtures: { mode: 'single' } })
    await ready()
    expect(spaceRequests().length).toBeLessThanOrEqual(5)
  })

  it('the quick note goes to the inbox, empties the box and reads the inbox again', async () => {
    m = await mount(routes, { entries: ['/s/default'], fixtures: { mode: 'single' } })
    await ready()
    await settle(() => section(th.quick.title)?.querySelector('textarea') != null, 'the box')
    const box = section(th.quick.title)!.querySelector('textarea')!
    const before = m.requests.filter((r) => r.url.includes('/pages?folder=Inbox')).length
    await type(box, '路上想到的一句')
    await click([...section(th.quick.title)!.querySelectorAll('button')].find((b) => b.textContent === th.quick.save))
    await settle(
      () => m!.requests.some((r) => r.method === 'POST' && r.url === '/api/spaces/default/pages'),
      'the note is sent',
    )
    const post = m.requests.find((r) => r.method === 'POST' && r.url === '/api/spaces/default/pages')!
    expect(JSON.parse(post.body!)).toEqual({ folder: 'Inbox', body: '路上想到的一句\n', tz: tzOffset() })
    await settle(() => box.value === '', 'the box is empty')
    await settle(() => section(th.inbox.title)?.textContent?.includes('路上想到的一句') === true, 'the inbox shows it')
    expect(m.requests.filter((r) => r.url.includes('/pages?folder=Inbox')).length).toBeGreaterThan(before)
    expect(localStorage.getItem('pb.capture.local.default')).toBeNull()
  })

  it('opens a page from the list and has links to the whole list and the inbox', async () => {
    m = await mount(routes, { entries: ['/s/default'], fixtures: { mode: 'single' } })
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
      fixtures: { mode: 'single' },
    })
    await ready()
    await settle(() => daily() !== null, 'the text')
    expect(daily()!.textContent).toMatch(/第 \d 条文案/)
    expect(daily()!.textContent).toMatch(/第 \d 条文案.*—— 《暮雪\d｜副\d》/)
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

  it('"another" keeps its size for a mouse and has a 40 px area to hit on a phone or a touch screen', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server))
        return ['/s/default']
      },
      fixtures: { mode: 'single' },
    })
    await ready()
    await settle(() => another() !== null, 'the button')
    // h-6 (24 px) with a transparent box of 8 px more on every side where the screen is small or the pointer coarse.
    expect(another()!.className).toContain('h-6')
    expect(another()!.className).toContain('max-md:before:-inset-2')
    expect(another()!.className).toContain('pointer-coarse:before:-inset-2')
  })

  it('"another" asks for the next one with skip, and shows it only when it came', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server))
        return ['/s/default']
      },
      fixtures: { mode: 'single' },
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

  it('skip_too_large turns "another" off with a short note, and keeps the text on screen', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server))
        return ['/s/default']
      },
      fixtures: { mode: 'single' },
    })
    await ready()
    await settle(() => daily() !== null && another() !== null, 'the text and the button')
    const first = daily()!.textContent
    // The server will not go further than the pages it has.
    m.fx.fail('GET', '/api/spaces/default/daily', 400, { code: 'skip_too_large', message: 'skip is too large' })
    await click(another())
    await settle(() => document.querySelector('[data-slot="daily-skip-limit"]') !== null, 'the note')
    expect(document.querySelector('[data-slot="daily-skip-limit"]')!.textContent).toBe(th.daily.skipLimit)
    expect(another()!.disabled).toBe(true)
    expect(daily()!.textContent).toContain(first!.split(th.daily.another)[0])
    expect(document.body.textContent).not.toContain(th.daily.anotherFailed)
  })

  it('a single page has nothing to change to', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seedTexts(sender(server), 1)
        return ['/s/default']
      },
      fixtures: { mode: 'single' },
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
      fixtures: { mode: 'single' },
    })
    await ready()
    await settle(() => document.querySelector('[data-slot="daily-empty"]') !== null, 'the hint')
    expect(daily()).toBeNull()
    await click(document.querySelector('[data-slot="daily-empty"] button'))
    expect(locationText()).toBe('/settings/home')
  })

  describe('in accounts mode', () => {
    const open = (scene: (fx: FixtureServer, space: string) => void, options: { entries?: string } = {}) =>
      mount(routes, {
        signIn: true,
        fixtures: { mode: 'accounts' },
        entries: (fx) => {
          const space = fx.data.spaces[0].info.id
          scene(fx, space)
          localStorage.setItem('pb.space', space)
          return [`/s/${space}${options.entries ?? ''}`]
        },
      })

    // What the space's text of the day is, said exactly (which source it comes from is the server's to decide).
    const says = (fx: FixtureServer, answer: DailyAnswer) => fx.reply('GET', '/api/spaces/:space/daily', answer)

    it('a line the administrators pasted: no page, no link', async () => {
      m = await open((fx) =>
        says(fx, { page: null, origin: 'site', quote: { text: '山色空蒙雨亦奇', author: '苏轼' } }),
      )
      await ready()
      await settle(() => daily() !== null, 'the text')
      expect(daily()!.textContent).toContain('山色空蒙雨亦奇')
      expect(daily()!.textContent).toContain('苏轼')
      expect(daily()!.querySelector('a')).toBeNull()
    })

    // An interface's text, with who said it and a link back to its service.
    const external = (text: string, over: Partial<NonNullable<DailyAnswer['quote']>> = {}): DailyAnswer => ({
      page: null,
      origin: 'external',
      count: 5,
      quote: { text, source: 'src', author: 'au', link: 'https://hitokoto.cn?uuid=1', ...over },
    })

    it('an external text with who said it, and never a link to somewhere else', async () => {
      m = await open((fx) => says(fx, external('一言')))
      await ready()
      await settle(() => daily() !== null, 'the text')
      expect(daily()!.textContent).toContain('一言')
      expect(daily()!.textContent).toContain('—— au《src》')
      // The service sends a link back; it is kept and not drawn: nothing here leads off the site.
      expect(daily()!.querySelector('a')).toBeNull()
      expect(daily()!.querySelector('[data-slot="daily-link"]')).toBeNull()
      expect(daily()!.innerHTML).not.toContain('hitokoto.cn')
      expect(daily()!.querySelector('[data-slot="daily-stale"]')).toBeNull()
    })

    it('a text that is the last one it said is marked, with the day', async () => {
      m = await open((fx) => says(fx, external('旧的一言', { stale: true, fetched: '2026-11-20T08:00:00Z' })))
      await ready()
      await settle(() => daily()?.querySelector('[data-slot="daily-stale"]') != null, 'the marker')
      expect(daily()!.querySelector('[data-slot="daily-stale"]')!.textContent).toContain('暂时没有回应')
    })

    it('another text from an interface is asked for with skip', async () => {
      m = await open((fx) => says(fx, external('一言')))
      await ready()
      await settle(() => another() !== null, 'the button')
      says(m.fx, external('一言 #1'))
      await click(another())
      await settle(() => m!.requests.some((r) => r.url.includes('skip=1')), 'skip=1 is asked')
      await settle(() => daily()!.textContent!.includes('#1'), 'the next text')
    })

    it('nothing at all: the block is not there, and the greeting still is', async () => {
      m = await open(() => undefined)
      await ready()
      await settle(() => m!.requests.some((r) => r.url.includes('/daily?')), 'asked')
      await settle(
        () =>
          document.querySelector('[data-slot="daily"]') === null && document.querySelector('.animate-pulse') === null,
        'settled',
      )
      expect(daily()).toBeNull()
      expect(greeting()).not.toBe('')
    })

    it('someone who may only read has no note box, and is not told what to set up', async () => {
      m = await mount(routes, {
        signIn: true,
        fixtures: { mode: 'accounts' },
        entries: (fx) => {
          const { space, owner } = shareSpace(fx, 'viewer')
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
  const opens = () => mount(routes, { entries: ['/s/default'], fixtures: { mode: 'single', empty: true } })

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
    localStorage.setItem(
      'pb.home.visit.local.default',
      JSON.stringify({ date: '2026-10-01', first: true, absentDays: 0 }),
    )
    fixed(2026, 11, 25, 15)
    m = await opens()
    await ready()
    expect(greeting()).toMatch(/55 天|好久不见|你回来啦/)
    expect(JSON.parse(localStorage.getItem('pb.home.visit.local.default')!)).toMatchObject({
      date: '2026-11-25',
      first: true,
      absentDays: 55,
      seen: true,
    })
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
    // This browser saved a page a few minutes ago.
    noteWrote(wroteKey(undefined, 'default'), 'p1', new Date(Date.now() - 5 * 60_000))
    m = await opens()
    await ready()
    expect(greeting()).toMatch(/刚写完|写得不错/)
  })

  it("does not take another person's save for the person's own", async () => {
    fixed(2026, 11, 25, 15)
    // Pages written just now by someone else in the space; this browser wrote nothing.
    m = await mount(routes, {
      entries: (s) => {
        for (let i = 0; i < 4; i++) sender(s)('POST', '/pages', { folder: '', body: `别人写的${i}\n` })
        return ['/s/default']
      },
      fixtures: { mode: 'single', empty: true },
    })
    await ready()
    expect(greeting()).not.toMatch(/刚写完|写得不错|今天写了/)
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
      fixtures: { mode: 'accounts' },
      entries: (fx) => {
        const space = fx.data.spaces[0].info.id
        localStorage.setItem('pb.space', space)
        return [`/s/${space}`]
      },
    })
    await ready()
    expect(greeting()).toBe('早上好，管理员')
  })
})
