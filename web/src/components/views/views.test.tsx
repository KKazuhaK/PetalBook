// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, useEffect, type ReactNode } from 'react'
import { useNavigate, type NavigateFunction } from 'react-router'
import { toast } from 'sonner'
import { byLabel, click, installDomShims, locationText, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { listShell, putViews, seedWork, sender, TASK, viewsOf } from '@/testing/listKit'
import type { MockServer } from '@/api/mock/server'
import { keys, viewsTiming } from '@/api/hooks'
import { viewIdKey } from '@/lib/savedViews'
import { viewKey } from '@/lib/listView'
import { t } from '@/i18n'
import { tb as tbWords } from '@/i18n/boards'
import { tp as tpWords } from '@/i18n/props'

// The tabs of saved views above the listing of a folder or a tag, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const FOLDER = { folder: 'Work' }
const tabs = () => [...document.querySelectorAll<HTMLElement>('[role="tab"]')]
const tabNames = () => tabs().map((e) => e.firstChild?.textContent ?? '')
const selected = () => tabs().find((e) => e.getAttribute('aria-selected') === 'true')?.firstChild?.textContent
const tab = (name: string) => tabs().find((e) => e.firstChild?.textContent === name)!
const modifiedMark = () => document.querySelector('[data-slot="modified"]')
const puts = () => m!.requests.filter((r) => r.method === 'PUT' && r.url.endsWith('/views'))
const listUrls = () => m!.requests.filter((r) => r.method === 'GET' && /\/pages\?/.test(r.url)).map((r) => decodeURIComponent(r.url))
const rowTitles = () => [...document.querySelectorAll('[data-file-key^="p:"]')].map((e) => e.textContent ?? '')
const button = (label: string) => byLabel(label) as HTMLButtonElement | null
const textButton = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === label) ?? null

async function open(path = '/s/default/f/Work', scene?: (send: ReturnType<typeof sender>, ids: ReturnType<typeof seedWork>, server: MockServer) => void, options: Parameters<typeof mount>[1] = {}) {
  m = await mount(withNav(listShell), {
    ...options,
    entries: (server) => {
      const send = sender(server)
      const ids = seedWork(server)
      scene?.(send, ids, server)
      return [path]
    },
  })
  await settle(() => byLabel(t.view.list) !== null, 'the listing')
  return m
}

const saved = () => viewsOf(sender(m!.server), 'folder=Work')

// A way to move within the app, as a link or "back" would.
let nav: NavigateFunction | null = null
function NavGrab() {
  const navigate = useNavigate()
  useEffect(() => {
    nav = navigate
  }, [navigate])
  return null
}
const withNav = (ui: ReactNode) => (
  <>
    {ui}
    <NavGrab />
  </>
)

describe('a folder with no saved view', () => {
  it('is as it was: no tabs, only a way to make the first view; the list or cards choice of 2.1 is its layout', async () => {
    localStorage.setItem(viewKey('default', 'folder', 'Work'), 'cards')
    await open()
    await settle(() => button(t.views.add) !== null, 'the add button')
    expect(tabs()).toEqual([])
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    // Changing the layout is remembered in the browser as before, and is not a change to save.
    await click(button(t.view.list))
    expect(localStorage.getItem(viewKey('default', 'folder', 'Work'))).toBeNull()
    expect(modifiedMark()).toBeNull()
  })

  it('makes the first view from how things look now, shows it and remembers it', async () => {
    await open()
    await settle(() => button(t.views.add) !== null, 'the add button')
    await click(button(t.view.cards))
    await click(button(t.views.add))
    await settle(() => document.querySelector('input[aria-label="' + t.views.name + '"]') !== null, 'the name dialog')
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '卡片')
    await act(async () => input.form!.requestSubmit())
    await settle(() => tabNames().length === 1, 'the tab')
    expect(selected()).toBe('卡片')
    expect(puts()).toHaveLength(1)
    const body = JSON.parse(puts()[0].body!)
    expect(body).toMatchObject({ folder: 'Work', views: [{ name: '卡片', layout: 'cards', sort: '', group: '', filter: [], hidden: [] }] })
    expect(typeof body.baseRev).toBe('string')
    const [view] = (await saved()).views
    expect(view.id).toMatch(/^[a-z0-9]{6}$/)
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBe(view.id)
    expect(modifiedMark()).toBeNull()
  })

  it('asks for a name, and does not take an empty one', async () => {
    await open()
    await settle(() => button(t.views.add) !== null, 'the add button')
    await click(button(t.views.add))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '   ')
    await act(async () => input.form!.requestSubmit())
    expect(document.body.textContent).toContain(t.views.nameInvalid)
    expect(puts()).toHaveLength(0)
  })
})

