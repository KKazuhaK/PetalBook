// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { t } from '@/i18n'
import { MOCK_ADMIN } from '@/api/mock/server'
import { adminRequests, click, installDomShims, makePerson, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The members section of a space's settings, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const panelText = () => dialog()?.textContent ?? ''
const button = (label: string) => [...(dialog()?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label)
const select = (label: string) => dialog()!.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)
const rows = () => [...dialog()!.querySelectorAll('ul.divide-y > li')].map((li) => li.textContent ?? '')
const sentToServer = () => m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login').map((r) => `${r.method} ${r.url}`)

const confirmDemotion = async () => {
  await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
  await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.members.demoteConfirm))
}

// The signed-in administrator, owner of 'Team', with another person to add; Team is the space in use.
async function openAsOwner() {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const space = (adminRequests(server)('POST', '/api/spaces', { name: 'Team' }).body as { id: string }).id
      makePerson(server, 'tang')
      localStorage.setItem('pb.space', space)
      return ['/settings/members']
    },
  })
  await settle(() => panelText().includes(t.members.addTitle), 'the members')
}

// Someone else's space, with the signed-in administrator in it as `role`.
async function openAs(role: 'editor' | 'viewer') {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      localStorage.setItem('pb.space', shareSpace(server, role).space)
      return ['/settings/members']
    },
  })
  await settle(() => rows().length === 2, 'the members')
}

describe('the members section: an owner', () => {
  it('lists the members, owners first, marking the one who is looking', async () => {
    await openAsOwner()
    expect(rows()).toHaveLength(1)
    expect(rows()[0]).toContain(MOCK_ADMIN.name)
    expect(rows()[0]).toContain(t.members.you)
    expect(select(t.fmt.labelled(MOCK_ADMIN.name, t.members.role))!.value).toBe('owner')
  })

  it('adds a member by username with a role, and the list shows them', async () => {
    await openAsOwner()
    await type(dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!, ' Tang ')
    await type(dialog()!.querySelector<HTMLSelectElement>('#add-member-role')!, 'viewer')
    await click(button(t.members.add))
    await settle(() => rows().length === 2, 'the new member')
    const sent = m!.requests.find((r) => r.method === 'POST' && r.url.endsWith('/members'))!
    expect(JSON.parse(sent.body!)).toEqual({ username: 'Tang', role: 'viewer' })
    expect(rows()[1]).toContain('名 tang')
    expect(select(t.fmt.labelled('名 tang', t.members.role))!.value).toBe('viewer')
    // The box is empty again for the next one.
    expect(dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!.value).toBe('')
  })

  it('says in plain words when there is no such user, and when they are in the space already', async () => {
    await openAsOwner()
    const field = () => dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!
    await click(button(t.members.add))
    await settle(() => panelText().includes(t.members.usernameRequired), 'empty username')
    await type(field(), 'nobody')
    await click(button(t.members.add))
    await settle(() => panelText().includes(t.members.errors.userNotFound), 'no such user')
    await type(field(), 'tang')
    await click(button(t.members.add))
    await settle(() => rows().length === 2, 'added')
    await type(field(), 'tang')
    await click(button(t.members.add))
    await settle(() => panelText().includes(t.members.errors.exists), 'already a member')
  })

  it('changes a role; removes a member after asking', async () => {
    await openAsOwner()
    await type(dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!, 'tang')
    await click(button(t.members.add))
    await settle(() => rows().length === 2, 'added')
    const roleOfTang = t.fmt.labelled('名 tang', t.members.role)
    // Added as an editor, the default.
    expect(select(roleOfTang)!.value).toBe('editor')
    await type(select(roleOfTang)!, 'viewer')
    await settle(() => sentToServer().some((r) => r.startsWith('PATCH')), 'the role request')
    const patch = m!.requests.find((r) => r.method === 'PATCH')!
    expect(patch.url).toMatch(/\/members\/[^/]+$/)
    expect(JSON.parse(patch.body!)).toEqual({ role: 'viewer' })
    await settle(() => select(roleOfTang)?.value === 'viewer', 'the new role shown')

    await click(button(t.fmt.labelled(t.members.remove, '名 tang')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(t.members.removeHelp('名 tang'))
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.members.removeConfirm))
    await settle(() => rows().length === 1, 'removed')
    expect(sentToServer().some((r) => r.startsWith('DELETE'))).toBe(true)
  })

  it('will not leave the space without an owner: the refusal is explained', async () => {
    await openAsOwner()
    await type(select(t.fmt.labelled(MOCK_ADMIN.name, t.members.role))!, 'editor')
    await confirmDemotion()
    await settle(() => panelText().includes(t.members.errors.lastOwner), 'last owner')
    // Still the owner: the list was not changed.
    await settle(() => select(t.fmt.labelled(MOCK_ADMIN.name, t.members.role))!.value === 'owner', 'back to owner')
  })

  it('asks before an owner gives up their own ownership, and does nothing if they change their mind', async () => {
    await openAsOwner()
    await type(dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!, 'tang')
    await type(dialog()!.querySelector<HTMLSelectElement>('#add-member-role')!, 'owner')
    await click(button(t.members.add))
    await settle(() => rows().length === 2, 'a second owner')
    const mine = t.fmt.labelled(MOCK_ADMIN.name, t.members.role)
    await type(select(mine)!, 'editor')
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(t.members.demoteTitle)
    // Nothing has been sent yet; saying no keeps the role.
    expect(sentToServer().some((r) => r.startsWith('PATCH'))).toBe(false)
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.members.cancel))
    await settle(() => document.querySelector('[role="alertdialog"]') === null, 'the question gone')
    expect(select(mine)!.value).toBe('owner')
    expect(sentToServer().some((r) => r.startsWith('PATCH'))).toBe(false)
    // Saying yes changes it.
    await type(select(mine)!, 'editor')
    await confirmDemotion()
    // An editor no longer changes members: the list is read-only now.
    await settle(() => panelText().includes(t.members.noManage), 'the new role')
    expect(sentToServer().filter((r) => r.startsWith('PATCH'))).toHaveLength(1)
  })

  it('does not ask when someone else\'s role is changed', async () => {
    await openAsOwner()
    await type(dialog()!.querySelector<HTMLInputElement>('input[autocomplete="off"]')!, 'tang')
    await click(button(t.members.add))
    await settle(() => rows().length === 2, 'added')
    await type(select(t.fmt.labelled('名 tang', t.members.role))!, 'viewer')
    await settle(() => sentToServer().some((r) => r.startsWith('PATCH')), 'the role request')
    expect(document.querySelector('[role="alertdialog"]')).toBeNull()
  })

  it('a personal space is not shared: one line says so, and there is no form to add anyone', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/settings/members'] })
    await settle(() => panelText().includes(t.members.personalNote), 'the note')
    expect(dialog()!.querySelector('#add-member-role')).toBeNull()
    expect(button(t.members.add)).toBeUndefined()
    // And there is no leaving it either.
    expect(button(t.members.leave)).toBeUndefined()
  })
})

