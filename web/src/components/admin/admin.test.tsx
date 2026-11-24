// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { api, markSignedIn } from '@/api/client'
import { applySession } from '@/api/hooks'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { ta } from '@/i18n/admin'
import { tg } from '@/i18n/manage'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  PERSON_PASSWORD,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The administration pages, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const heading = () => document.querySelector('h1')?.textContent
const navLinks = () => [...document.querySelectorAll(`nav[aria-label="${ta.nav}"] a`)].map((a) => a.textContent)
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-space]')]
const rowOf = (name: string) => rows().find((r) => r.querySelector('th')?.textContent?.includes(name))!
const menuItem = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === label)!
const adminCalls = () => m!.requests.filter((r) => r.url.startsWith('/api/admin') || r.url.startsWith('/api/users'))

async function openMenu(name: string) {
  await click(byLabel(ta.spaces.moreFor(name)))
  await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
}

describe('who can reach the administration', () => {
  it('an administrator of an accounts server has it: users and invites first, and /admin goes there', async () => {
    m = await mount(routes, { entries: ['/admin'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => locationText() === '/admin/users' && text().includes(t.users.createInvite), 'users and invites')
    expect(heading()).toBe(ta.title)
    expect(navLinks()).toEqual([
      ta.sections.users,
      ta.sections.groups,
      ta.sections.spaces,
      ta.sections.manage,
      ta.sections.recycle,
      ta.sections.daily,
      ta.sections.access,
      ta.sections.storage,
      ta.sections.security,
      ta.sections.mail,
      ta.sections.settings,
      ta.sections.tenants,
      ta.sections.usage,
      ta.sections.status,
    ])
    expect(document.querySelector('nav [aria-current="page"]')?.textContent).toBe(ta.sections.users)
  })

  it('a member gets the page every unknown address gets, and nothing of the administration is asked for', async () => {
    // Nobody is signed in when the page opens; then a member signs in.
    m = await mount(routes, { entries: ['/admin/spaces'], server: { mode: 'accounts' } })
    const person = makePerson(m.server, 'carol')
    await act(async () => {
      applySession(m!.qc, (await api.login({ username: person.username, password: PERSON_PASSWORD })).user)
    })
    await settle(() => text().includes(t.notFound), 'not found')
    expect(heading()).toBeUndefined()
    expect(text()).not.toContain(ta.sections.spaces)
    expect(adminCalls()).toEqual([])
  })

  it('a single-user server has no administration: the address is not found', async () => {
    m = await mount(routes, { entries: ['/admin/spaces'], server: { mode: 'single' } })
    await settle(() => text().includes(t.notFound), 'not found')
    expect(heading()).toBeUndefined()
    expect(adminCalls()).toEqual([])
  })

  it('someone not signed in does not get it either', async () => {
    m = await mount(routes, { entries: ['/admin'], server: { mode: 'accounts' } })
    await settle(() => text().includes(t.notFound), 'not found')
    expect(adminCalls()).toEqual([])
    markSignedIn()
  })
})

describe('all spaces', () => {
  const open = (setup?: (server: Mounted['server']) => void) =>
    mount(routes, {
      entries: (server) => {
        setup?.(server)
        return ['/admin/spaces']
      },
      signIn: true,
      // No limit on the number of spaces (--max-spaces 0): this page is about lists, not quotas.
      server: { mode: 'accounts', maxSpaces: 0 },
    })

  it('is a table with the owner, the members, a dash for a size not measured, sync and the state; personal spaces are marked', async () => {
    m = await open((server) => {
      const tang = makePerson(server, 'tang')
      const id = (tang.request('POST', '/api/spaces', { name: '团队空间' }).body as { id: string }).id
      tang.request('POST', `/api/spaces/${id}/members`, { username: 'admin', role: 'viewer' })
    })
    await settle(() => rows().length === 3, 'three spaces')
    const team = rowOf('团队空间')
    const cells = [...team.querySelectorAll('td')].map((c) => c.textContent)
    // The first cell is the tick box of a bulk action.
    expect(cells[1]).toContain('名 tang')
    expect(cells[2]).toBe('2')
    expect(cells[3]).toContain(ta.spaces.notMeasured)
    expect(cells[4]).toContain(tg.where.names.server)
    expect(cells[4]).toContain(tg.where.counted)
    expect(cells[5]).toBe(ta.spaces.syncStates.local)
    expect(cells[6]).toContain(ta.spaces.active)
    expect(team.textContent).not.toContain(ta.spaces.personal)
    expect(rows().filter((r) => r.textContent?.includes(ta.spaces.personal))).toHaveLength(2)
    // The way into a space's content.
    expect(team.querySelector('a')!.getAttribute('href')).toMatch(/^\/s\/[^/]+\/$/)
    expect(text()).toContain(ta.spaces.allLoaded(3))
  })

  it('measures one space on request and shows the size in human units', async () => {
    m = await open()
    await settle(() => rows().length === 1, 'the personal space')
    const row = rows()[0]
    expect(row.textContent).toContain(ta.spaces.notMeasured)
    await click(
      row.querySelector(
        `button[aria-label="${t.fmt.labelled(ta.spaces.measure, row.querySelector('th')!.textContent!.replace(ta.spaces.personal, ''))}"]`,
      ),
    )
    await settle(() => /\d+(\.\d)? (KB|MB)/.test(rowOf('').textContent ?? ''), 'a size')
    expect(m.requests.some((r) => r.method === 'POST' && /\/api\/admin\/spaces\/[^/]+\/size$/.test(r.url))).toBe(true)
    expect(rows()[0].textContent).not.toContain(ta.spaces.notMeasured)
  })

  it('shows 50 at a time and loads more on request, until all are there', async () => {
    m = await open((server) => {
      const admin = adminRequests(server)
      for (let i = 1; i <= 55; i++) admin('POST', '/api/spaces', { name: `空间 ${String(i).padStart(2, '0')}` })
    })
    await settle(() => rows().length === 50, 'the first page')
    expect(text()).toContain(ta.spaces.loaded(50))
    const more = () => [...document.querySelectorAll('button')].find((b) => b.textContent === ta.spaces.loadMore)
    expect(more()).toBeDefined()
    await click(more())
    await settle(() => rows().length === 56, 'the rest')
    expect(more()).toBeUndefined()
    expect(text()).toContain(ta.spaces.allLoaded(56))
    const listed = m.requests
      .filter((r) => r.url.startsWith('/api/admin/spaces?'))
      .map((r) => new URL(r.url, 'http://x').searchParams)
    expect(listed[0].get('limit')).toBe('50')
    expect(listed[0].get('cursor')).toBeNull()
    expect(listed[1].get('cursor')).toBeTruthy()
  })

  it('searches by name once typing stops, and says when nothing matches', async () => {
    m = await open((server) => {
      const admin = adminRequests(server)
      for (const name of ['苹果园', '苹果酱', '香蕉']) admin('POST', '/api/spaces', { name })
    })
    await settle(() => rows().length === 4, 'all spaces')
    const box = document.querySelector<HTMLInputElement>(`input[aria-label="${ta.spaces.search}"]`)!
    await type(box, '苹果')
    await settle(() => rows().length === 2, 'the matches')
    expect(m.requests.some((r) => new URL(r.url, 'http://x').searchParams.get('q') === '苹果')).toBe(true)
    await type(box, '没有这个')
    await settle(() => text().includes(ta.spaces.noMatch('没有这个')), 'no match')
  })

  it('transfers ownership to a chosen user', async () => {
    let space = ''
    m = await open((server) => {
      makePerson(server, 'tang')
      space = (adminRequests(server)('POST', '/api/spaces', { name: '转让' }).body as { id: string }).id
    })
    await settle(() => rows().length === 3, 'spaces')
    await openMenu('转让')
    await click(menuItem(ta.spaces.transfer))
    await settle(() => document.querySelector('#transfer-to') !== null, 'the dialog')
    const pick = document.querySelector<HTMLSelectElement>('#transfer-to')!
    await settle(() => pick.options.length > 1, 'the users')
    // The current owner is not offered; the button waits for a choice.
    expect([...pick.options].map((o) => o.textContent)).toEqual([ta.spaces.transferChoose, '名 tang (tang)'])
    const confirm = () =>
      [...document.querySelectorAll('[role="dialog"] button')].find(
        (b) => b.textContent === ta.spaces.transferConfirm,
      ) as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    await type(pick, [...pick.options][1].value)
    await click(confirm())
    await settle(() => rowOf('转让').textContent!.includes('名 tang'), 'the new owner')
    const sent = m.requests.find((r) => r.method === 'POST' && r.url === `/api/admin/spaces/${space}/transfer`)!
    expect(JSON.parse(sent.body!)).toEqual({ userId: m.server.userId('tang') })
  })

  it('looks for the new owner by name on the server, never loading every user', async () => {
    m = await open((server) => {
      for (const name of ['tang', 'tangerine', 'mei']) makePerson(server, name)
      adminRequests(server)('POST', '/api/spaces', { name: '转让' })
    })
    await settle(() => rows().length === 5, 'spaces')
    await openMenu('转让')
    await click(menuItem(ta.spaces.transfer))
    await settle(() => document.querySelector('#transfer-search') !== null, 'the dialog')
    const pick = document.querySelector<HTMLSelectElement>('#transfer-to')!
    await settle(() => pick.options.length > 1, 'the first users')
    await type(document.querySelector<HTMLInputElement>('#transfer-search')!, 'tang')
    await settle(
      () =>
        [...pick.options].map((o) => o.textContent).join() ===
        [ta.spaces.transferChoose, '名 tang (tang)', '名 tangerine (tangerine)'].join(),
      'the matches',
    )
    // Every request for users says what it wants and how many: none asks for the lot.
    const asked = m.requests
      .filter((r) => new URL(r.url, 'http://x').pathname === '/api/users')
      .map((r) => new URL(r.url, 'http://x').search)
    expect(asked.length).toBeGreaterThan(0)
    for (const q of asked) expect(q).toContain('limit=20')
    expect(asked.some((q) => q.includes('q=tang'))).toBe(true)
    // Disabled accounts are left out by the server, ahead of the limit.
    for (const q of asked) expect(q).toContain('enabled=1')
    // The pick stays when the search moves on.
    await type(pick, [...pick.options][1].value)
    await type(document.querySelector<HTMLInputElement>('#transfer-search')!, 'mei')
    await settle(() => [...pick.options].some((o) => o.textContent === '名 mei (mei)'), 'the other matches')
    expect(pick.selectedOptions[0].textContent).toBe('名 tang (tang)')
    await type(document.querySelector<HTMLInputElement>('#transfer-search')!, 'nobody-like-this')
    await settle(() => document.body.textContent!.includes(ta.spaces.transferNoMatch), 'no match')
  })

  it('disables a space after asking, and enables it again', async () => {
    m = await open((server) => void adminRequests(server)('POST', '/api/spaces', { name: '停用我' }))
    await settle(() => rows().length === 2, 'spaces')
    await openMenu('停用我')
    await click(menuItem(ta.spaces.disable))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(ta.spaces.disableTitle('停用我'))
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === ta.spaces.disableConfirm,
      ),
    )
    await settle(() => rowOf('停用我').textContent!.includes(ta.spaces.disabled), 'marked disabled')
    await openMenu('停用我')
    expect(document.body.textContent).not.toContain(ta.spaces.disable)
    await click(menuItem(ta.spaces.enable))
    await settle(() => !rowOf('停用我').textContent!.includes(ta.spaces.disabled), 'enabled again')
  })

  it('deletes a space once its name is typed: it goes to the recycle bin, with the days it stays', async () => {
    m = await open((server) => void adminRequests(server)('POST', '/api/spaces', { name: '删掉我' }))
    await settle(() => rows().length === 2, 'spaces')
    await openMenu('删掉我')
    await click(menuItem(tg.spaces.delete))
    await settle(
      () => document.querySelector('#delete-confirm') !== null && text().includes(tg.del.daysLine(30)),
      'the dialog with the preview',
    )
    const confirm = () =>
      [...document.querySelectorAll('[role="dialog"] button')].find(
        (b) => b.textContent === tg.del.space.confirm,
      ) as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, '删掉')
    expect(confirm().disabled).toBe(true)
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, '删掉我')
    expect(confirm().disabled).toBe(false)
    await click(confirm())
    await settle(() => text().includes(tg.del.space.deleted('删掉我')), 'the result')
    await click([...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === tg.bulk.close))
    await settle(() => rows().length === 1, 'the space is out of the table')
    expect(m.server.manage.recycled().map((i) => i.name)).toEqual(['删掉我'])
  })

  it('compares the typed name trimmed and normalised, and shows the name to copy', async () => {
    const name = 'Caf\u00e9'
    m = await open((server) => void adminRequests(server)('POST', '/api/spaces', { name }))
    await settle(() => rows().length === 2, 'spaces')
    await openMenu(name)
    await click(menuItem(tg.spaces.delete))
    await settle(() => document.querySelector('#delete-confirm') !== null, 'the dialog')
    expect([...document.querySelectorAll('[role="dialog"] code')].map((c) => c.textContent)).toContain(name)
    const confirm = () =>
      [...document.querySelectorAll('[role="dialog"] button')].find(
        (b) => b.textContent === tg.del.space.confirm,
      ) as HTMLButtonElement
    // The same letters, composed the other way, with spaces around.
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, ' Cafe\u0301  ')
    expect(confirm().disabled).toBe(false)
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, 'cafe')
    expect(confirm().disabled).toBe(true)
  })

  it('a personal space cannot be transferred, and its deletion says it goes with its person', async () => {
    m = await open()
    await settle(() => rows().length === 1, 'the personal space')
    const name = rows()[0].querySelector('th span')!.textContent!
    await openMenu(name)
    expect(menuItem(ta.spaces.transfer).getAttribute('aria-disabled')).toBe('true')
    expect(menuItem(ta.spaces.disable).getAttribute('aria-disabled')).not.toBe('true')
    await click(menuItem(tg.spaces.delete))
    await settle(() => text().includes(tg.del.space.personalBlocked), 'the explanation')
    const confirm = [...document.querySelectorAll('[role="dialog"] button')].find(
      (b) => b.textContent === tg.del.space.confirm,
    ) as HTMLButtonElement
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, name)
    expect(confirm.disabled).toBe(true)
  })
})

describe('server status and users', () => {
  it('shows what the server says about itself', async () => {
    m = await mount(routes, { entries: ['/admin/status'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => text().includes(ta.status.goVersion), 'the status')
    const rows = Object.fromEntries(
      [...document.querySelectorAll('dl > div')].map((r) => [
        r.querySelector('dt')!.firstChild!.textContent,
        r.querySelector('dd')!.textContent,
      ]),
    )
    expect(rows[ta.status.version]).toBe('mock')
    expect(rows[ta.status.database]).toBe('sqlite')
    expect(rows[ta.status.spaces]).toBe('1')
    expect(rows[ta.status.users]).toBe('1')
    expect(rows[ta.status.disk]).toBe('50 GB')
    expect(rows[ta.status.uptime]).toBe(ta.status.duration(0, 0, 0))
  })

  it('users and invites are here: the list, the roles and the invite form', async () => {
    m = await mount(routes, { entries: ['/admin/users'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => text().includes(t.users.createInvite), 'users')
    expect(text()).toContain('管理员')
    expect(text()).toContain(t.users.invites)
  })
})
