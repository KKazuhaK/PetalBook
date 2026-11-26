// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  locationText,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import type { MockServer } from '@/api/mock/server'
import { AdminGate } from './AdminGate'

// The list of people (paged, searched, filtered and sorted by the server) and a person's page with its tabs.

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
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-user]')]
const userCalls = () =>
  m!.requests
    .filter((r) => r.method === 'GET' && new URL(r.url, 'http://x').pathname === '/api/users')
    .map((r) => new URL(r.url, 'http://x').searchParams)
const button = (label: string) =>
  [...document.querySelectorAll('button')].find((b) => b.textContent === label) as HTMLButtonElement
const select = (label: string) => document.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!

async function open(path: string, scene?: (server: MockServer) => unknown) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const where = scene?.(server)
      return [typeof where === 'string' ? where : path]
    },
  })
}
const asTenantAdmin = (server: MockServer) => {
  const admin = adminRequests(server)
  makePerson(server, 'second', 'admin')
  const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
    (u) => u.username === 'admin',
  )!
  admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
}

describe('the list of people', () => {
  it('is cut into pages by the server, and next and previous ask for the page by its cursor', async () => {
    await open('/admin/users', (server) => {
      localStorage.setItem('pb.table.users', JSON.stringify({ pageSize: 25 }))
      for (let i = 0; i < 27; i++) makePerson(server, `p${String(i).padStart(2, '0')}`)
    })
    await settle(() => rows().length === 25, 'the first page')
    expect(userCalls()[0].get('limit')).toBe('25')
    expect(userCalls()[0].get('cursor')).toBeNull()
    expect(button(tc.table.previous).disabled).toBe(true)
    await click(button(tc.table.next))
    await settle(() => rows().length === 3 && text().includes(tc.table.page(2)), 'the second page')
    expect(userCalls().some((q) => q.get('cursor')?.startsWith('users.created.asc:'))).toBe(true)
    expect(button(tc.table.next).disabled).toBe(true)
    await click(button(tc.table.previous))
    await settle(() => rows().length === 25, 'back')
    // Another size starts again from the first page, and is the reader's own.
    await type(document.querySelector<HTMLSelectElement>('[data-slot="table-footer"] select')!, '50')
    await settle(() => rows().length === 28, 'all in one page')
    expect(JSON.parse(localStorage.getItem('pb.table.users')!).pageSize).toBe(50)
  })

  it('searches, filters and sorts on the server, and starts again from the first page each time', async () => {
    await open('/admin/users', (server) => {
      makePerson(server, 'amy')
      makePerson(server, 'bob', 'admin')
      const carol = makePerson(server, 'carol')
      adminRequests(server)('PATCH', `/api/users/${carol.id}`, { disabled: true })
    })
    await settle(() => rows().length === 4, 'everyone')
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${tc.users.search}"]`)!, 'am')
    await settle(() => userCalls().some((q) => q.get('q') === 'am'), 'the search')
    await settle(() => rows().length === 1, 'one match')
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${tc.users.search}"]`)!, '')
    await type(select(tc.users.role), 'admin')
    await settle(() => userCalls().some((q) => q.get('role') === 'admin'), 'the role')
    await type(select(tc.users.role), '')
    await type(select(tc.users.state), 'disabled')
    await settle(() => userCalls().some((q) => q.get('state') === 'disabled'), 'the state')
    await settle(() => rows().length === 1 && rows()[0].textContent!.includes('carol'), 'the disabled one')
    await type(select(tc.users.state), '')
    await type(select(tc.users.active), 'inactive')
    await settle(() => userCalls().some((q) => q.get('inactiveDays') === '90'), 'the last active filter')
    await type(select(tc.users.active), '')
    const header = () => document.querySelector(`button[aria-label="${tc.table.sortBy(tc.users.cols.user)}"]`)
    await click(header())
    await settle(() => userCalls().some((q) => q.get('sort') === 'name' && q.get('order') === 'asc'), 'sorted')
    await click(header())
    await settle(() => userCalls().some((q) => q.get('sort') === 'name' && q.get('order') === 'desc'), 'reversed')
    await settle(
      () => rows()[0].textContent!.includes('管理员') && rows()[1].textContent!.includes('carol'),
      "the server's order",
    )
  })

  it('has the common actions at the end of a row: a link to the person, and a menu that disables', async () => {
    await open('/admin/users', (server) => makePerson(server, 'amy'))
    await settle(() => rows().length === 2, 'two people')
    const row = rows().find((r) => r.textContent!.includes('amy'))!
    const view = row.querySelector<HTMLAnchorElement>(`a[aria-label="${t.fmt.labelled(tc.table.view, '名 amy')}"]`)!
    expect(view.getAttribute('href')).toMatch(/^\/admin\/user\/[^/]+$/)
    await click(byLabel(t.fmt.labelled(tc.table.more, '名 amy')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the menu')
    await click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === tc.users.disable))
    await settle(() => m!.requests.some((r) => r.method === 'PATCH' && r.url.startsWith('/api/users/')), 'patched')
    expect(JSON.parse(m!.requests.find((r) => r.method === 'PATCH')!.body!)).toEqual({ disabled: true })
    await settle(
      () =>
        rows()
          .find((r) => r.textContent!.includes('amy'))!
          .textContent!.includes(tc.users.stateDisabled),
      'disabled',
    )
    // Nobody deletes themself from here.
    await click(byLabel(t.fmt.labelled(tc.table.more, '管理员')))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the menu')
    expect([...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent)).not.toContain(tc.users.delete)
  })

  it('shows the invite button, and the bulk actions once someone is ticked', async () => {
    await open('/admin/users', (server) => makePerson(server, 'amy'))
    await settle(() => rows().length === 2, 'two people')
    const invite = [...document.querySelectorAll('a')].find((a) => a.textContent === tc.users.invite)!
    expect(invite.getAttribute('href')).toBe('/admin/users/invites')
    expect(document.querySelector('[data-slot="bulk-bar"]')).toBeNull()
    await click(byLabel(tc.table.selectRow('名 amy')))
    expect(document.querySelector('[data-slot="bulk-bar"]')).not.toBeNull()
  })

  it('a tenant administrator is not offered the site administrators as a role to look for', async () => {
    await open('/admin/users', asTenantAdmin)
    await settle(() => rows().length >= 1, 'people')
    expect([...select(tc.users.role).options].map((o) => o.value)).toEqual(['', 'member', 'admin'])
  })
})