describe('tabs', () => {
  const two = (send: ReturnType<typeof sender>) => putViews(send, FOLDER, [{ name: '全部', layout: 'list' }, { name: '看板', layout: 'board', group: 'status', color: 'teal' }])

  it('show the views in order, the first one at first, and switching shows the layout of the view', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    expect(tabNames()).toEqual(['全部', '看板'])
    expect(selected()).toBe('全部')
    expect(button(t.view.list)!.getAttribute('aria-pressed')).toBe('true')
    await click(tab('看板'))
    expect(selected()).toBe('看板')
    expect(button(t.view.board)!.getAttribute('aria-pressed')).toBe('true')
    await settle(() => document.querySelector('[data-slot="board-view"]') !== null, 'the board')
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBe((await saved()).views[1].id)
  })

  it('remember the last view in the browser, and fall back to the first when it is gone or storage is blocked', async () => {
    let ids: string[] = []
    await open('/s/default/f/Work', (send) => {
      two(send)
      ids = viewsOf(send, 'folder=Work').views.map((v) => v.id)
      localStorage.setItem(viewIdKey('default', 'folder', 'Work'), ids[1])
    })
    await settle(() => tabs().length === 2, 'the tabs')
    expect(selected()).toBe('看板')
    await m!.unmount()
    m = undefined
    await open('/s/default/f/Work', (send) => {
      two(send)
      localStorage.setItem(viewIdKey('default', 'folder', 'Work'), 'gone')
    })
    await settle(() => tabs().length === 2, 'the tabs again')
    expect(selected()).toBe('全部')
    await m!.unmount()
    m = undefined
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs with blocked storage')
    expect(selected()).toBe('全部')
    await click(tab('看板'))
    expect(selected()).toBe('看板')
  })

  it('mark a view as modified when its layout, sort, filters, group or columns are changed, and Revert takes the changes back', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    expect(modifiedMark()).toBeNull()
    expect(textButton(t.views.save)).toBeNull()
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
    expect(modifiedMark()!.closest('[role="tab"]')).toBe(tab('全部'))
    expect(textButton(t.views.save)).not.toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    // Back to what the view says: nothing is modified any more.
    await click(button(t.view.list))
    expect(modifiedMark()).toBeNull()
    // The sort goes in the address, and counts too.
    const sort = document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!
    await type(sort, 'title')
    expect(locationText()).toContain('sort=title')
    expect(modifiedMark()).not.toBeNull()
    await click(textButton(t.views.revert))
    expect(modifiedMark()).toBeNull()
    expect(locationText()).not.toContain('sort=')
    // Nothing was written: the views were made behind the app's back, and a change is not a save.
    expect(puts()).toHaveLength(0)
  })

  it('are saved only when asked: Save writes the whole list with the changed view, keeping what the app does not know', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('看板'))
    await settle(() => byLabel(t.view.board)!.getAttribute('aria-pressed') === 'true', 'board')
    await click(button(t.view.table))
    const before = puts().length
    expect(modifiedMark()).not.toBeNull()
    // Nothing was written by the change itself.
    expect(before).toBe(puts().length)
    const rev = (await saved()).rev
    await click(textButton(t.views.save))
    await settle(() => puts().length === before + 1, 'the save')
    const body = JSON.parse(puts().at(-1)!.body!)
    expect(body.baseRev).toBe(rev)
    expect(body.views.map((v: any) => [v.name, v.layout])).toEqual([['全部', 'list'], ['看板', 'table']])
    // The first view is as it was; the other keeps its id and its colour.
    expect(body.views[0].id).toBeTruthy()
    expect(body.views[1].color).toBe('teal')
    await settle(() => modifiedMark() === null, 'no longer modified')
    expect((await saved()).views[1]).toMatchObject({ layout: 'table', color: 'teal' })
  })

  it('filters are the view\'s too: a filter changes the listing and the tab, and is saved with the view', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await settle(() => rowTitles().length === 4, 'four pages')
    await click(await settleFor(() => textButton(tp().filter.add)))
    const field = document.querySelector<HTMLSelectElement>(`select[aria-label="${tp().filter.field}"]`)!
    await type(field, 'status')
    const chip = await settleFor(() => [...document.querySelectorAll<HTMLButtonElement>('[role="group"] button')].find((b) => b.textContent === 'todo') ?? null)
    await click(chip)
    await click(textButton(tp().filter.apply))
    await settle(() => rowTitles().length === 1, 'one page left')
    expect(listUrls().at(-1)).toContain('"op":"in"')
    expect(modifiedMark()).not.toBeNull()
    await click(textButton(t.views.save))
    await settle(() => (viewsOf(sender(m!.server), 'folder=Work').views[0].filter as any[]).length === 1, 'saved')
    expect((await saved()).views[0].filter).toEqual([{ key: 'status', op: 'in', values: ['todo'] }])
  })

  it('can be renamed, copied, moved and deleted, each written at once with the rev it was made from', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    // rename the shown view
    await click(button(t.views.rename))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    expect(input.value).toBe('全部')
    await type(input, '所有页面')
    await act(async () => input.form!.requestSubmit())
    await settle(() => tabNames()[0] === '所有页面', 'renamed')
    expect(selected()).toBe('所有页面')
    // copy: a new tab at the end with a new id, and it is shown
    await click(button(t.views.duplicate))
    await settle(() => tabs().length === 3, 'copy')
    expect(tabNames()[2]).toBe(t.views.copyName('所有页面'))
    expect(selected()).toBe(t.views.copyName('所有页面'))
    const ids = (await saved()).views.map((v) => v.id)
    expect(new Set(ids).size).toBe(3)
    // move it to the front
    // (the button is off while a move is being written: the second click waits for the first to settle)
    await click(button(t.views.moveLeft))
    await settle(() => tabNames()[1] === t.views.copyName('所有页面') && !button(t.views.moveLeft)!.disabled, 'moved once')
    await click(button(t.views.moveLeft))
    await settle(() => tabNames()[0] === t.views.copyName('所有页面'), 'moved')
    expect((await saved()).views.map((v) => v.name)).toEqual([t.views.copyName('所有页面'), '所有页面', '看板'])
    expect(button(t.views.moveLeft)!.disabled).toBe(true)
    // delete it, after being asked
    await click(button(t.views.remove))
    expect(document.body.textContent).toContain(t.views.deleteTitle(t.views.copyName('所有页面')))
    expect((await saved()).views).toHaveLength(3)
    await click(textButton(t.views.deleteConfirm))
    await settle(() => tabs().length === 2, 'deleted')
    expect((await saved()).views.map((v) => v.name)).toEqual(['所有页面', '看板'])
    expect(selected()).toBe('所有页面')
    // each write named the rev it came from
    const revs = puts().map((r) => JSON.parse(r.body!).baseRev)
    expect(new Set(revs).size).toBe(revs.length)
  })

  it('are put in another order by dragging a tab onto another', async () => {
    await open('/s/default/f/Work', (send) => putViews(send, FOLDER, [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'cards' }, { name: '丙', layout: 'list' }]))
    await settle(() => tabs().length === 3, 'the tabs')
    const dt = { setData() {}, effectAllowed: '' }
    const fire = (el: Element, type: string) => act(async () => {
      const e = new Event(type, { bubbles: true, cancelable: true })
      Object.assign(e, { dataTransfer: dt })
      el.dispatchEvent(e)
    })
    await fire(tab('丙'), 'dragstart')
    await fire(tab('甲'), 'dragover')
    await fire(tab('甲'), 'drop')
    await settle(() => tabNames().join() === '丙,甲,乙', 'reordered')
    expect((await saved()).views.map((v) => v.name)).toEqual(['丙', '甲', '乙'])
  })

  it('at most twenty: the buttons that would make a twenty-first are off', async () => {
    await open('/s/default/f/Work', (send) => putViews(send, FOLDER, Array.from({ length: 20 }, (_, i) => ({ name: `v${i}`, layout: 'list' }))))
    await settle(() => tabs().length === 20, 'twenty tabs')
    expect(button(t.views.tooMany(20))).not.toBeNull()
    expect((document.querySelectorAll(`[aria-label="${t.views.tooMany(20)}"]`) as NodeListOf<HTMLButtonElement>).length).toBe(2)
    expect([...document.querySelectorAll<HTMLButtonElement>(`[aria-label="${t.views.tooMany(20)}"]`)].every((b) => b.disabled)).toBe(true)
  })
})

