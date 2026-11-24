// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { t } from '@/i18n'
import { tg } from '@/i18n/groups'
import type { MockServer } from '@/api/mock/server'
import {
  adminRequests,
  click,
  installDomShims,
  makePerson,
  mount,
  settle,
  shareSpace,
  type,
  type Mounted,
} from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The groups of a space's members, the view of everyone who can reach it, and "My groups", against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const tgs = tg.space
const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const panelText = () => dialog()?.textContent ?? ''
const button = (label: string, root: ParentNode = dialog()) =>
  [...root.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null
const groupRows = () => [...dialog().querySelectorAll<HTMLElement>('[data-slot="space-groups"] > li')]
const sent = () =>
  m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login').map((r) => `${r.method} ${r.url}`)

// The signed-in administrator owns 'Team'; two local groups exist, 'Engineering' with tang in it.
async function openAsOwner(scene?: (server: MockServer, space: string) => void) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const admin = adminRequests(server)
      const space = (admin('POST', '/api/spaces', { name: 'Team' }).body as { id: string }).id
      const tang = makePerson(server, 'tang')
      const eng = (admin('POST', '/api/admin/groups', { name: 'Engineering', source: 'local' }).body as { id: string })
        .id
      admin('POST', '/api/admin/groups', { name: 'Operations', source: 'local' })
      admin('POST', `/api/admin/groups/${eng}/members`, { userIds: [tang.id] })
      scene?.(server, space)
      localStorage.setItem('pb.space', space)
      return ['/settings/members']
    },
  })
  await settle(() => panelText().includes(tgs.addTitle), 'the groups of the members')
}

describe('the groups of a space: an owner', () => {
  it('finds a group by name, adds it with a role, and the list shows it with its people', async () => {
    await openAsOwner()
    expect(panelText()).toContain(tgs.none)
    await type(dialog().querySelector<HTMLInputElement>('#space-group-search')!, 'eng')
    await settle(() => button(t.fmt.labelled(tgs.choose, 'Engineering')) !== null, 'the match')
    // Anyone may find a group by name: it is the ordinary search endpoint, not the administration's.
    expect(m!.requests.some((r) => r.url.startsWith('/api/groups?q=eng'))).toBe(true)
    await click(button(t.fmt.labelled(tgs.choose, 'Engineering')))
    await type(dialog().querySelector<HTMLSelectElement>('#space-group-role')!, 'editor')
    await click(button(`${tgs.add}: Engineering`))
    await settle(() => groupRows().length === 1, 'the group in the list')
    const post = m!.requests.find((r) => r.method === 'POST' && r.url.endsWith('/groups'))!
    expect(JSON.parse(post.body!)).toEqual({ groupId: expect.any(String), role: 'editor' })
    expect(groupRows()[0].textContent).toContain('Engineering')
    expect(groupRows()[0].textContent).toContain(tgs.people(1))
    expect(groupRows()[0].textContent).toContain(tg.sourceNames.local)
    expect(
      dialog().querySelector<HTMLSelectElement>(`select[aria-label="${t.fmt.labelled('Engineering', tgs.role)}"]`)!
        .value,
    ).toBe('editor')
    // It is not offered again.
    await type(dialog().querySelector<HTMLInputElement>('#space-group-search')!, 'eng')
    await settle(() => panelText().includes(tgs.noMatch), 'nothing left to add')
  })

  it('asks for a group before adding, and changes a role and removes a group after asking', async () => {
    await openAsOwner((server, space) => {
      const admin = adminRequests(server)
      const list = admin('GET', '/api/admin/groups').body as { items: { id: string; name: string }[] }
      admin('POST', `/api/spaces/${space}/groups`, {
        groupId: list.items.find((g) => g.name === 'Engineering')!.id,
        role: 'viewer',
      })
    })
    await settle(() => groupRows().length === 1, 'the group')
    await click(button(tgs.add, dialog().querySelector(`form[aria-label="${tgs.addTitle}"]`)!))
    await settle(() => panelText().includes(tgs.pick), 'the question')
    const select = dialog().querySelector<HTMLSelectElement>(
      `select[aria-label="${t.fmt.labelled('Engineering', tgs.role)}"]`,
    )!
    await type(select, 'owner')
    await settle(() => sent().some((r) => r.startsWith('PATCH') && r.includes('/groups/')), 'the change is sent')
    await click(button(t.fmt.labelled(tgs.remove, 'Engineering')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(tgs.removeHelp('Engineering'))
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === tgs.removeConfirm),
    )
    await settle(() => groupRows().length === 0, 'the group is out')
    expect(sent().at(-1)).toMatch(/^DELETE \/api\/spaces\/.+\/groups\/.+$/)
  })

  it('shows everyone who can reach the space with how, and filters them', async () => {
    await openAsOwner((server, space) => {
      const admin = adminRequests(server)
      const list = admin('GET', '/api/admin/groups').body as { items: { id: string; name: string }[] }
      admin('POST', `/api/spaces/${space}/groups`, {
        groupId: list.items.find((g) => g.name === 'Engineering')!.id,
        role: 'editor',
      })
    })
    // Not loaded until asked for.
    expect(m!.requests.some((r) => r.url.includes('/members/effective'))).toBe(false)
    await click(button(tgs.effectiveShow))
    await settle(() => dialog().querySelectorAll('[data-slot="effective-members"] > li').length === 2, 'both people')
    const rows = [...dialog().querySelectorAll('[data-slot="effective-members"] > li')].map(
      (li) => li.textContent ?? '',
    )
    const tang = rows.find((r) => r.includes('名 tang'))!
    expect(tang).toContain(`${tgs.direct}: ${tgs.noDirect}`)
    expect(tang).toContain(`${tgs.viaLabel}: Engineering (${t.roles.editor})`)
    expect(tang).toContain(t.roles.editor)
    await type(dialog().querySelector<HTMLInputElement>(`input[aria-label="${tgs.effectiveSearch}"]`)!, 'tan')
    await settle(() => dialog().querySelectorAll('[data-slot="effective-members"] > li').length === 1, 'filtered')
  })

  it('marks a member who is also in a group, with the role the two give together', async () => {
    await openAsOwner((server, space) => {
      const admin = adminRequests(server)
      admin('POST', `/api/spaces/${space}/members`, { username: 'tang', role: 'viewer' })
      const list = admin('GET', '/api/admin/groups').body as { items: { id: string; name: string }[] }
      admin('POST', `/api/spaces/${space}/groups`, {
        groupId: list.items.find((g) => g.name === 'Engineering')!.id,
        role: 'editor',
      })
    })
    await settle(() => panelText().includes(tgs.viaGroups), 'the mark')
    expect(panelText()).toContain(tgs.effective(t.roles.editor))
  })
})

