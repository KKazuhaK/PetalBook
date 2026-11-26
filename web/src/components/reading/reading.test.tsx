// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { shareSpace } from '@/api/fixtures/data/scenes'
import type { FixtureServer } from '@/api/fixtures/server'
import { byLabel, click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { listShell, putViews, sender, TASK, type Send } from '@/testing/listKit'
import { t } from '@/i18n'
import { tb } from '@/i18n/boards'

// Continuous reading against the mock server. The drawing of diagrams is not run here (it needs a real browser).
const renderDiagram = vi.fn<(text: string, dark: boolean) => Promise<string>>()
vi.mock('@/components/editor/code/mermaidRender', () => ({
  renderDiagram: (text: string, dark: boolean) => renderDiagram(text, dark),
}))

beforeAll(installDomShims)

// --- a stand-in for the browser's IntersectionObserver: the test says what is near the window ---
type Entry = { target: Element; isIntersecting: boolean }
class FakeObserver {
  static live = new Set<FakeObserver>()
  els = new Set<Element>()
  cb: (entries: Entry[]) => void
  constructor(cb: (entries: Entry[]) => void) {
    this.cb = cb
    FakeObserver.live.add(this)
  }
  observe(el: Element) {
    this.els.add(el)
    this.cb([{ target: el, isIntersecting: isNear(el) }])
  }
  unobserve(el: Element) {
    this.els.delete(el)
  }
  disconnect() {
    this.els.clear()
    FakeObserver.live.delete(this)
  }
  takeRecords() {
    return []
  }
}
// The pages from `from` to `to` (by their place in the list) are near the window; the end of the list is when `to` reaches it.
let view = { from: 0, to: 2 }
function isNear(el: Element): boolean {
  const all = [...document.querySelectorAll('[data-reading-id]')]
  const at = all.indexOf(el)
  if (at >= 0) return at >= view.from && at <= view.to
  // The end of the list.
  return view.to >= all.length - 1
}
const look = (from: number, to: number) =>
  act(async () => {
    view = { from, to }
    for (const o of FakeObserver.live) for (const el of o.els) o.cb([{ target: el, isIntersecting: isNear(el) }])
  })

let m: FxMounted | undefined
beforeEach(() => {
  localStorage.clear()
  view = { from: 0, to: 2 }
  vi.stubGlobal('IntersectionObserver', FakeObserver)
  renderDiagram.mockReset()
  renderDiagram.mockImplementation(async (text) => `data:image/svg+xml,${encodeURIComponent(`<svg>${text}</svg>`)}`)
})
afterEach(async () => {
  await m?.unmount()
  m = undefined
  FakeObserver.live.clear()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const articles = () => [...document.querySelectorAll<HTMLElement>('[data-reading-id]')]
const drawn = () => articles().filter((a) => a.getAttribute('data-drawn') === 'true')
const bodyRequests = () =>
  m!.requests
    .filter((r) => r.method === 'GET' && /\/pages\?/.test(r.url) && r.url.includes('body=1'))
    .map((r) => new URL(r.url, 'http://x').searchParams)
const textButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label) ?? null

async function openReading(scene: (send: Send, server: FixtureServer) => void, path = '/s/default/f/文案') {
  m = await mount(listShell, {
    fixtures: true,
    entries: (server) => {
      const send = sender(server)
      send('POST', '/folders', { path: '文案' })
      scene(send, server)
      putViews(send, path.includes('/t/') ? { tag: decodeURIComponent(path.split('/t/')[1]) } : { folder: '文案' }, [
        { name: '连读', layout: 'read' },
      ])
      return [path]
    },
  })
  await settle(() => articles().length > 0, 'the pages')
}
const page = (send: Send, meta: Record<string, unknown>, body: string, folder = '文案') =>
  send('POST', '/pages', { folder, meta, body }).body as { id: string }

describe('reading a folder', () => {
  it('shows the pages one after the other with their texts, in the order of the listing, with no editor mounted', async () => {
    await openReading((send) => {
      page(
        send,
        { title: '暮雪', subtitle: '下一个路口' },
        '第一段。\n\n第二段，有**粗体**和[链接](https://example.com/x)。\n',
      )
      page(send, { title: '早安' }, '# 小标题\n\n- 一\n- 二\n')
    })
    await settle(() => drawn().length === 2, 'both drawn')
    expect(
      articles()
        .map((a) => a.querySelector('h2')?.textContent)
        .sort(),
    ).toEqual(['《早安》', '《暮雪》｜下一个路口'])
    const byTitle = (title: string) => articles().find((a) => a.querySelector('h2')?.textContent === title)!
    const first = byTitle('《暮雪》｜下一个路口')
    expect(first.querySelectorAll('p').length).toBe(2)
    expect(first.querySelector('strong')!.textContent).toBe('粗体')
    const link = first.querySelector<HTMLAnchorElement>('.pb-prose a')!
    expect([link.getAttribute('href'), link.getAttribute('rel')]).toEqual([
      'https://example.com/x',
      'noopener noreferrer',
    ])
    expect(byTitle('《早安》').querySelector('h1')!.textContent).toBe('小标题')
    expect(byTitle('《早安》').querySelectorAll('li')).toHaveLength(2)
    // A static layout: no editor, no editable text.
    expect(document.querySelector('.ProseMirror, [contenteditable="true"]')).toBeNull()
    expect(first.querySelector('.pb-prose')).not.toBeNull()
    // The request asks for bodies, twenty at the most.
    expect(bodyRequests()[0].get('limit')).toBe('20')
    expect(bodyRequests()[0].get('folder')).toBe('文案')
  })

  it('opens the editor from a title; an untitled page is only its text, with a small way in', async () => {
    let id = ''
    await openReading((send) => {
      id = page(send, { title: '暮雪' }, '正文\n').id
      page(send, {}, '没有标题的一页\n')
    })
    await settle(() => drawn().length === 2, 'both drawn')
    const titled = articles().find((a) => a.querySelector('h2'))!
    const link = titled.querySelector('h2 a')!
    expect(link.getAttribute('href')).toBe(`/s/default/p/${id}`)
    expect(link.getAttribute('aria-label')).toBe(tb.reading.edit('暮雪'))
    const untitled = articles().find((a) => !a.querySelector('h2'))!
    expect(untitled.textContent).toContain('没有标题的一页')
    expect(untitled.querySelector('h2')).toBeNull()
    expect(untitled.querySelector('a')!.getAttribute('href')).toMatch(/\/p\//)
    expect(untitled.textContent).not.toContain('《')
    await click(link)
    await settle(
      () => document.querySelector('[data-testid="location"]')!.textContent!.startsWith(`/s/default/p/${id}`),
      'at the editor',
    )
  })

  it('looks like the editor: lists with tasks, a quote, a table with alignment, an image from the space, code with its language and colours', async () => {
    await openReading((send) => {
      page(
        send,
        { title: '全' },
        [
          '> 引用',
          '',
          '- [x] 做完',
          '- [ ] 待做',
          '',
          '| 甲 | 乙 |',
          '| :-: | --: |',
          '| 1 | 2 |',
          '',
          '![雨街](/assets/p1/street.jpg)',
          '',
          '```js',
          'const a = 1',
          '```',
          '',
          '行内`代码`与---',
          '',
          '---',
          '',
          '<div>原样</div>',
          '',
          '脚注[^1]',
          '',
          '[^1]: 注释',
          '',
        ].join('\n'),
      )
    })
    await settle(() => drawn().length === 1, 'drawn')
    const a = drawn()[0]
    expect(a.querySelector('blockquote')!.textContent).toBe('引用')
    const tasks = [...a.querySelectorAll<HTMLInputElement>('ul[data-type="taskList"] input')]
    expect(tasks.map((c) => [c.checked, c.disabled])).toEqual([
      [true, true],
      [false, true],
    ])
    expect(a.querySelector('li[data-checked="true"]')).not.toBeNull()
    const cells = [...a.querySelectorAll<HTMLElement>('.tableWrapper th, .tableWrapper td')]
    expect(cells.map((c) => c.textContent)).toEqual(['甲', '乙', '1', '2'])
    expect(cells.map((c) => c.style.textAlign)).toEqual(['center', 'right', 'center', 'right'])
    expect(a.querySelectorAll('th')).toHaveLength(2)
    expect(a.querySelector('img')!.getAttribute('src')).toBe('/api/spaces/default/files/assets/p1/street.jpg')
    const code = a.querySelector('.pb-code')!
    expect(code.getAttribute('data-language')).toBe('JavaScript')
    expect(code.querySelector('pre code')!.textContent).toBe('const a = 1')
    // The colours arrive with the highlighter.
    await settle(() => code.querySelector('.hljs-keyword') !== null, 'colours', 400)
    expect(a.querySelector('code:not(pre code)')!.textContent).toBe('代码')
    expect(a.querySelector('hr')).not.toBeNull()
    expect(a.querySelector('pre[data-raw-block]')!.textContent).toBe('<div>原样</div>')
    expect(a.querySelector('sup[data-footnote-ref]')!.textContent).toBe('1')
    expect(a.querySelector('aside[data-footnote-def]')!.textContent).toBe('注释')
  })

  it('draws a mermaid block as its picture, and its code when it does not draw', async () => {
    renderDiagram.mockImplementation(async (text) => {
      if (text.includes('bad')) throw new Error('Parse error')
      return `data:image/svg+xml,${encodeURIComponent('<svg></svg>')}`
    })
    await openReading((send) => {
      page(send, { title: '图' }, '```mermaid\ngraph TD\nA-->B\n```\n\n```mermaid\nbad text\n```\n')
    })
    await settle(() => drawn().length === 1 && document.querySelector('.pb-diagram img') !== null, 'the picture')
    const blocks = [...document.querySelectorAll('.pb-code')]
    expect(blocks).toHaveLength(2)
    // The one that drew shows the picture and not the code.
    expect(blocks[0].querySelector('.pb-diagram img')!.getAttribute('alt')).toBe(tb.reading.diagram)
    expect(blocks[0].querySelector('pre')).toBeNull()
    await settle(() => blocks[1].querySelector('.pb-diagram-error') !== null, 'the error')
    expect(blocks[1].querySelector('pre code')!.textContent).toBe('bad text')
    expect(blocks[1].querySelector('.pb-diagram-error')!.textContent).toBe('Parse error')
  })

  it('does not make a link of an address that runs a script, and shows the text', async () => {
    await openReading((send) => {
      page(send, { title: '链' }, '[点我](javascript:alert(1)) 和 [好链接](/f/a.md) 和 <b>x</b>\n')
    })
    await settle(() => drawn().length === 1, 'drawn')
    const links = [...drawn()[0].querySelectorAll<HTMLAnchorElement>('.pb-prose a')]
    expect(links.map((l) => l.textContent)).toEqual(['好链接'])
    expect(drawn()[0].textContent).toContain('点我')
    expect(drawn()[0].querySelector('b')).toBeNull()
  })

  it('says so for an empty page and for an empty folder', async () => {
    await openReading((send) => page(send, { title: '空' }, ''))
    await settle(() => drawn().length === 1, 'drawn')
    expect(drawn()[0].textContent).toContain(tb.reading.emptyBody)
    await m!.unmount()
    m = undefined
    await openReading(() => undefined).catch(() => undefined)
  })
})

describe('a long reading', () => {
  const MANY = 300
  async function openMany() {
    await openReading((send) => {
      for (let i = 0; i < MANY; i++)
        page(send, { title: `第${String(i).padStart(3, '0')}篇` }, `这是第 ${i} 篇的正文。\n\n第二段 ${i}。\n`)
    })
  }
  const loaded = () => articles().length

  it('is read twenty pages at a time, each batch asked for with the cursor when the end of what is loaded comes near', async () => {
    await openMany()
    expect(loaded()).toBe(20)
    expect(bodyRequests()).toHaveLength(1)
    expect(document.body.textContent).toContain(tb.reading.loaded(20))
    // Near the first pages only: nothing more is asked for.
    await new Promise((r) => setTimeout(r, 50))
    expect(bodyRequests()).toHaveLength(1)
    // The window reaches the end of the 20: the next twenty.
    await look(15, 19)
    await settle(() => loaded() === 40, 'forty')
    const qs = bodyRequests()
    expect(qs).toHaveLength(2)
    expect(qs[1].get('cursor')).toBeTruthy()
    expect(qs.every((q) => q.get('limit') === '20')).toBe(true)
    // The button asks as well, for a browser that cannot say what is near.
    await click(textButton(tb.reading.loadMore))
    await settle(() => loaded() === 60, 'sixty')
    expect(bodyRequests()).toHaveLength(3)
  })

  it('keeps the page small: only the pages near the window are drawn, whatever number have been read', async () => {
    await openMany()
    // Read all of it, a batch at a time, as a person scrolling to the end would.
    for (let n = 20; n < MANY; n += 20) {
      await look(loaded() - 3, loaded() - 1)
      await settle(() => loaded() === n + 20, `${n + 20} loaded`)
    }
    expect(loaded()).toBe(MANY)
    await look(MANY - 3, MANY - 1)
    expect(document.body.textContent).toContain(tb.reading.allLoaded(MANY))
    expect(bodyRequests()).toHaveLength(MANY / 20)
    // The window is at the end now: a handful of pages are in the page, the rest are blanks.
    await settle(() => drawn().length <= 3, 'few drawn at the end')
    const ends = drawn().map((a) => articles().indexOf(a))
    expect(ends.every((i) => i >= MANY - 3)).toBe(true)
    const all = document.querySelector('[data-slot="reading-view"]')!.querySelectorAll('*').length
    // A blank is one element; hundreds of drawn pages would be many thousands.
    expect(all).toBeLessThan(MANY + 150)
    expect(document.querySelectorAll('[data-slot="static-body"]').length).toBeLessThanOrEqual(3)
    // Scrolling back up draws the pages that come near and lets go of the ones that left.
    await look(100, 104)
    await settle(() => drawn().length === 5 && articles().indexOf(drawn()[0]) === 100, 'a different window')
    expect(drawn()).toEqual(articles().slice(100, 105))
    expect(drawn().every((a) => a.querySelector('h2') !== null)).toBe(true)
    expect(document.querySelectorAll('[data-slot="static-body"]').length).toBe(5)
    // A blank keeps a height (the guess here, as nothing is laid out), so the scroll bar does not jump.
    expect(articles()[0].style.height).toBe('480px')
    expect(articles()[0].getAttribute('aria-hidden')).toBe('true')
    // No more was asked for.
    expect(bodyRequests()).toHaveLength(MANY / 20)
  })
})

describe('reading a tag', () => {
  it('reads the pages under the tag, from any folder, and the layout is offered', async () => {
    await openReading((send) => {
      page(send, { title: '甲', tags: ['玉兰'] }, '甲的正文\n')
      send('POST', '/folders', { path: '别处' })
      page(send, { title: '乙', tags: ['玉兰'] }, '乙的正文\n', '别处')
      page(send, { title: '丙' }, '丙不在\n')
    }, '/s/default/t/玉兰')
    await settle(() => drawn().length === 2, 'two pages')
    expect(
      drawn()
        .map((a) => a.querySelector('h2')!.textContent)
        .sort(),
    ).toEqual(['《乙》', '《甲》'].sort())
    expect(byLabel(t.view.read)!.getAttribute('aria-pressed')).toBe('true')
    expect(bodyRequests()[0].getAll('tag')).toEqual(['玉兰'])
  })
})

describe('reading by the view', () => {
  it('follows the sort and the filters of the view, and someone who may only read can read', async () => {
    m = await mount(listShell, {
      signIn: true,
      fixtures: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        const send = (method: string, path: string, body?: unknown) =>
          owner.request(method, `/api/spaces/${space}${path}`, body)
        send('PUT', '/types/task', TASK)
        send('POST', '/folders', { path: 'Work' })
        send('PUT', '/folders/type', { folder: 'Work', type: 'task' })
        for (const [title, status, points] of [
          ['A', 'done', 3],
          ['B', 'todo', 1],
          ['C', 'todo', 2],
        ] as const)
          send('POST', '/pages', { folder: 'Work', meta: { title, status, points }, body: `${title} 的正文\n` })
        const cur = send('GET', '/views?folder=Work').body as { rev: string }
        send('PUT', '/views', {
          folder: 'Work',
          baseRev: cur.rev,
          views: [
            {
              name: '待办',
              layout: 'read',
              sort: 'prop:points',
              filter: [{ key: 'status', op: 'in', values: ['todo'] }],
            },
          ],
        })
        return [`/s/${space}/f/Work`]
      },
    })
    await settle(() => drawn().length === 2, 'two pages')
    expect(drawn().map((a) => a.querySelector('h2')!.textContent)).toEqual(['《B》', '《C》'])
    const q = bodyRequests()[0]
    expect(q.get('sort')).toBe('prop:points')
    expect(JSON.parse(q.get('filter')!)).toEqual([{ key: 'status', op: 'in', values: ['todo'] }])
    expect(m.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
  })
})