// The person's own write of the views is exclusive: while it is on its way the tabs, the tools of the view, the sort,
// Save and Revert are off (aria-busy), nothing read meanwhile is taken in, and when it has settled the views are read
// again. The four races of review round 10 are impossible or harmless that way.
describe('while the person\'s own write of the views is on its way', () => {
  beforeEach(() => {
    toast.dismiss()
  })
  const two = (send: ReturnType<typeof sender>) => putViews(send, FOLDER, [{ name: '全部', layout: 'list' }, { name: '看板', layout: 'board', group: 'status' }])
  const sortSelect = () => document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!
  const bar = () => document.querySelector('[data-slot="view-bar"]')
  const off = () => bar()?.getAttribute('aria-busy') === 'true' && tabs().every((e) => (e as HTMLButtonElement).disabled) && sortSelect().disabled
  const on = () => bar() !== null && bar()!.getAttribute('aria-busy') === null && tabs().every((e) => !(e as HTMLButtonElement).disabled) && !sortSelect().disabled
  const isViews = (method: string, input: RequestInfo | URL) => (method === 'PUT' || method === 'GET') && /\/views(\?|$)/.test(String(input instanceof Request ? input.url : input))
  // Holds the answers of the requests that match after the server has answered them (so they are written, or read,
  // at once, and arrive when released); `fail` makes them fail instead.
  const hold = (which: (method: string) => boolean, how: { fail?: boolean } = {}) => {
    const real = globalThis.fetch
    let release: () => void = () => {}
    const held = new Promise<void>((r) => (release = r))
    globalThis.fetch = async (input, init) => {
      const method = init?.method ?? 'GET'
      if (!isViews(method, input) || !which(method)) return real(input, init)
      const res = await real(input, init)
      await held
      if (how.fail) throw new TypeError('network down')
      return res
    }
    return {
      release: () => act(async () => release()),
      restore: () => (globalThis.fetch = real),
    }
  }
  const refetch = () => act(async () => void m!.qc.invalidateQueries({ queryKey: keys.views('default', FOLDER) }))

  it('round 10 (1): Save, then a click on another tab and back while it is on its way: the tabs are off, and the saved view is shown as saved', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const h = hold((method) => method === 'PUT')
    await click(textButton(t.views.save))
    await settle(off, 'everything off')
    expect(button(t.view.cards)!.disabled).toBe(true)
    expect((textButton(t.views.revert) as HTMLButtonElement).disabled).toBe(true)
    await click(tab('看板'))
    await click(tab('全部'))
    expect(selected()).toBe('全部')
    await h.release()
    await settle(() => on() && modifiedMark() === null, 'saved and on again')
    expect(selected()).toBe('全部')
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    // Nothing is left to save: no old settings can be written back.
    expect(textButton(t.views.save)).toBeNull()
    expect(puts()).toHaveLength(1)
    expect((await saved()).views[0]).toMatchObject({ name: '全部', layout: 'cards' })
  })

  it('round 10 (2): another person\'s view read during an own rename is taken in only after it, and can then be picked and changed without a word of loss', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    const h = hold((method) => method === 'PUT')
    await click(button(t.views.rename))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '全部二')
    await act(async () => input.form!.requestSubmit())
    await settle(() => puts().length === 1 && off(), 'the rename on its way')
    // Written: another person adds a view, and the views are read again before the answer comes.
    putViews(sender(m!.server), FOLDER, [...(await saved()).views, { name: '别人的', layout: 'list' }])
    await refetch()
    await new Promise((r) => setTimeout(r, 50))
    expect(tabNames()).toEqual(['全部', '看板'])
    await h.release()
    await settle(() => on() && tabNames().join() === '全部二,看板,别人的', 'the views read after the rename')
    expect(selected()).toBe('全部二')
    await click(tab('别人的'))
    await click(button(t.view.cards))
    await new Promise((r) => setTimeout(r, 50))
    await act(async () => {})
    expect(selected()).toBe('别人的')
    expect(modifiedMark()).not.toBeNull()
    expect(tabNames()).toEqual(['全部二', '看板', '别人的'])
    expect(document.body.textContent!.includes(t.views.lostUnnamed)).toBe(false)
    expect(document.body.textContent!.includes(t.views.lost('别人的'))).toBe(false)
  })

  it('round 10 (3): a read sent before the save that arrives after it is called off: the saved settings stay, and nothing could be changed meanwhile', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await type(sortSelect(), 'title')
    // A read of the views, answered with the list before the save, held back.
    const g = hold((method) => method === 'GET')
    const reads = () => m!.requests.filter((r) => r.method === 'GET' && /\/views\?/.test(r.url)).length
    const before = reads()
    await refetch()
    await settle(() => reads() > before, 'the read sent')
    g.restore()
    const h = hold((method) => method === 'PUT')
    await click(textButton(t.views.save))
    await settle(off, 'everything off')
    // Nothing can be changed meanwhile: the sort is off, and a change sent to it anyway is not taken.
    await type(sortSelect(), '')
    await h.release()
    await settle(() => on() && modifiedMark() === null && puts().length === 1, 'saved')
    await g.release()
    await new Promise((r) => setTimeout(r, 50))
    await act(async () => {})
    expect(sortSelect().value).toBe('title')
    expect(modifiedMark()).toBeNull()
    expect(m!.qc.getQueryData<{ views: { sort: string }[] }>(keys.views('default', FOLDER))!.views[0].sort).toBe('title')
  })

  it('nothing stays off after a save whose answer is lost on the way', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const h = hold((method) => method === 'PUT', { fail: true })
    await click(textButton(t.views.save))
    await settle(off, 'everything off')
    await h.release()
    await settle(() => on() && document.body.textContent!.includes(t.views.failed(t.errors.network)), 'on again, and told')
    // The server wrote it before the answer was lost: the views read again say so.
    await settle(() => modifiedMark() === null, 'the views read again')
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
  })

  it('nothing stays off after a refused save (409)', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    putViews(sender(m!.server), FOLDER, [...(await saved()).views, { name: '别人的', layout: 'list' }])
    await click(button(t.view.cards))
    await click(textButton(t.views.save))
    await settle(() => on() && document.body.textContent!.includes(t.views.conflict) && tabs().length === 3, 'refused, on again')
    expect(modifiedMark()).not.toBeNull()
  })

  it('leaving the listing does not stop the write; opened again, it is off (no tabs yet) until the write settles, then on', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const h = hold((method) => method === 'PUT')
    await click(textButton(t.views.save))
    await settle(off, 'everything off')
    await act(async () => nav!('/s/default/t/栀子'))
    await settle(() => tabs().length === 0, 'another listing')
    await act(async () => nav!('/s/default/f/Work'))
    // Opened again while the write is on its way: nothing is taken in yet (no tabs), and the sort is off.
    await settle(() => locationText() === '/s/default/f/Work' && sortSelect()?.disabled === true, 'still off')
    expect(tabs()).toHaveLength(0)
    await h.release()
    await settle(() => tabs().length === 2 && on(), 'on again')
    await settle(() => button(t.view.cards)!.getAttribute('aria-pressed') === 'true' && modifiedMark() === null, 'the saved view')
    expect((await saved()).views[0].layout).toBe('cards')
  })
})

