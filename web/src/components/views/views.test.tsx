// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { byLabel, click, installDomShims, locationText, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { listShell, putViews, seedWork, sender, TASK, viewsOf } from '@/testing/listKit'
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

async function open(path = '/s/default/f/Work', scene?: (send: ReturnType<typeof sender>, ids: ReturnType<typeof seedWork>) => void, options: Parameters<typeof mount>[1] = {}) {
  m = await mount(listShell, {
    ...options,
    entries: (server) => {
      const send = sender(server)
      const ids = seedWork(server)
      scene?.(send, ids)
      return [path]
    },
  })
  await settle(() => byLabel(t.view.list) !== null, 'the listing')
  return m
}

const saved = () => viewsOf(sender(m!.server), 'folder=Work')

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
