// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { Route, Routes } from 'react-router'
import { api, markSignedIn } from '@/api/client'
import { applySession } from '@/api/hooks'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { adminRequests, byLabel, click, installDomShims, locationText, makePerson, mount, PERSON_PASSWORD, settle, type, type Mounted } from '@/testing/harness'
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
const navLinks = () => [...document.querySelectorAll(`nav[aria-label="${t.admin.nav}"] a`)].map((a) => a.textContent)
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-space]')]
const rowOf = (name: string) => rows().find((r) => r.querySelector('th')?.textContent?.includes(name))!
const menuItem = (label: string) => [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((e) => e.textContent?.trim() === label)!
const adminCalls = () => m!.requests.filter((r) => r.url.startsWith('/api/admin') || r.url.startsWith('/api/users'))

async function openMenu(name: string) {
  await click(byLabel(t.admin.spaces.moreFor(name)))
  await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the actions')
}

describe('who can reach the administration', () => {
  it('an administrator of an accounts server has it: users and invites first, and /admin goes there', async () => {
    m = await mount(routes, { entries: ['/admin'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => locationText() === '/admin/users' && text().includes(t.users.createInvite), 'users and invites')
    expect(heading()).toBe(t.admin.title)
    expect(navLinks()).toEqual([t.admin.sections.users, t.admin.sections.spaces, t.admin.sections.status])
    expect(document.querySelector('nav [aria-current="page"]')?.textContent).toBe(t.admin.sections.users)
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
    expect(text()).not.toContain(t.admin.sections.spaces)
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
      server: { mode: 'accounts' },
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
    expect(cells[0]).toContain('名 tang')
    expect(cells[1]).toBe('2')
    expect(cells[2]).toContain(t.admin.spaces.notMeasured)
    expect(cells[3]).toBe(t.admin.spaces.syncStates.local)
    expect(cells[4]).toContain(t.admin.spaces.active)
    expect(team.textContent).not.toContain(t.admin.spaces.personal)
    expect(rows().filter((r) => r.textContent?.includes(t.admin.spaces.personal))).toHaveLength(2)
    // The way into a space's content.
    expect(team.querySelector('a')!.getAttribute('href')).toMatch(/^\/s\/[^/]+\/recent$/)
    expect(text()).toContain(t.admin.spaces.allLoaded(3))
  })

  it('measures one space on request and shows the size in human units', async () => {
    m = await open()
    await settle(() => rows().length === 1, 'the personal space')
    const row = rows()[0]
    expect(row.textContent).toContain(t.admin.spaces.notMeasured)
    await click(row.querySelector(`button[aria-label="${t.fmt.labelled(t.admin.spaces.measure, row.querySelector('th')!.textContent!.replace(t.admin.spaces.personal, ''))}"]`))
    await settle(() => /\d+(\.\d)? (KB|MB)/.test(rowOf('').textContent ?? ''), 'a size')
    expect(m.requests.some((r) => r.method === 'POST' && /\/api\/admin\/spaces\/[^/]+\/size$/.test(r.url))).toBe(true)
    expect(rows()[0].textContent).not.toContain(t.admin.spaces.notMeasured)
  })

  it('shows 50 at a time and loads more on request, until all are there', async () => {
    m = await open((server) => {
      const admin = adminRequests(server)
      for (let i = 1; i <= 55; i++) admin('POST', '/api/spaces', { name: `空间 ${String(i).padStart(2, '0')}` })
    })
    await settle(() => rows().length === 50, 'the first page')
    expect(text()).toContain(t.admin.spaces.loaded(50))
    const more = () => [...document.querySelectorAll('button')].find((b) => b.textContent === t.admin.spaces.loadMore)
    expect(more()).toBeDefined()
    await click(more())
    await settle(() => rows().length === 56, 'the rest')
    expect(more()).toBeUndefined()
    expect(text()).toContain(t.admin.spaces.allLoaded(56))
    const listed = m.requests.filter((r) => r.url.startsWith('/api/admin/spaces?')).map((r) => new URL(r.url, 'http://x').searchParams)
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
    const box = document.querySelector<HTMLInputElement>(`input[aria-label="${t.admin.spaces.search}"]`)!
    await type(box, '苹果')
    await settle(() => rows().length === 2, 'the matches')
    expect(m.requests.some((r) => new URL(r.url, 'http://x').searchParams.get('q') === '苹果')).toBe(true)
    await type(box, '没有这个')
    await settle(() => text().includes(t.admin.spaces.noMatch('没有这个')), 'no match')
  })

  it('transfers ownership to a chosen user', async () => {
    let space = ''
    m = await open((server) => {
      makePerson(server, 'tang')
      space = (adminRequests(server)('POST', '/api/spaces', { name: '转让' }).body as { id: string }).id
    })
    await settle(() => rows().length === 3, 'spaces')
    await openMenu('转让')
    await click(menuItem(t.admin.spaces.transfer))
    await settle(() => document.querySelector('#transfer-to') !== null, 'the dialog')
    const pick = document.querySelector<HTMLSelectElement>('#transfer-to')!
    await settle(() => pick.options.length > 1, 'the users')
    // The current owner is not offered; the button waits for a choice.
    expect([...pick.options].map((o) => o.textContent)).toEqual([t.admin.spaces.transferChoose, '名 tang (tang)'])
    const confirm = () => [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t.admin.spaces.transferConfirm) as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    await type(pick, [...pick.options][1].value)
    await click(confirm())
    await settle(() => rowOf('转让').textContent!.includes('名 tang'), 'the new owner')
    const sent = m.requests.find((r) => r.method === 'POST' && r.url === `/api/admin/spaces/${space}/transfer`)!
    expect(JSON.parse(sent.body!)).toEqual({ userId: m.server.userId('tang') })
  })

  it('disables a space after asking, and enables it again', async () => {
    m = await open((server) => void adminRequests(server)('POST', '/api/spaces', { name: '停用我' }))
    await settle(() => rows().length === 2, 'spaces')
    await openMenu('停用我')
    await click(menuItem(t.admin.spaces.disable))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(t.admin.spaces.disableTitle('停用我'))
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.admin.spaces.disableConfirm))
    await settle(() => rowOf('停用我').textContent!.includes(t.admin.spaces.disabled), 'marked disabled')
    await openMenu('停用我')
    expect(document.body.textContent).not.toContain(t.admin.spaces.disable)
    await click(menuItem(t.admin.spaces.enable))
    await settle(() => !rowOf('停用我').textContent!.includes(t.admin.spaces.disabled), 'enabled again')
  })

  it('deletes a space once its name is typed, and says where it went', async () => {
    let space = ''
    m = await open((server) => {
      space = (adminRequests(server)('POST', '/api/spaces', { name: '删掉我' }).body as { id: string }).id
    })
    await settle(() => rows().length === 2, 'spaces')
    await openMenu('删掉我')
    await click(menuItem(t.admin.spaces.delete))
    await settle(() => document.querySelector('#delete-confirm') !== null, 'the dialog')
    const confirm = () => [...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t.admin.spaces.deleteConfirm) as HTMLButtonElement
    expect(confirm().disabled).toBe(true)
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, '删掉')
    expect(confirm().disabled).toBe(true)
    await type(document.querySelector<HTMLInputElement>('#delete-confirm')!, '删掉我')
    expect(confirm().disabled).toBe(false)
    await click(confirm())
    await settle(() => document.body.textContent!.includes(t.admin.spaces.deleted('删掉我')), 'the result')
    expect(document.body.textContent).toMatch(new RegExp(`trash/${space}-\\d{8}T\\d{6}Z`))
    await click([...document.querySelectorAll('[role="dialog"] button')].find((b) => b.textContent === t.common.close))
    await settle(() => rows().length === 1, 'the space is out of the table')
  })

  it('a personal space cannot be transferred or deleted from the menu', async () => {
    m = await open()
    await settle(() => rows().length === 1, 'the personal space')
    const name = rows()[0].querySelector('th span')!.textContent!
    await openMenu(name)
    expect(menuItem(t.admin.spaces.transfer).getAttribute('aria-disabled')).toBe('true')
    expect(menuItem(t.admin.spaces.delete).getAttribute('aria-disabled')).toBe('true')
    expect(menuItem(t.admin.spaces.disable).getAttribute('aria-disabled')).not.toBe('true')
  })
})

describe('server status and users', () => {
  it('shows what the server says about itself', async () => {
    m = await mount(routes, { entries: ['/admin/status'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => text().includes(t.admin.status.goVersion), 'the status')
    const rows = Object.fromEntries([...document.querySelectorAll('dl > div')].map((r) => [r.querySelector('dt')!.firstChild!.textContent, r.querySelector('dd')!.textContent]))
    expect(rows[t.admin.status.version]).toBe('mock')
    expect(rows[t.admin.status.database]).toBe('sqlite')
    expect(rows[t.admin.status.spaces]).toBe('1')
    expect(rows[t.admin.status.users]).toBe('1')
    expect(rows[t.admin.status.disk]).toBe('50 GB')
    expect(rows[t.admin.status.uptime]).toBe(t.admin.status.duration(0, 0, 0))
  })

  it('users and invites are here: the list, the roles and the invite form', async () => {
    m = await mount(routes, { entries: ['/admin/users'], signIn: true, server: { mode: 'accounts' } })
    await settle(() => text().includes(t.users.createInvite), 'users')
    expect(text()).toContain('管理员')
    expect(text()).toContain(t.users.invites)
  })
})