describe('the members section: without the members permission', () => {
  it('a viewer sees who is in the space, cannot change anyone, and may leave', async () => {
    await openAs('viewer')
    expect(dialog()!.querySelector('select')).toBeNull()
    expect(dialog()!.querySelector('#add-member-role')).toBeNull()
    expect(panelText()).toContain(t.members.noManage)
    expect(panelText()).toContain(t.roles.viewer)
    expect(panelText()).toContain(t.roles.owner)
    // Nobody to remove: the only button is the one for leaving, on the viewer's own row.
    const buttons = [...dialog()!.querySelectorAll('ul.divide-y button')].map((b) => b.textContent)
    expect(buttons).toEqual([t.members.leave])
    await click(button(t.members.leave))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.members.leaveConfirm))
    await settle(() => sentToServer().some((r) => r.startsWith('DELETE')), 'leaving')
    expect(sentToServer()[0]).toMatch(/^DELETE \/api\/spaces\/[^/]+\/members\/[^/]+$/)
    // The space is gone from the list, so the settings are about another one now.
    await settle(() => panelText().includes(t.members.personalNote), 'moved on to the personal space')
  })

  it('an editor is in the same place: the people, and no way to change them', async () => {
    await openAs('editor')
    expect(dialog()!.querySelector('select')).toBeNull()
    expect(panelText()).toContain(t.members.noManage)
  })
})

describe('a space that an administrator switched off', () => {
  it('cannot show its members, but a member may still leave it', async () => {
    let space = ''
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts' },
      entries: (server) => {
        space = shareSpace(server, 'viewer', '停用的').space
        adminRequests(server)('POST', `/api/admin/spaces/${space}/disable`)
        localStorage.setItem('pb.space', space)
        return ['/settings/members']
      },
    })
    await settle(() => panelText().includes(t.members.disabledLeaveHelp), 'the notice')
    expect(rows()).toHaveLength(0)
    await click(button(t.members.leave))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click([...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.members.leaveConfirm))
    await settle(() => sentToServer().some((r) => r.startsWith('DELETE')), 'leaving')
    expect(sentToServer()[0]).toMatch(/^DELETE \/api\/spaces\/[^/]+\/members\/[^/]+$/)
    // It is gone from the list: the settings are about another space now.
    await settle(() => panelText().includes(t.members.personalNote), 'moved on to the personal space')
  })
})

describe('single-user mode', () => {
  it('has no members section, and the address goes to the preferences', async () => {
    m = await mount(routes, { server: { mode: 'single' }, entries: ['/settings/members'] })
    await settle(() => dialog() !== null && [...dialog()!.querySelectorAll('nav a')].length > 0, 'dialog')
    expect([...dialog()!.querySelectorAll('nav a')].map((a) => a.textContent)).not.toContain(t.settingsUi.sections.members)
    await settle(() => document.querySelector('[data-testid="location"]')?.textContent === '/settings/preferences', 'redirected')
  })
})