// The address before the views are read for the first time.
describe('an address that changes before the views are read', () => {
  beforeEach(() => {
    toast.dismiss()
  })
  const slowViews = { delay: (method: string, url: string) => (method === 'GET' && /\/views\?/.test(url) ? 300 : 0) }

  it('round 10 (4): "sort=" for the view it names is kept as given, and the first list decides', async () => {
    let id = ''
    m = await mount(withNav(listShell), {
      ...slowViews,
      entries: (server) => {
        const send = sender(server)
        seedWork(server)
        putViews(send, FOLDER, [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list', sort: 'title' }])
        id = viewsOf(send, 'folder=Work').views[1].id
        localStorage.setItem(viewIdKey('default', 'folder', 'Work'), id)
        return ['/s/default/f/Work']
      },
    })
    await act(async () => nav!(`/s/default/f/Work?view=${id}&sort=`))
    await settle(() => tabs().length === 2, 'the tabs')
    expect(selected()).toBe('乙')
    expect(document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!.value).toBe('')
    expect(modifiedMark()).not.toBeNull()
  })

  it('round 10 (4): "sort=" for the implicit view, which the first list shows is gone: dropped with the word for a link', async () => {
    m = await mount(withNav(listShell), {
      ...slowViews,
      entries: (server) => {
        seedWork(server)
        putViews(sender(server), FOLDER, [{ name: '甲', layout: 'list', sort: 'title' }])
        return ['/s/default/f/Work']
      },
    })
    await act(async () => nav!('/s/default/f/Work?sort='))
    await settle(() => tabs().length === 1, 'the tabs')
    await settle(() => document.body.textContent!.includes(t.views.linkGone), 'the word')
    expect(modifiedMark()).toBeNull()
  })
})

describe('when someone else changed the views', () => {
  const two = (send: ReturnType<typeof sender>) => putViews(send, FOLDER, [{ name: '全部', layout: 'list' }, { name: '看板', layout: 'board', group: 'status' }])

  it('a save from an old rev is refused: the person is told, the tabs show the views as they are now, and nothing was overwritten', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    // Another person adds a view and renames one.
    putViews(sender(m!.server), FOLDER, [{ ...(await saved()).views[0], name: '别人改的' }, (await saved()).views[1], { name: '别人的新视图', layout: 'cards' }])
    await click(button(t.view.cards))
    await click(textButton(t.views.save))
    await settle(() => document.body.textContent!.includes(t.views.conflict), 'the message')
    await settle(() => tabs().length === 3, 'the other person\'s tabs')
    expect(tabNames()).toEqual(['别人改的', '看板', '别人的新视图'])
    expect((await saved()).views[0]).toMatchObject({ name: '别人改的', layout: 'list' })
    // The view shown is still the one the person was in, with the change not yet saved: it can be saved again.
    expect(selected()).toBe('别人改的')
    expect(modifiedMark()).not.toBeNull()
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null, 'saved the second time')
    expect((await saved()).views[0]).toMatchObject({ name: '别人改的', layout: 'cards' })
  })

  it('a rename, a copy and a delete are refused the same way', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    putViews(sender(m!.server), FOLDER, [(await saved()).views[0]])
    await click(button(t.views.duplicate))
    await settle(() => document.body.textContent!.includes(t.views.conflict), 'the message')
    await settle(() => tabs().length === 1, 'the views as they are')
    expect((await saved()).views).toHaveLength(1)
  })

  it('a view that the other person deleted is not shown any more: the first one is', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('看板'))
    putViews(sender(m!.server), FOLDER, [(await saved()).views[0]])
    await click(button(t.views.rename))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '新名字')
    await act(async () => input.form!.requestSubmit())
    await settle(() => tabs().length === 1, 'the views as they are')
    expect(selected()).toBe('全部')
  })
})

describe('someone who may only read', () => {
  async function openReadOnly() {
    m = await mount(listShell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        const send = (method: string, path: string, body?: unknown) => owner.request(method, `/api/spaces/${space}${path}`, body)
        send('PUT', '/types/task', TASK)
        send('POST', '/folders', { path: 'Work' })
        send('PUT', '/folders/type', { folder: 'Work', type: 'task' })
        send('POST', '/pages', { folder: 'Work', meta: { title: 'A', status: 'done' }, body: '' })
        const cur = send('GET', '/views?folder=Work').body as { rev: string }
        send('PUT', '/views', { folder: 'Work', baseRev: cur.rev, views: [{ name: '全部', layout: 'list' }, { name: '表格', layout: 'table' }] })
        return [`/s/${space}/f/Work`]
      },
    })
    await settle(() => tabs().length === 2, 'the tabs')
  }

  it('sees the tabs and can switch and adjust a view, but has no way to make, change or save views', async () => {
    await openReadOnly()
    expect(byLabel(t.views.actions)).toBeNull()
    expect(button(t.views.add)).toBeNull()
    await click(tab('表格'))
    await settle(() => document.querySelector('[data-slot="table-view"]') !== null, 'the table')
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
    // No Save, only Revert, and a word that says why.
    expect(textButton(t.views.save)).toBeNull()
    expect(document.body.textContent).toContain(t.views.readOnly)
    await click(textButton(t.views.revert))
    expect(modifiedMark()).toBeNull()
    expect(m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')).toEqual([])
  })

  it('who has no view at all sees no tabs and no button', async () => {
    m = await mount(listShell, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        owner.request('POST', `/api/spaces/${space}/folders`, { path: 'Work' })
        return [`/s/${space}/f/Work`]
      },
    })
    await settle(() => byLabel(t.view.list) !== null, 'the listing')
    await new Promise((r) => setTimeout(r, 50))
    expect(document.querySelector('[data-slot="view-bar"]')).toBeNull()
  })
})

describe('a tag', () => {
  async function openTag(scene?: (send: ReturnType<typeof sender>) => void) {
    return open('/s/default/t/栀子', (send) => scene?.(send))
  }

  it('has views of its own and every layout but the table; the board only once a view says what to group by', async () => {
    await openTag()
    await settle(() => rowTitles().length === 1, 'the page tagged')
    expect(button(t.view.list)).not.toBeNull()
    expect(button(t.view.cards)).not.toBeNull()
    expect(button(t.view.read)).not.toBeNull()
    expect(button(t.view.table)).toBeNull()
    expect(button(t.view.board)).toBeNull()
    // The settings take the property's name.
    const settings = await settleFor(() => button(tb().tools.settings))
    await click(settings)
    const key = await settleFor(() => document.querySelector<HTMLInputElement>(`input[aria-label="${tb().tools.groupKey}"]`))
    await type(key, 'status')
    await act(async () => key.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })))
    await settle(() => button(t.view.board) !== null, 'the board option')
    // A view made now is the tag's, and is written under the tag.
    await click(button(t.view.board))
    await click(button(t.views.add))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '状态')
    await act(async () => input.form!.requestSubmit())
    await settle(() => tabs().length === 1, 'the tab')
    const body = JSON.parse(puts()[0].body!)
    expect(body).toMatchObject({ tag: '栀子', views: [{ name: '状态', layout: 'board', group: 'status' }] })
    expect(body.folder).toBeUndefined()
    expect(viewsOf(sender(m!.server), 'tag=%E6%A0%80%E5%AD%90').views).toHaveLength(1)
    expect(localStorage.getItem(viewIdKey('default', 'tag', '栀子'))).toBeTruthy()
  })

  it('opens at its saved board', async () => {
    await openTag((send) => putViews(send, { tag: '栀子' }, [{ name: '状态', layout: 'board', group: 'status' }]))
    await settle(() => document.querySelector('[data-slot="board-view"]') !== null, 'the board')
    expect(button(t.view.board)!.getAttribute('aria-pressed')).toBe('true')
  })
})

