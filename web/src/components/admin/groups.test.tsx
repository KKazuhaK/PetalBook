// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import type { FixtureServer } from '@/api/fixtures/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tg } from '@/i18n/groups'
import { tp } from '@/i18n/people'
import { tsec } from '@/i18n/security'
import { byLabel, click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addGroup, addPerson, addProvider } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The administration's Groups page and the groups mapping of a sign-in provider, against the fixture server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const ta = tg.admin
const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-group]')]
const rowOf = (name: string) => rows().find((r) => r.querySelector('th')?.textContent?.includes(name))!
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null
// Whether a person is among the group's members: a row of the list of people in the group's own dialog (the add
// dialog, opened over it, lists people too).
const isMember = (name: string) =>
  [...dlg().querySelectorAll('section[aria-labelledby="group-people-heading"] ul li')].some((li) =>
    li.textContent?.includes(name),
  )
// The add dialog is the last dialog on the page.
const adder = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"]')].at(-1)!
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const sent = () =>
  m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login').map((r) => `${r.method} ${r.url}`)

async function openPage(scene?: (d: FixtureData) => unknown, setup?: (fx: FixtureServer) => void) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void scene?.(d) },
    entries: (fx) => {
      setup?.(fx)
      return ['/admin/groups']
    },
  })
  await settle(() => text().includes(ta.empty) || rows().length > 0, 'the groups page')
}

async function make(name: string, source: 'local' | 'synced') {
  await click(button(ta.create))
  await settle(() => dlg()?.textContent?.includes(ta.createTitle) === true, 'the form')
  await type(dlg().querySelector<HTMLInputElement>('input[maxlength="80"]')!, name)
  await type(dlg().querySelector<HTMLSelectElement>('#group-source')!, source)
  await click(button(ta.create, dlg()))
  await settle(
    () =>
      dlg()?.textContent?.includes(tg.sourceNames[source]) === true &&
      dlg().textContent?.includes(ta.peopleTitle) === true,
    'the new group',
  )
}