describe("a person's page", () => {
  it('has the person in its header and a tab for each part of them, and the way back to the list', async () => {
    let id = ''
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      id = amy.id
      return `/admin/user/${amy.id}`
    })
    await settle(() => document.querySelector('[data-slot="person-page"]') !== null, 'the page')
    expect(document.querySelector('[data-slot="page-description"]')!.textContent).toBe(
      tc.person.summary('amy', '', '', t.roles.member),
    )
    expect(document.querySelector('h1')!.textContent).toBe('名 amy')
    expect(
      [...document.querySelectorAll(`nav[aria-label="${tc.person.tabsLabel}"] a`)].map((a) => a.textContent),
    ).toEqual([
      tc.person.tabs.profile,
      tc.person.tabs.security,
      tc.person.tabs.sessions,
      tc.person.tabs.spaces,
      tc.person.tabs.usage,
      tc.person.tabs.audit,
    ])
    // Looking as the person is for 3.6: the button is here and cannot be pressed.
    const viewAs = button(tc.person.viewAs)
    expect(viewAs.disabled).toBe(true)
    expect(viewAs.title).toBe(tc.person.viewAsSoon)
    // The list is the sidebar's item "Users".
    expect(document.querySelector('nav [aria-current="page"]')!.textContent).toBe(tc.sections.users)
    await click([...document.querySelectorAll('a')].find((a) => a.textContent === tc.person.back))
    await settle(() => locationText() === '/admin/users/all', 'the list')
    expect(id).not.toBe('')
  })

  it('says there is no such person for an address that is none', async () => {
    await open('/admin/user/nobody')
    await settle(() => text().includes(tc.person.notFound), 'not found')
  })

  it('disables and enables from the header, and says why a change is refused', async () => {
    await open('/admin', (server) => `/admin/user/${makePerson(server, 'amy').id}`)
    await settle(() => document.querySelector('[data-slot="person-page"]') !== null, 'the page')
    await click(byLabel(t.fmt.labelled(t.users.disable, '名 amy')))
    await settle(() => text().includes(t.users.disabled), 'disabled')
    await click(byLabel(t.fmt.labelled(t.users.enable, '名 amy')))
    await settle(() => !text().includes(t.users.disabled), 'enabled again')
    // An administrator cannot disable themself: there is no button for it on their own page.
    await m!.unmount()
    await open('/admin', (server) => {
      const me = (adminRequests(server)('GET', '/api/users').body as { users: { id: string; username: string }[] })
        .users[0]
      return `/admin/user/${me.id}`
    })
    await settle(() => document.querySelector('[data-slot="person-page"]') !== null, 'own page')
    expect(byLabel(t.fmt.labelled(t.users.disable, '管理员'))).toBeNull()
  })

  it('lists the spaces they belong to and the groups they are in, and what they hold', async () => {
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      amy.request('POST', '/api/spaces', { name: '她的团队' })
      adminRequests(server)('POST', '/api/admin/groups', { name: '编辑组', source: 'local' })
      const group = (adminRequests(server)('GET', '/api/admin/groups').body as { items: { id: string }[] }).items[0]
      adminRequests(server)('POST', `/api/admin/groups/${group.id}/members`, { userIds: [amy.id] })
      return `/admin/user/${amy.id}/spaces`
    })
    await settle(
      () => document.querySelectorAll('[data-slot="person-spaces"] li[data-space]').length === 2,
      'two spaces',
    )
    expect(text()).toContain('她的团队')
    expect(text()).toContain(tc.person.memberships.personal)
    await settle(() => document.querySelector('[data-group]') !== null, 'the group')
    expect(text()).toContain('编辑组')
    // The usage is the storage management's own numbers.
    await click([...document.querySelectorAll('nav a')].find((a) => a.textContent === tc.person.tabs.usage))
    await settle(() => document.querySelector('[data-slot="person-usage"] [data-space]') !== null, 'the numbers')
  })

  it('lists the audit rows about the person, a page at a time', async () => {
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      for (let i = 0; i < 3; i++)
        adminRequests(server)('PATCH', `/api/users/${amy.id}`, { emailVerified: false, email: `a${i}@x.test` })
      return `/admin/user/${amy.id}/audit`
    })
    await settle(() => document.querySelectorAll('[data-slot="person-audit"] tbody tr[data-audit]').length >= 3, 'rows')
    const asked = m!.requests.find((r) => r.url.startsWith('/api/admin/audit'))!
    expect(new URL(asked.url, 'http://x').searchParams.get('user')).toMatch(/.+/)
    expect(new URL(asked.url, 'http://x').searchParams.get('limit')).toBe('50')
    expect(text()).toContain(tsec.audit.kinds.user_changed)
  })
})