describe('a view whose layout this listing cannot show', () => {
  it('keeps its layout when another part of it is saved (a table in a tag shows as a list)', async () => {
    await open('/s/default/t/栀子', (send) => putViews(send, { tag: '栀子' }, [{ name: '表', layout: 'table' }]))
    await settle(() => tabs().length === 1 && rowTitles().length === 1, 'the tab and the page')
    // The listing shows a list; the view is not modified by that.
    expect(button(t.view.list)!.getAttribute('aria-pressed')).toBe('true')
    expect(modifiedMark()).toBeNull()
    const sort = document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!
    await type(sort, 'title')
    await settle(() => modifiedMark() !== null, 'the change')
    await click(textButton(t.views.save))
    await settle(() => puts().length === 1, 'the save')
    expect(JSON.parse(puts()[0].body!).views[0]).toMatchObject({ name: '表', layout: 'table', sort: 'title' })
    expect(viewsOf(sender(m!.server), 'tag=%E6%A0%80%E5%AD%90').views[0].layout).toBe('table')
  })
})

const tp = () => tpWords
const tb = () => tbWords
async function settleFor<T>(find: () => T | null | undefined): Promise<T> {
  let found: T | null | undefined = null
  await settle(() => (found = find()) != null, 'the element')
  return found as T
}

describe('a view written by hand', () => {
  const hand = (_send: unknown, _ids: unknown, server: MockServer) => server.seedHandWrittenViews('Work', [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }])
  const dragOnto = async (from: string, to: string) => {
    const dt = { setData() {}, effectAllowed: '' }
    const fire = (el: Element, type: string) => act(async () => {
      const e = new Event(type, { bubbles: true, cancelable: true })
      Object.assign(e, { dataTransfer: dt })
      el.dispatchEvent(e)
    })
    await fire(tab(from), 'dragstart')
    await fire(tab(to), 'dragover')
    await fire(tab(to), 'drop')
  }

  it('stays the view shown when it is saved: the write gives it a new id and the tab, the memory and the page follow', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    expect((await saved()).views.map((v) => v.id)).toEqual(['@0', '@1'])
    await click(tab('乙'))
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBe('@1')
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null && puts().length === 1, 'saved')
    expect(selected()).toBe('乙')
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    const id = (await saved()).views[1].id
    expect(id).not.toMatch(/^@/)
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBe(id)
  })

  it('keeps its unsaved changes when another write gives it a new id', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await click(button(t.view.cards))
    await dragOnto('甲', '乙')
    await settle(() => puts().length === 1 && tabNames().join() === '乙,甲', 'reordered')
    expect(selected()).toBe('乙')
    expect(modifiedMark()).not.toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).not.toMatch(/^@/)
  })

  // Another person writes the list: every "@n" gets a real id, in the same order, and a third view is added.
  const otherWrites = async () => {
    putViews(sender(m!.server), FOLDER, [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }, { name: '丙', layout: 'cards' }])
    expect((await saved()).views.map((v) => v.id).some((id) => id.startsWith('@'))).toBe(false)
  }
  const stillInTheSecond = async () => {
    const id = (await saved()).views[1].id
    expect(selected()).toBe('乙')
    expect(modifiedMark()).not.toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    await settle(() => localStorage.getItem(viewIdKey('default', 'folder', 'Work')) === id, 'the remembered id follows')
  }

  it('keeps its unsaved changes when a save is refused after another person named every view', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await click(button(t.view.cards))
    await otherWrites()
    await click(textButton(t.views.save))
    await settle(() => document.body.textContent!.includes(t.views.conflict) && tabs().length === 3, 'the conflict')
    await stillInTheSecond()
    // And the change can be saved on the next try.
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null, 'saved')
    expect((await saved()).views[1]).toMatchObject({ name: '乙', layout: 'cards' })
  })

  it('keeps its unsaved changes when the views are read again after another person named every view', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await click(button(t.view.cards))
    await otherWrites()
    await act(async () => {
      await m!.qc.invalidateQueries({ queryKey: keys.views('default', FOLDER) })
    })
    await settle(() => tabs().length === 3, 'the refetched tabs')
    await stillInTheSecond()
  })
})