describe('the Groups page', () => {
  it('is in the administration, starts empty, and makes a local group that opens at once', async () => {
    await openPage()
    expect(text()).toContain(ta.empty)
    await make('Engineering', 'local')
    expect(m!.requests.find((r) => r.method === 'POST' && r.url === '/api/admin/groups')!.body).toBe(
      JSON.stringify({ name: 'Engineering', description: '', source: 'local' }),
    )
    await settle(() => dlg().textContent!.includes(ta.noPeople), 'no people yet')
    // A local group has the button for adding people.
    expect(button(tp.add, dlg())).not.toBeNull()
    await click(button(ta.close, dlg()))
    await settle(() => rows().length === 1, 'the list')
    expect(rowOf('Engineering').textContent).toContain(tg.sourceNames.local)
  })

  it('asks for a name, and says when the name is taken (ignoring case)', async () => {
    // The server refuses a name that is taken, whatever the case.
    await openPage(
      (d) => addGroup(d, 'Staff'),
      (fx) => fx.fail('POST', '/api/admin/groups', 409, { code: 'exists' }),
    )
    await click(button(ta.create))
    await settle(() => dlg()?.textContent?.includes(ta.createTitle) === true, 'the form')
    await click(button(ta.create, dlg()))
    await settle(() => dlg().textContent!.includes(ta.nameRequired), 'the name is required')
    await type(dlg().querySelector<HTMLInputElement>('input[maxlength="80"]')!, 'STAFF')
    await click(button(ta.create, dlg()))
    await settle(() => dlg().textContent!.includes(ta.errors.exists), 'the name is taken')
  })

  it('filters by name and by source', async () => {
    await openPage((d) => {
      addGroup(d, 'Alpha')
      addGroup(d, 'Beta', 'synced')
    })
    await settle(() => rows().length === 2, 'both groups')
    await type(document.querySelector<HTMLSelectElement>(`select[aria-label="${ta.source}"]`)!, 'synced')
    await settle(() => rows().length === 1 && rows()[0].textContent!.includes('Beta'), 'only the synced one')
    await type(document.querySelector<HTMLSelectElement>(`select[aria-label="${ta.source}"]`)!, '')
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${ta.search}"]`)!, 'alp')
    await settle(() => rows().length === 1 && rows()[0].textContent!.includes('Alpha'), 'only Alpha')
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${ta.search}"]`)!, 'zzz')
    await settle(() => text().includes(ta.noMatch('zzz')), 'no match')
  })

  it('adds people by picking them and from a file, checked first, and takes one out', async () => {
    await openPage(
      (d) => {
        addPerson(d, 'tang')
        addPerson(d, 'wu')
        addGroup(d, 'Eng')
      },
      // The picker searches the people of the tenant: the one that matches.
      (fx) => {
        const tang = fx.data.auth.users.find((u) => u.username === 'tang')!
        fx.reply('GET', '/api/people', { people: [{ id: tang.id, username: tang.username, name: tang.name }] })
      },
    )
    await settle(() => rows().length === 1, 'the group')
    await click(button(t_open('Eng')))
    await settle(() => button(tp.add, dlg()) !== null, 'the group opens')
    // Picked from a search of the tenant's people.
    await click(button(tp.add, dlg()))
    await settle(() => document.querySelectorAll('[role="dialog"]').length === 2, 'the add dialog')
    await type(adder().querySelector<HTMLInputElement>('#add-people-search')!, 'tang')
    const boxes = () => [...adder().querySelectorAll<HTMLInputElement>('input[type="checkbox"]')]
    await settle(() => boxes().length === 1, 'the match')
    await click(boxes()[0])
    await click(button(tp.addSelected(1), adder()))
    await settle(() => isMember('名 tang'), 'tang is in')
    expect(sent().filter((r) => r.endsWith('/members/batch'))).toHaveLength(1)
    expect(sent().some((r) => /\/members$/.test(r))).toBe(false)
    // From a file: the one who is in already and the one nobody has are told, not sent.
    await click(button(tp.add, dlg()))
    await settle(() => document.querySelectorAll('[role="dialog"]').length === 2, 'the add dialog again')
    await click([...adder().querySelectorAll('[role="tab"]')].find((b) => b.textContent === tp.tabCsv))
    await type(adder().querySelector<HTMLTextAreaElement>('textarea')!, 'wu\ntang\nnobody')
    await click(button(tp.csv.check, adder()))
    await settle(() => adder().textContent!.includes(tp.status.not_found), 'the check')
    expect(adder().textContent).toContain(tp.status.already_member)
    expect(isMember('名 wu')).toBe(false)
    await click(button(tp.csv.apply(1), adder()))
    await settle(() => adder().textContent!.includes(tp.statusDone.ok), 'applied')
    await click(button(tp.done, adder()))
    await settle(() => isMember('名 wu'), 'wu is in')
    // Taken out, from the row's menu.
    await click(button(t.fmt.labelled(tp.list.more, '名 tang'), dlg()))
    await settle(() => document.querySelector('[role="menuitem"]') !== null, 'the menu')
    await click(
      [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === ta.removePerson),
    )
    await settle(() => !isMember('名 tang'), 'tang is out')
  })

  it("shows a synced group's people without a way to change them, and where each came from", async () => {
    await openPage((d) => addGroup(d, 'Staff', 'synced'))
    await settle(() => rows().length === 1, 'the group')
    await click(button(t_open('Staff')))
    await settle(() => dlg().textContent!.includes(ta.syncedPeopleNote), 'the note')
    expect(button(tp.add, dlg())).toBeNull()
  })

  it('lists the spaces a group is in, and deleting one that is in use needs a second, forced, answer', async () => {
    await openPage(
      (d) => addGroup(d, 'Eng', 'local', { spaceRefs: [{ id: '01FSHARE1', name: 'Team', role: 'editor' }] }),
      // The server refuses to delete a group that is in a space, unless forced.
      (fx) =>
        fx.fail('DELETE', '/api/admin/groups/:id', 409, { code: 'group_in_use', spaces: 1, mappings: 0 }, { times: 1 }),
    )
    await settle(() => rows().length === 1, 'the group')
    expect(rowOf('Eng').textContent).toContain('1')
    await click(button(t_open('Eng')))
    await settle(() => dlg().textContent!.includes('Team'), 'its space')
    await click(button(ta.delete, dlg()))
    await settle(() => document.querySelectorAll('[role="dialog"]').length === 2, 'the question')
    const ask = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"]')].at(-1)!
    await click(button(ta.deleteConfirm, ask()))
    await settle(() => ask().textContent!.includes(ta.deleteInUse(1, 0)), 'it is in use')
    await click(button(ta.deleteForce, ask()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'gone')
    expect(sent().at(-1)).toMatch(/^DELETE \/api\/admin\/groups\/.+\?force=1$/)
    await settle(() => text().includes(ta.empty), 'the list is empty')
  })
})

const t_open = (name: string) => t.fmt.labelled(ta.open, name)

describe('the groups mapping of a sign-in provider', () => {
  const openProviders = async () => {
    m = await mount(routes, {
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          addProvider(d, 'company', 'Company SSO')
          addGroup(d, 'Staff', 'synced')
          addGroup(d, 'Local one')
        },
      },
      entries: ['/admin/security/providers'],
    })
    await settle(() => text().includes('Company SSO'), 'the providers')
    await click(byLabel(tsec.providers.editFor('Company SSO')))
    await settle(() => document.querySelector('[data-slot="groups-fields"]') !== null, 'the form')
  }

  it('maps claim values to synced groups only, and saves what changed', async () => {
    await openProviders()
    const fields = () => document.querySelector<HTMLElement>('[data-slot="groups-fields"]')!
    await click(button(tg.provider.addRow, fields()))
    await type(fields().querySelector<HTMLInputElement>('#groups-value-0')!, '/staff')
    // Only synced groups are offered.
    const options = [...fields().querySelectorAll<HTMLOptionElement>('#groups-group-0 option')].map(
      (o) => o.textContent,
    )
    await settle(
      () =>
        [...fields().querySelectorAll<HTMLOptionElement>('#groups-group-0 option')].some(
          (o) => o.textContent === 'Staff',
        ),
      'the synced group is offered',
    )
    expect(options).not.toContain('Local one')
    const select = fields().querySelector<HTMLSelectElement>('#groups-group-0')!
    await type(select, [...select.options].find((o) => o.textContent === 'Staff')!.value)
    await click(fields().querySelector('[role="switch"]'))
    await click(button(tsec.providers.save, document.querySelector('[data-slot="provider-form"]')!))
    await settle(
      () =>
        m!.requests.some((r) => r.method === 'PATCH' && r.url === '/api/admin/auth/providers/company') ||
        document.querySelector('[role="alert"]') !== null,
      'saved',
    )
    expect(document.querySelector('[role="alert"]')?.textContent ?? '').toBe('')
    const body = JSON.parse(m!.requests.find((r) => r.method === 'PATCH')!.body!) as {
      groups: { enabled: boolean; map: { value: string }[] }
      oidc?: unknown
    }
    expect(body.groups.enabled).toBe(true)
    expect(body.groups.map).toEqual([{ value: '/staff', groupId: expect.any(String) }])
    expect(body.oidc).toBeUndefined()
  })

  it('finds a synced group past the first 200 by its name', async () => {
    m = await mount(routes, {
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          addProvider(d, 'company', 'Company SSO')
          for (let i = 0; i < 200; i++) addGroup(d, `A${String(i).padStart(3, '0')}`, 'synced')
          addGroup(d, 'Staff', 'synced')
        },
      },
      entries: ['/admin/security/providers'],
    })
    await settle(() => text().includes('Company SSO'), 'the providers')
    await click(byLabel(tsec.providers.editFor('Company SSO')))
    await settle(() => document.querySelector('[data-slot="groups-fields"]') !== null, 'the form')
    const fields = () => document.querySelector<HTMLElement>('[data-slot="groups-fields"]')!
    await click(button(tg.provider.addRow, fields()))
    const offered = () =>
      [...fields().querySelectorAll<HTMLOptionElement>('#groups-group-0 option')].map((o) => o.textContent)
    await settle(() => offered().includes('A000'), 'the first page')
    expect(offered()).not.toContain('Staff')
    await type(byLabel(tg.provider.findGroup) as HTMLInputElement, 'staf')
    await settle(() => offered().includes('Staff'), 'the search finds it')
    const select = fields().querySelector<HTMLSelectElement>('#groups-group-0')!
    await type(select, [...select.options].find((o) => o.textContent === 'Staff')!.value)
    // Its name stays once the search changes.
    await type(byLabel(tg.provider.findGroup) as HTMLInputElement, 'A00')
    await settle(() => offered().includes('A001'), 'another search')
    expect(offered()).toContain('Staff')
  })
})