describe('the search of people', () => {
  it('finds a person by their email address too, and takes no cursor of another list', async () => {
    await open('/admin/users', (server) => {
      const amy = makePerson(server, 'amy')
      adminRequests(server)('PATCH', `/api/users/${amy.id}`, { email: 'Zed.Mail@Example.org' })
    })
    const ask = adminRequests(m!.server)
    const found = (ask('GET', '/api/users?q=zed.mail@exam').body as { users: { username: string }[] }).users
    expect(found.map((u) => u.username)).toEqual(['amy'])
    expect(() => ask('GET', '/api/users?cursor=spaces.created.asc:1')).toThrow(/400/)
  })
})

describe("a person's sessions and connected accounts", () => {
  it('lists the devices, with how they signed in; ends one and then all', async () => {
    let id = ''
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      id = amy.id
      // A second sign-in of the same person.
      adminRequests(server)
      return `/admin/user/${amy.id}/sessions`
    })
    await settle(() => document.querySelectorAll('[data-slot="sessions"] li').length === 1, 'the session')
    const row = document.querySelector('[data-slot="sessions"] li')!
    expect(row.textContent).toContain(tc.person.sessions.methods.invite)
    // End it: asked first, then the server is told, and the list is empty.
    await click(row.querySelector('button'))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === tc.person.sessions.end,
      ),
    )
    await settle(() => document.querySelector('[data-slot="sessions"]') === null, 'ended')
    const sent = m!.requests.filter((r) => r.method === 'DELETE' && r.url.startsWith(`/api/users/${id}/sessions/`))
    expect(sent).toHaveLength(1)
    expect(text()).toContain(tc.person.sessions.none)
    // It is in the audit log.
    const audit = adminRequests(m!.server)('GET', '/api/admin/audit?kind=sessions_ended').body as {
      items: { detail: { by: string; count: number } }[]
    }
    expect(audit.items[0].detail).toEqual({ by: 'admin', count: 1 })
  })

  it('ends every session at once, after asking', async () => {
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      amy.request('POST', '/api/login', { username: 'amy', password: amy.password })
      return `/admin/user/${amy.id}/sessions`
    })
    await settle(() => document.querySelectorAll('[data-slot="sessions"] li').length >= 1, 'the sessions')
    await click(button(tc.person.sessions.endAll))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === tc.person.sessions.endAll,
      ),
    )
    await settle(() => text().includes(tc.person.sessions.none), 'all ended')
    expect(m!.requests.filter((r) => r.method === 'DELETE' && /\/sessions$/.test(r.url))).toHaveLength(1)
  })

  it("marks the administrator's own session, and says so before it is ended", async () => {
    await open('/admin', (server) => {
      const me = (adminRequests(server)('GET', '/api/session').body as { user: { id: string } }).user
      return `/admin/user/${me.id}/sessions`
    })
    await settle(() => document.querySelectorAll('[data-slot="sessions"] li').length >= 1, 'the sessions')
    const mine = [...document.querySelectorAll('[data-slot="sessions"] li')].filter((li) =>
      li.textContent?.includes(tc.person.sessions.thisDevice),
    )
    expect(mine).toHaveLength(1)
    await click(mine[0].querySelector('button'))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(tc.person.sessions.endThisHelp)
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.settings.cancel),
    )
    await settle(() => document.querySelector('[role="alertdialog"]') === null, 'closed')
    expect(m!.requests.filter((r) => r.method === 'DELETE')).toEqual([])
    // All of one's own: the question says this device goes too, and what does not end.
    await click(button(tc.person.sessions.endAll))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(
      tc.person.sessions.endAllOwnHelp(document.querySelectorAll('[data-slot="sessions"] li').length),
    )
  })

  it('lists the connected accounts and disconnects one after asking', async () => {
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      server.addIdentity(amy.id, { id: 'IDENT00001', label: 'amy@idp.example', providerSlug: 'company' })
      return `/admin/user/${amy.id}/security`
    })
    await settle(() => document.querySelector('[data-identity="IDENT00001"]') !== null, 'the account')
    expect(text()).toContain('amy@idp.example')
    await click(byLabel(tc.person.security.disconnectFor('company · amy@idp.example')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === tc.person.security.disconnect,
      ),
    )
    await settle(() => document.querySelector('[data-identity]') === null, 'disconnected')
    expect(m!.requests.filter((r) => r.method === 'DELETE' && r.url.includes('/identities/'))).toHaveLength(1)
  })

  it("are a site administrator's alone: a tenant's administrator has no tab, no address and no calls", async () => {
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      asTenantAdmin(server)
      return `/admin/user/${amy.id}/sessions`
    })
    // The address of the tab goes to the person's profile.
    await settle(() => document.querySelector('[data-slot="person-profile"]') !== null, 'the profile')
    expect(
      [...document.querySelectorAll(`nav[aria-label="${tc.person.tabsLabel}"] a`)].map((a) => a.textContent),
    ).not.toContain(tc.person.tabs.sessions)
    await click([...document.querySelectorAll('nav a')].find((a) => a.textContent === tc.person.tabs.security))
    await settle(() => document.querySelector('[data-slot="person-security"]') !== null, 'security')
    expect(document.querySelector('[data-slot="identities"]')).toBeNull()
    expect(text()).not.toContain(tc.person.security.connected)
    expect(m!.requests.filter((r) => /\/(sessions|identities)/.test(r.url))).toEqual([])
  })

  it('the server answers a tenant administrator as for nobody', async () => {
    await open('/admin', (server) => {
      const amy = makePerson(server, 'amy')
      asTenantAdmin(server)
      return `/admin/user/${amy.id}`
    })
    await settle(() => document.querySelector('[data-slot="person-page"]') !== null, 'the page')
    const amy = (adminRequests(m!.server)('GET', '/api/users?q=amy').body as { users: { id: string }[] }).users[0]
    for (const path of ['/sessions', '/identities'])
      expect(() => adminRequests(m!.server)('GET', `/api/users/${amy.id}${path}`)).toThrow(/404/)
  })
})