// Views written by hand are "@n" by place; another person's write may delete, reorder, insert or rename views before
// this person's own write names them. The changes of a person never land on another view: they follow the view by its
// name and settings, or they are dropped with a message.
describe('a view written by hand while another person changes the list', () => {
  beforeEach(() => {
    toast.dismiss()
  })
  const three = (_send: unknown, _ids: unknown, server: MockServer) => server.seedHandWrittenViews('Work', [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }, { name: '丙', layout: 'list' }])
  const list = (...names: string[]) => names.map((name) => ({ name, layout: 'list' }))
  const other = (views: Record<string, unknown>[]) => putViews(sender(m!.server), FOLDER, views)
  const refetch = async () => {
    await act(async () => {
      await m!.qc.invalidateQueries({ queryKey: keys.views('default', FOLDER) })
    })
  }
  const editSecond = async (scene = three) => {
    await open('/s/default/f/Work', scene)
    await settle(() => tabs().length >= 2, 'the tabs')
    await click(tabs()[1])
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
  }
  const layouts = async () => Object.fromEntries((await saved()).views.map((v) => [v.name, v.layout]))
  const lostMessage = (name: string) => document.body.textContent!.includes(t.views.lost(name))
  const staysOnSecond = async () => {
    await settle(() => !(localStorage.getItem(viewIdKey('default', 'folder', 'Work')) ?? '').startsWith('@'), 'the remembered id follows')
    expect(selected()).toBe('乙')
    expect(modifiedMark()).not.toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    expect(document.body.textContent!.includes(t.views.lost('乙'))).toBe(false)
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null && puts().length === 1, 'saved')
    expect(await layouts()).toMatchObject({ 乙: 'cards' })
    expect((await saved()).views.filter((v) => v.layout === 'cards')).toHaveLength(1)
  }
  const dropped = async (names: string) => {
    await settle(() => tabNames().join() === names && lostMessage('乙'), 'the message')
    expect(modifiedMark()).toBeNull()
    expect(selected()).toBe(tabNames()[0])
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('false')
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBeNull()
    expect(puts()).toHaveLength(0)
    expect((await saved()).views.every((v) => v.layout === 'list')).toBe(true)
  }

  it('follows the view when the first view is deleted', async () => {
    await editSecond()
    other(list('乙', '丙'))
    await refetch()
    await settle(() => tabNames().join() === '乙,丙', 'the new tabs')
    await staysOnSecond()
    expect((await saved()).views.map((v) => v.name)).toEqual(['乙', '丙'])
  })

  it('follows the view when the order is swapped', async () => {
    await editSecond()
    other(list('乙', '甲', '丙'))
    await refetch()
    await settle(() => tabNames().join() === '乙,甲,丙', 'the new tabs')
    await staysOnSecond()
    expect((await saved()).views.map((v) => v.name)).toEqual(['乙', '甲', '丙'])
  })

  it('follows the view when another is inserted before it', async () => {
    await editSecond()
    other(list('新', '甲', '乙', '丙'))
    await refetch()
    await settle(() => tabs().length === 4, 'the new tabs')
    await staysOnSecond()
    expect((await saved()).views.map((v) => v.name)).toEqual(['新', '甲', '乙', '丙'])
  })

  it('drops the changes, and says so, when the other person renamed the view', async () => {
    await editSecond()
    other(list('甲', '乙乙', '丙'))
    await refetch()
    await dropped('甲,乙乙,丙')
    expect(selected()).toBe('甲')
  })

  it('drops the changes when two views have the same name and settings, rather than guess', async () => {
    await editSecond((_s, _i, server) => server.seedHandWrittenViews('Work', [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }]))
    other(list('丙', '乙', '乙'))
    await refetch()
    await dropped('丙,乙,乙')
    expect(selected()).toBe('丙')
  })

  it('drops the changes when the other person deleted the view', async () => {
    await editSecond()
    other(list('甲', '丙'))
    await refetch()
    await dropped('甲,丙')
  })

  it('on a refused save: follows the view, and the next save lands on it', async () => {
    await editSecond()
    other(list('乙', '丙'))
    await click(textButton(t.views.save))
    await settle(() => document.body.textContent!.includes(t.views.conflict) && tabNames().join() === '乙,丙', 'the conflict')
    expect(puts()).toHaveLength(1)
    expect((await saved()).views.every((v) => v.layout === 'list')).toBe(true)
    await staysOnSecondAfterRefusal()
  })
  // The refused attempt was one PUT; the next one is the second.
  const staysOnSecondAfterRefusal = async () => {
    await settle(() => !(localStorage.getItem(viewIdKey('default', 'folder', 'Work')) ?? '').startsWith('@'), 'the remembered id follows')
    expect(selected()).toBe('乙')
    expect(modifiedMark()).not.toBeNull()
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null && puts().length === 2, 'saved')
    expect(await layouts()).toEqual({ 乙: 'cards', 丙: 'list' })
  }

  it('on a refused save: drops the changes and says so when the view was renamed, and saves nothing on another view', async () => {
    await editSecond()
    other(list('甲', '乙乙', '丙'))
    await click(textButton(t.views.save))
    await settle(() => tabNames().join() === '甲,乙乙,丙' && lostMessage('乙'), 'the message')
    expect(modifiedMark()).toBeNull()
    expect(selected()).toBe('甲')
    expect(puts()).toHaveLength(1)
    expect((await saved()).views.every((v) => v.layout === 'list')).toBe(true)
  })

  it('keeps the view the person moved: the write answers in the order sent, even for views with the same name', async () => {
    await editSecond((_s, _i, server) => server.seedHandWrittenViews('Work', [{ name: '乙', layout: 'list' }, { name: '乙', layout: 'list' }, { name: '丙', layout: 'list' }]))
    await click(button(t.views.moveLeft))
    await settle(() => puts().length === 1 && tabs().map((e) => e.getAttribute('aria-selected')).join() === 'true,false,false', 'moved to the first place')
    expect(modifiedMark()).not.toBeNull()
    expect(document.body.textContent!.includes(t.views.lost('乙'))).toBe(false)
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null && puts().length === 2, 'saved')
    expect((await saved()).views.map((v) => v.layout)).toEqual(['cards', 'list', 'list'])
  })

  it('keeps the changes of a view that the person renamed', async () => {
    await editSecond()
    await click(button(t.views.rename))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '乙二')
    await act(async () => input.form!.requestSubmit())
    await settle(() => puts().length === 1 && tabNames().includes('乙二'), 'renamed')
    expect(selected()).toBe('乙二')
    expect(modifiedMark()).not.toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null && puts().length === 2, 'saved')
    expect(await layouts()).toEqual({ 甲: 'list', 乙二: 'cards', 丙: 'list' })
  })
})

describe('a view written by hand, then a new view or a copy', () => {
  beforeEach(() => {
    toast.dismiss()
  })
  const hand = (_send: unknown, _ids: unknown, server: MockServer) => server.seedHandWrittenViews('Work', [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }])
  const showSecond = async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
  }

  it('shows the new view that was made while a hand-written one was shown', async () => {
    await showSecond()
    await click(button(t.views.add))
    await settle(() => document.querySelector('input[aria-label="' + t.views.name + '"]') !== null, 'the name dialog')
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '新')
    await act(async () => input.form!.requestSubmit())
    await settle(() => puts().length === 1 && tabNames().includes('新'), 'created')
    await settle(() => selected() === '新', 'the new view is shown')
    const list = (await saved()).views
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBe(list[list.length - 1].id)
    expect(modifiedMark()).toBeNull()
  })

  it('shows the copy that was made of the hand-written view shown, with its changes saved in the copy only', async () => {
    await showSecond()
    await click(button(t.views.duplicate))
    await settle(() => puts().length === 1 && tabs().length === 3, 'copied')
    await settle(() => selected() === t.views.copyName('乙'), 'the copy is shown')
    expect(modifiedMark()).toBeNull()
    expect((await saved()).views.map((v) => v.layout)).toEqual(['list', 'list', 'cards'])
  })

  it('does not say the view is lost when a refetch lands before the answer of its own save', async () => {
    await showSecond()
    const real = globalThis.fetch
    let release: () => void = () => {}
    const held = new Promise<void>((r) => (release = r))
    globalThis.fetch = async (input, init) => {
      const res = await real(input, init)
      if ((init?.method ?? 'GET') === 'PUT' && String(input instanceof Request ? input.url : input).endsWith('/views')) await held
      return res
    }
    await click(textButton(t.views.save))
    await settle(() => puts().length === 1, 'the write is sent')
    // The server has committed; a refetch sees the new settings before the answer arrives.
    await act(async () => {
      await m!.qc.invalidateQueries({ queryKey: keys.views('default', FOLDER) })
    })
    await new Promise((r) => setTimeout(r, 100))
    await act(async () => {})
    expect(document.body.textContent!.includes(t.views.lost('乙'))).toBe(false)
    await act(async () => release())
    await settle(() => modifiedMark() === null, 'saved')
    expect(document.body.textContent!.includes(t.views.lost('乙'))).toBe(false)
    expect(selected()).toBe('乙')
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
  })
})

describe('the tab that is shown', () => {
  it('keeps what was changed in it when it is clicked again; another tab is the way to leave it', async () => {
    await open('/s/default/f/Work', (send) => putViews(send, FOLDER, [{ name: '全部', layout: 'list' }, { name: '看板', layout: 'board', group: 'status' }]))
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
    await click(tab('全部'))
    expect(modifiedMark()).not.toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
  })
})

