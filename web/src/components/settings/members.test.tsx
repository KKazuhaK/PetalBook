// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { t } from '@/i18n'
import { tp } from '@/i18n/people'
import { MOCK_ADMIN } from '@/api/fixtures/data/people'
import { addPerson, shareSpace } from '@/api/fixtures/data/scenes'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The members section of a space's settings, against the fixture server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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
const button = (label: string) =>
  [...(dialog()?.querySelectorAll('button') ?? [])].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  )
const select = (label: string) => dialog()!.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)
const rows = () => [...dialog()!.querySelectorAll('ul.divide-y > li')].map((li) => li.textContent ?? '')
const sentToServer = () =>
  m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login').map((r) => `${r.method} ${r.url}`)

// The add dialog: it is in a portal, not in the settings dialog.
const adder = () => document.querySelector<HTMLElement>('[data-slot="dialog-content"]')
const adderButton = (label: string) =>
  [...(adder()?.querySelectorAll('button') ?? [])].find((b) => b.textContent === label)

// Adds one person by the picker: opens the dialog, finds them, ticks them, sets the role, adds.
async function addViaPicker(query: string, role?: string) {
  await click(button(tp.add))
  await settle(() => adder() !== null, 'the add dialog')
  await type(adder()!.querySelector<HTMLInputElement>('#add-people-search')!, query)
  await settle(() => adder()!.querySelector('input[type="checkbox"]') !== null, 'a result')
  await click(adder()!.querySelector('input[type="checkbox"]'))
  if (role) await type(adder()!.querySelector<HTMLSelectElement>('#add-people-role')!, role)
  await click(adderButton(tp.addSelected(1)))
  await settle(() => adder() === null, 'the dialog closed')
}

// An action of a row's menu.
async function rowAction(name: string, label: string) {
  await click(button(t.fmt.labelled(tp.list.more, name)))
  await settle(() => document.querySelector('[role="menuitem"]') !== null, 'the menu')
  await click([...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === label))
}

const confirmDemotion = async () => {
  await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
  await click(
    [...document.querySelectorAll('[role="alertdialog"] button')].find(
      (b) => b.textContent === t.members.demoteConfirm,
    ),
  )
}

// The signed-in administrator, owner of 'Team', with another person to add; Team is the space in use.
async function openAsOwner() {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts' },
    entries: (fx) => {
      const space = (fx.call('POST', '/api/spaces', { name: 'Team' }).body as { id: string }).id
      addPerson(fx.data, 'tang')
      localStorage.setItem('pb.space', space)
      return ['/settings/members']
    },
  })
  await settle(() => rows().length > 0 && button(tp.add) !== undefined, 'the members')
}