describe('the groups of a space: without the members permission', () => {
  it('a viewer sees the groups, and cannot change or add any', async () => {
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const { space, owner } = shareSpace(server, 'viewer')
        const g = (
          adminRequests(server)('POST', '/api/admin/groups', { name: 'Engineering', source: 'synced' }).body as {
            id: string
          }
        ).id
        owner.request('POST', `/api/spaces/${space}/groups`, { groupId: g, role: 'editor' })
        localStorage.setItem('pb.space', space)
        return ['/settings/members']
      },
    })
    await settle(() => groupRows().length === 1, 'the group')
    expect(groupRows()[0].textContent).toContain(tg.sourceNames.synced)
    expect(groupRows()[0].querySelector('select')).toBeNull()
    expect(dialog().querySelector('#space-group-search')).toBeNull()
    expect(button(t.fmt.labelled(tgs.remove, 'Engineering'))).toBeNull()
  })

  it('a personal space has no groups', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/settings/members'] })
    await settle(() => panelText().includes(t.members.personalNote), 'the personal space')
    expect(panelText()).not.toContain(tgs.title)
  })
})

describe('my groups', () => {
  it('lists the groups the person is in, by name and kind', async () => {
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        const admin = adminRequests(server)
        const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users[0].id
        const g = (admin('POST', '/api/admin/groups', { name: 'Engineering', source: 'local' }).body as { id: string })
          .id
        admin('POST', `/api/admin/groups/${g}/members`, { userIds: [me] })
        return ['/settings/profile']
      },
    })
    await settle(() => dialog()?.querySelector('[data-slot="my-groups"]') !== null, 'the groups')
    expect(dialog().querySelector('[data-slot="my-groups"]')!.textContent).toBe(
      `Engineering · ${tg.account.source.local}`,
    )
  })

  it('says so when there are none', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/settings/profile'] })
    await settle(() => panelText().includes(tg.account.none), 'the note')
  })
})