describe('the name of a view', () => {
  const zh = (n: number) => '字'.repeat(n)
  it('is counted in bytes as the server counts them: 67 characters of 3 bytes are too long, 66 are not', async () => {
    await open('/s/default/f/Work')
    await settle(() => button(t.views.add) !== null, 'the add button')
    await click(button(t.views.add))
    const input = () => document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input(), zh(67))
    await act(async () => input().form!.requestSubmit())
    expect(document.body.textContent).toContain(t.views.nameInvalid)
    expect(puts()).toHaveLength(0)
    await type(input(), zh(66))
    await act(async () => input().form!.requestSubmit())
    await settle(() => puts().length === 1, 'the save')
  })

  it('of a copy is cut to fit, not refused by the server', async () => {
    const long = zh(65)
    await open('/s/default/f/Work', (send) => putViews(send, FOLDER, [{ name: long, layout: 'list' }]))
    await settle(() => tabs().length === 1, 'the tab')
    await click(button(t.views.duplicate))
    await settle(() => tabs().length === 2, 'the copy')
    expect(puts()).toHaveLength(1)
    expect(new TextEncoder().encode((await saved()).views[1].name).length).toBeLessThanOrEqual(200)
  })
})

// The sort is part of the working copy, like everything else changed in a view, and never lands on another view.
describe('the sort of a view that someone else removes or renames', () => {
  beforeEach(() => {
    toast.dismiss()
  })
  const refetch = async () => {
    refetchMark()
    await act(async () => {
      await m!.qc.invalidateQueries({ queryKey: keys.views('default', FOLDER) })
    })
  }
  const sortTo = async (value: string) => type(document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!, value)
  const sortShown = () => document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!.value
  const lost = (name: string) => document.body.textContent!.includes(t.views.lost(name))
  const real = (send: ReturnType<typeof sender>) => putViews(send, FOLDER, [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }])
  const hand = (_send: unknown, _ids: unknown, server: MockServer) => server.seedHandWrittenViews('Work', [{ name: '甲', layout: 'list' }, { name: '乙', layout: 'list' }])

  // The listings asked for from the moment the other person's change is read.
  let from = 0
  const refetchMark = () => (from = listUrls().length)
  const expectDropped = async () => {
    await settle(() => selected() === '甲' && lost('乙'), 'the first view and the message')
    expect(modifiedMark()).toBeNull()
    expect(textButton(t.views.save)).toBeNull()
    expect(sortShown()).toBe('')
    await settle(() => !locationText().includes('sort='), 'the address without the sort')
    expect(listUrls().slice(from).filter((u) => u.includes('sort=title'))).toEqual([])
  }

  it('a real id, the sort only changed, the view deleted by another person: 甲 is shown unmodified and the person is told', async () => {
    await open('/s/default/f/Work', real)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await sortTo('title')
    expect(locationText()).toContain('sort=title')
    expect(locationText()).toContain(`view=${(await saved()).views[1].id}`)
    putViews(sender(m!.server), FOLDER, [(await saved()).views[0]])
    await refetch()
    await settle(() => tabs().length === 1, 'the views as they are')
    await expectDropped()
    expect(puts()).toHaveLength(0)
  })

  it('a real id, other changes than the sort, the view deleted by another person: dropped with a word', async () => {
    await open('/s/default/f/Work', real)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await click(button(t.view.cards))
    putViews(sender(m!.server), FOLDER, [(await saved()).views[0]])
    await refetch()
    await settle(() => tabs().length === 1, 'the views as they are')
    await expectDropped()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('false')
  })

  it('a hand-written view, the sort only changed, renamed by another person: 甲 is shown unmodified, the person is told, and nothing can save the sort into 甲', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await sortTo('title')
    expect(locationText()).toContain('view=%401')
    putViews(sender(m!.server), FOLDER, [{ name: '甲', layout: 'list' }, { name: '乙乙', layout: 'list' }])
    await refetch()
    await settle(() => tabNames().join() === '甲,乙乙', 'the new tabs')
    await expectDropped()
    expect((await saved()).views.every((v) => v.sort === '')).toBe(true)
  })

  it('a hand-written view, the sort only changed, the file edited by hand (renamed in place): dropped with a word', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await sortTo('title')
    m!.server.seedHandWrittenViews('Work', [{ name: '甲', layout: 'list' }, { name: '乙乙', layout: 'list' }])
    await refetch()
    await settle(() => tabNames().join() === '甲,乙乙', 'the new tabs')
    await expectDropped()
  })

  it('a hand-written view whose sort was changed follows the view when another person names the views, and saves the sort on it', async () => {
    await open('/s/default/f/Work', hand)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('乙'))
    await sortTo('title')
    putViews(sender(m!.server), FOLDER, [{ name: '乙', layout: 'list' }, { name: '甲', layout: 'list' }])
    await refetch()
    await settle(() => tabNames().join() === '乙,甲' && selected() === '乙', 'followed')
    const id = (await saved()).views[0].id
    await settle(() => locationText().includes(`view=${id}`), 'the address follows')
    expect(modifiedMark()).not.toBeNull()
    expect(lost('乙')).toBe(false)
    await click(textButton(t.views.save))
    await settle(() => modifiedMark() === null, 'saved')
    expect((await saved()).views.map((v) => [v.name, v.sort])).toEqual([['乙', 'title'], ['甲', '']])
  })

  it('the sort in the address belongs to the view it names: opening it shows that view with the sort, another view never gets it', async () => {
    m = await mount(listShell, {
      entries: (server) => {
        const send = sender(server)
        seedWork(server)
        real(send)
        return [`/s/default/f/Work?sort=title&view=${viewsOf(send, 'folder=Work').views[1].id}`]
      },
    })
    await settle(() => tabs().length === 2 && selected() === '乙', 'the view named')
    expect(sortShown()).toBe('title')
    expect(modifiedMark()).not.toBeNull()
    await click(tab('甲'))
    expect(sortShown()).toBe('')
    expect(modifiedMark()).toBeNull()
    await settle(() => !locationText().includes('sort='), 'the address without the sort')
  })
})