// Someone else's space, with the signed-in administrator in it as `role`.
async function openAs(role: 'editor' | 'viewer') {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts' },
    entries: (fx) => {
      localStorage.setItem('pb.space', shareSpace(fx, role).space)
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

  it('adds a member from the picker with a role, and the list shows them', async () => {
    await openAsOwner()
    await addViaPicker('tang', 'viewer')
    await settle(() => rows().length === 2, 'the new member')
    const sent = m!.requests.find((r) => r.method === 'POST' && r.url.endsWith('/members/batch'))!
    expect(JSON.parse(sent.body!)).toEqual({ rows: [{ user: 'tang', role: 'viewer' }], dryRun: false })
    expect(rows()[1]).toContain('名 tang')
    expect(select(t.fmt.labelled('名 tang', t.members.role))!.value).toBe('viewer')
  })

  it('finds only people of the tenant, and marks the ones who are in the space already', async () => {
    await openAsOwner()
    await click(button(tp.add))
    await settle(() => adder() !== null, 'the add dialog')
    expect(adder()!.textContent).toContain(tp.tenantOnly)
    await type(adder()!.querySelector<HTMLInputElement>('#add-people-search')!, 'a')
    expect(adder()!.textContent).toContain(tp.minChars)
    await type(adder()!.querySelector<HTMLInputElement>('#add-people-search')!, MOCK_ADMIN.username)
    await settle(() => adder()!.textContent!.includes(tp.alreadyIn), 'the member marked')
    // The picker searches as the space (its tenant's people), not the caller's own tenant.
    expect(m!.requests.some((r) => /\/api\/spaces\/[^/]+\/people\?q=/.test(r.url))).toBe(true)
    expect(adder()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!.disabled).toBe(true)
    // Nothing is picked, so there is nothing to add.
    expect(adderButton(tp.addPeople)!.disabled).toBe(true)
  })

  it('imports a file: checks it row by row first, then adds what can be added', async () => {
    await openAsOwner()
    await click(button(tp.add))
    await settle(() => adder() !== null, 'the add dialog')
    await click([...adder()!.querySelectorAll('[role="tab"]')].find((b) => b.textContent === tp.tabCsv))
    const csv = 'user,role\ntang,viewer\nnobody,editor\nother-person,viewer\n,viewer'
    await type(adder()!.querySelector<HTMLTextAreaElement>('textarea')!, csv)
    await click(adderButton(tp.csv.check))
    await settle(() => adder()!.textContent!.includes(tp.status.not_found), 'the check')
    const text = adder()!.textContent!
    expect(text).toContain(tp.status.ok)
    // Another organization's person is nobody to a space's batch, as with a single add.
    expect(text).not.toContain(tp.status.other_tenant)
    expect(text).toContain(tp.status.invalid)
    // A check writes nothing.
    expect(rows()).toHaveLength(1)
    expect(m!.requests.filter((r) => r.url.endsWith('/members/batch')).map((r) => JSON.parse(r.body!).dryRun)).toEqual([
      true,
    ])
    await click(adderButton(tp.csv.apply(1)))
    await settle(() => adder()!.textContent!.includes(tp.statusDone.ok), 'applied')
    await click(adderButton(tp.done))
    await settle(() => rows().length === 2, 'the new member')
    expect(m!.requests.filter((r) => r.url.endsWith('/members/batch')).map((r) => JSON.parse(r.body!).dryRun)).toEqual([
      true,
      false,
    ])
  })

  it('changes a role; removes a member after asking', async () => {
    await openAsOwner()
    await addViaPicker('tang')
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

    await rowAction('名 tang', t.members.remove)
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(t.members.removeHelp('名 tang'))
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === t.members.removeConfirm,
      ),
    )
    await settle(() => rows().length === 1, 'removed')
    expect(sentToServer().some((r) => r.startsWith('DELETE'))).toBe(true)
  })

  it('will not leave the space without an owner: the refusal is explained', async () => {
    await openAsOwner()
    m!.fx.fail('PATCH', '/api/spaces/:space/members/:user', 409, { code: 'last_owner' }, { times: 1 })
    await type(select(t.fmt.labelled(MOCK_ADMIN.name, t.members.role))!, 'editor')
    await confirmDemotion()
    await settle(() => panelText().includes(t.members.errors.lastOwner), 'last owner')
    // Still the owner: the list was not changed.
    await settle(() => select(t.fmt.labelled(MOCK_ADMIN.name, t.members.role))!.value === 'owner', 'back to owner')
  })

  it('asks before an owner gives up their own ownership, and does nothing if they change their mind', async () => {
    await openAsOwner()
    await addViaPicker('tang', 'owner')
    await settle(() => rows().length === 2, 'a second owner')
    const mine = t.fmt.labelled(MOCK_ADMIN.name, t.members.role)
    await type(select(mine)!, 'editor')
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(t.members.demoteTitle)
    // Nothing has been sent yet; saying no keeps the role.
    expect(sentToServer().some((r) => r.startsWith('PATCH'))).toBe(false)
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find((b) => b.textContent === t.members.cancel),
    )
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

  it("does not ask when someone else's role is changed", async () => {
    await openAsOwner()
    await addViaPicker('tang')
    await settle(() => rows().length === 2, 'added')
    await type(select(t.fmt.labelled('名 tang', t.members.role))!, 'viewer')
    await settle(() => sentToServer().some((r) => r.startsWith('PATCH')), 'the role request')
    expect(document.querySelector('[role="alertdialog"]')).toBeNull()
  })

  it('a personal space is not shared: one line says so, and there is no form to add anyone', async () => {
    m = await mount(routes, { signIn: true, fixtures: { mode: 'accounts' }, entries: ['/settings/members'] })
    await settle(() => panelText().includes(t.members.personalNote), 'the note')
    expect(button(tp.add)).toBeUndefined()
    // And there is no leaving it either.
    expect(button(t.members.leave)).toBeUndefined()
  })
})

describe('the members section: without the members permission', () => {
  it('a viewer sees who is in the space, cannot change anyone, and may leave', async () => {
    await openAs('viewer')
    expect(dialog()!.querySelector('select')).toBeNull()
    expect(button(tp.add)).toBeUndefined()
    expect(panelText()).toContain(t.members.noManage)
    expect(panelText()).toContain(t.roles.viewer)
    expect(panelText()).toContain(t.roles.owner)
    // Nobody to remove: the only menu is the one on the viewer's own row, and it only leaves.
    expect(dialog()!.querySelectorAll('ul.divide-y button')).toHaveLength(1)
    await rowAction(MOCK_ADMIN.name, t.members.leave)
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === t.members.leaveConfirm,
      ),
    )
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
      fixtures: { mode: 'accounts' },
      entries: (fx) => {
        const shared = shareSpace(fx, 'viewer', '停用的')
        shared.data.info.disabled = true
        space = shared.space
        localStorage.setItem('pb.space', space)
        return ['/settings/members']
      },
    })
    await settle(() => panelText().includes(t.members.disabledLeaveHelp), 'the notice')
    expect(rows()).toHaveLength(0)
    await click(button(t.members.leave))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(
      [...document.querySelectorAll('[role="alertdialog"] button')].find(
        (b) => b.textContent === t.members.leaveConfirm,
      ),
    )
    await settle(() => sentToServer().some((r) => r.startsWith('DELETE')), 'leaving')
    expect(sentToServer()[0]).toMatch(/^DELETE \/api\/spaces\/[^/]+\/members\/[^/]+$/)
    // It is gone from the list: the settings are about another space now.
    await settle(() => panelText().includes(t.members.personalNote), 'moved on to the personal space')
  })
})

describe('single-user mode', () => {
  it('has no members section, and the address goes to the preferences', async () => {
    m = await mount(routes, { fixtures: { mode: 'single' }, entries: ['/settings/members'] })
    await settle(() => dialog() !== null && [...dialog()!.querySelectorAll('nav a')].length > 0, 'dialog')
    expect([...dialog()!.querySelectorAll('nav a')].map((a) => a.textContent)).not.toContain(
      t.settingsUi.sections.members,
    )
    await settle(
      () => document.querySelector('[data-testid="location"]')?.textContent === '/settings/preferences',
      'redirected',
    )
  })
})