describe('when the views cannot be read after the person\'s own write, or an answer does not come', () => {
  const timing = { ...viewsTiming }
  beforeEach(() => {
    toast.dismiss()
    Object.assign(viewsTiming, { timeoutMs: 300, retryMs: 50, retries: 5 })
  })
  afterEach(() => Object.assign(viewsTiming, timing))
  const two = (send: ReturnType<typeof sender>) => putViews(send, FOLDER, [{ name: '全部', layout: 'list' }, { name: '看板', layout: 'board', group: 'status' }])
  const sortSelect = () => document.querySelector<HTMLSelectElement>(`select[aria-label="${t.list.sort}"]`)!
  const bar = () => document.querySelector('[data-slot="view-bar"]')
  const on = () => bar() !== null && bar()!.getAttribute('aria-busy') === null && tabs().every((e) => !(e as HTMLButtonElement).disabled) && !sortSelect().disabled
  const said = (text: string) => document.body.textContent!.includes(text)
  const viewsReads = () => m!.requests.filter((r) => r.method === 'GET' && /\/views\?/.test(r.url)).length
  const urlOf = (input: RequestInfo | URL) => String(input instanceof Request ? input.url : input)
  // Never answers; called off only by its signal (as a fetch is when its time runs out).
  const never = (init?: RequestInit) =>
    new Promise<Response>((_, reject) => {
      const s = init?.signal
      s?.addEventListener('abort', () => reject(s.reason))
    })
  // From the first PUT of the views on, reads of the views fail (`how: 'fail'`, offline) or never answer (`'hang'`),
  // until `back()`.
  const readsAfterPut = (how: 'fail' | 'hang') => {
    const real = globalThis.fetch
    let put = false
    let down = true
    globalThis.fetch = async (input, init) => {
      const method = init?.method ?? 'GET'
      const views = /\/views(\?|$)/.test(urlOf(input))
      if (views && method === 'PUT') {
        const res = await real(input, init)
        put = true
        return res
      }
      if (views && method === 'GET' && put && down) {
        if (how === 'hang') return never(init)
        throw new TypeError('offline')
      }
      return real(input, init)
    }
    return {
      back: () => (down = false),
      restore: () => (globalThis.fetch = real),
    }
  }
  // The PUT of the views never answers: before the server has it (`written: false`), or after it wrote it.
  const putHangs = (written: boolean) => {
    const real = globalThis.fetch
    globalThis.fetch = async (input, init) => {
      if ((init?.method ?? 'GET') === 'PUT' && /\/views$/.test(urlOf(input))) {
        if (written) await real(input, init)
        return never(init)
      }
      return real(input, init)
    }
    return { restore: () => (globalThis.fetch = real) }
  }

  it('a save written, the read after it offline: the saved settings are shown, nothing is off, and the views are read again later', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const r = readsAfterPut('fail')
    await click(textButton(t.views.save))
    await settle(() => on() && said(t.views.readLate), 'on again, and told')
    expect(selected()).toBe('全部')
    expect(modifiedMark()).toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    expect(tabNames()).toEqual(['全部', '看板'])
    expect((await saved()).views[0].layout).toBe('cards')
    // A later Save writes nothing old: there is nothing to save, and the next change goes on the saved settings.
    expect(textButton(t.views.save)).toBeNull()
    // Online again: a read in the background, without anything going off.
    const before = viewsReads()
    r.back()
    await settle(() => viewsReads() > before && on(), 'read again in the background')
    r.restore()
    await type(sortSelect(), 'title')
    await click(textButton(t.views.save))
    await settle(() => on() && modifiedMark() === null && puts().length === 2, 'saved again')
    expect((await saved()).views[0]).toMatchObject({ layout: 'cards', sort: 'title' })
  })

  it('a new view written, the read after it offline: its tab is there and shown, and it can be changed and saved', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    const r = readsAfterPut('fail')
    await click(button(t.views.add))
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${t.views.name}"]`)!
    await type(input, '新的')
    await act(async () => input.form!.requestSubmit())
    await settle(() => on() && tabNames().join() === '全部,看板,新的', 'the new tab')
    expect(selected()).toBe('新的')
    await settle(() => said(t.views.readLate), 'told')
    expect(localStorage.getItem(viewIdKey('default', 'folder', 'Work'))).toBe((await saved()).views[2].id)
    // Its own: a change shows as a change of it, and is saved on it.
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
    r.back()
    r.restore()
    await click(textButton(t.views.save))
    await settle(() => on() && modifiedMark() === null && puts().length === 2, 'saved')
    expect((await saved()).views.map((v) => [v.name, v.layout])).toEqual([['全部', 'list'], ['看板', 'board'], ['新的', 'cards']])
  })

  it('a copy written, the read after it offline: its tab is there and shown, with what was shown in it', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const r = readsAfterPut('fail')
    await click(button(t.views.duplicate))
    await settle(() => on() && tabs().length === 3, 'the copy')
    r.restore()
    expect(tabNames()).toEqual(['全部', '看板', t.views.copyName('全部')])
    expect(selected()).toBe(t.views.copyName('全部'))
    expect(modifiedMark()).toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    await type(sortSelect(), 'title')
    expect(modifiedMark()).not.toBeNull()
    await click(textButton(t.views.save))
    await settle(() => on() && modifiedMark() === null && puts().length === 2, 'saved')
    expect((await saved()).views.map((v) => [v.name, v.layout, v.sort])).toEqual([
      ['全部', 'list', ''],
      ['看板', 'board', ''],
      [t.views.copyName('全部'), 'cards', 'title'],
    ])
  })

  it('a delete written, the read after it offline: the tab is gone and the first view is shown', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(tab('看板'))
    const r = readsAfterPut('fail')
    await click(button(t.views.remove))
    await click(textButton(t.views.deleteConfirm))
    await settle(() => on() && tabs().length === 1, 'the tab gone')
    r.restore()
    expect(tabNames()).toEqual(['全部'])
    expect(selected()).toBe('全部')
    await settle(() => said(t.views.readLate), 'told')
    await click(button(t.view.cards))
    expect(modifiedMark()).not.toBeNull()
  })

  it('the read after a save never answers: once its time is out the saved settings are shown and nothing is off', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const r = readsAfterPut('hang')
    await click(textButton(t.views.save))
    await settle(() => on() && said(t.views.readLate), 'on again after the time ran out')
    expect(modifiedMark()).toBeNull()
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
    r.back()
    r.restore()
  })

  it('a save that never answers, not written: once its time is out the person is told it is not known, nothing is off, and the change is still there', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const h = putHangs(false)
    await click(textButton(t.views.save))
    await settle(() => on() && said(t.views.unknown), 'on again, and told')
    h.restore()
    expect(selected()).toBe('全部')
    expect(modifiedMark()).not.toBeNull()
    expect((await saved()).views[0].layout).toBe('list')
    // It can be saved again.
    await click(textButton(t.views.save))
    await settle(() => on() && modifiedMark() === null, 'saved')
    expect((await saved()).views[0].layout).toBe('cards')
  })

  it('a save that never answers, written: the views are read again, so the saved settings are shown', async () => {
    await open('/s/default/f/Work', two)
    await settle(() => tabs().length === 2, 'the tabs')
    await click(button(t.view.cards))
    const before = viewsReads()
    const h = putHangs(true)
    await click(textButton(t.views.save))
    await settle(() => on() && said(t.views.unknown), 'on again, and told')
    h.restore()
    expect(viewsReads()).toBeGreaterThan(before)
    await settle(() => modifiedMark() === null, 'the views read again')
    expect(button(t.view.cards)!.getAttribute('aria-pressed')).toBe('true')
  })
})
