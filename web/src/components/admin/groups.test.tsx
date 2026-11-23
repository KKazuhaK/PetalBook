// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tg } from '@/i18n/groups'
import { tsec } from '@/i18n/security'
import { adminRequests, byLabel, click, installDomShims, makePerson, mount, settle, shareSpace, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The administration's Groups page and the groups mapping of a sign-in provider, against the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const ta = tg.admin
const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/:section?" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr[data-group]')]
const rowOf = (name: string) => rows().find((r) => r.querySelector('th')?.textContent?.includes(name))!
const button = (label: string, root: ParentNode = document) => [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label) ?? null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const sent = () => m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login').map((r) => `${r.method} ${r.url}`)

async function openPage(scene?: (server: MockServer) => unknown) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      scene?.(server)
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
  await settle(() => dlg()?.textContent?.includes(tg.sourceNames[source]) === true && dlg().textContent?.includes(ta.peopleTitle) === true, 'the new group')
}

describe('the Groups page', () => {
  it('is in the administration, starts empty, and makes a local group that opens at once', async () => {
    await openPage()
    expect(text()).toContain(ta.empty)
    await make('Engineering', 'local')
    expect(m!.requests.find((r) => r.method === 'POST' && r.url === '/api/admin/groups')!.body).toBe(JSON.stringify({ name: 'Engineering', description: '', source: 'local' }))
    await settle(() => dlg().textContent!.includes(ta.noPeople), 'no people yet')
    // A local group has the form for adding people.
    expect(dlg().querySelector('[data-slot="add-people"]')).not.toBeNull()
    await click(button(ta.close, dlg()))
    await settle(() => rows().length === 1, 'the list')
    expect(rowOf('Engineering').textContent).toContain(tg.sourceNames.local)
  })

  it('asks for a name, and says when the name is taken (ignoring case)', async () => {
    await openPage((server) => adminRequests(server)('POST', '/api/admin/groups', { name: 'Staff', source: 'local' }))
    await click(button(ta.create))
    await settle(() => dlg()?.textContent?.includes(ta.createTitle) === true, 'the form')
    await click(button(ta.create, dlg()))
    await settle(() => dlg().textContent!.includes(ta.nameRequired), 'the name is required')
    await type(dlg().querySelector<HTMLInputElement>('input[maxlength="80"]')!, 'STAFF')
    await click(button(ta.create, dlg()))
    await settle(() => dlg().textContent!.includes(ta.errors.exists), 'the name is taken')
  })

  it('filters by name and by source', async () => {
    await openPage((server) => {
      const admin = adminRequests(server)
      admin('POST', '/api/admin/groups', { name: 'Alpha', source: 'local' })
      admin('POST', '/api/admin/groups', { name: 'Beta', source: 'synced' })
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

  it('adds people by picking them and by pasting usernames, all or nothing, and takes one out', async () => {
    await openPage((server) => {
      makePerson(server as never, 'tang')
      makePerson(server as never, 'wu')
      adminRequests(server)('POST', '/api/admin/groups', { name: 'Eng', source: 'local' })
    })
    await settle(() => rows().length === 1, 'the group')
    await click(button(t_open('Eng')))
    await settle(() => dlg().querySelector('[data-slot="add-people"]') !== null, 'the group opens')
    const form = () => dlg().querySelector<HTMLElement>('[data-slot="add-people"]')!
    // Picked from a search.
    await type(form().querySelector<HTMLInputElement>('#group-pick-search')!, 'tang')
    await settle(() => form().querySelector('input[type="checkbox"]') !== null, 'the match')
    await click(form().querySelector('input[type="checkbox"]'))
    await click(button(ta.addSelected(1), form()))
    await settle(() => dlg().textContent!.includes('名 tang'), 'tang is in')
    expect(sent().at(-1)).toMatch(/^POST \/api\/admin\/groups\/.+\/members$/)
    // Pasted, with a name that is no account: nothing is sent for it.
    await type(form().querySelector<HTMLTextAreaElement>('#group-paste')!, 'wu\nnobody')
    await settle(() => form().textContent!.includes(ta.pasteUnknown('nobody')), 'the unknown name is said')
    expect(button(ta.pasteAdd, form())!.disabled).toBe(true)
    await type(form().querySelector<HTMLTextAreaElement>('#group-paste')!, 'WU\ntang')
    await settle(() => !button(ta.pasteAdd, form())!.disabled, 'ready')
    const before = sent().length
    await click(button(ta.pasteAdd, form()))
    await settle(() => sent().length === before + 1 && dlg().textContent!.includes('名 wu'), 'wu is in')
    const body = JSON.parse(m!.requests.filter((r) => r.method === 'POST' && r.url.endsWith('/members')).at(-1)!.body!) as { userIds: string[] }
    expect(body.userIds).toHaveLength(2) // tang was in already: the server says so, not the page
    // Taken out.
    await click(button(ta.removePersonLabel('名 tang'), dlg()))
    await settle(() => !dlg().textContent!.includes('名 tang'), 'tang is out')
  })

  it('shows a synced group\'s people without a way to change them, and where each came from', async () => {
    await openPage((server) => adminRequests(server)('POST', '/api/admin/groups', { name: 'Staff', source: 'synced' }))
    await settle(() => rows().length === 1, 'the group')
    await click(button(t_open('Staff')))
    await settle(() => dlg().textContent!.includes(ta.syncedPeopleNote), 'the note')
    expect(dlg().querySelector('[data-slot="add-people"]')).toBeNull()
  })

  it('lists the spaces a group is in, and deleting one that is in use needs a second, forced, answer', async () => {
    await openPage((server) => {
      const admin = adminRequests(server)
      const g = (admin('POST', '/api/admin/groups', { name: 'Eng', source: 'local' }).body as { id: string }).id
      const { space } = shareSpace(server as never, 'owner', 'Team')
      admin('POST', `/api/spaces/${space}/groups`, { groupId: g, role: 'editor' })
    })
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
      server: { mode: 'accounts', providers: [{ slug: 'company', kind: 'oidc', name: 'Company SSO', oidc: { issuer: 'https://sso.example', clientId: 'x' } }] },
      entries: (server) => {
        const admin = adminRequests(server)
        admin('POST', '/api/admin/groups', { name: 'Staff', source: 'synced' })
        admin('POST', '/api/admin/groups', { name: 'Local one', source: 'local' })
        return ['/admin/security']
      },
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
    const options = [...fields().querySelectorAll<HTMLOptionElement>('#groups-group-0 option')].map((o) => o.textContent)
    await settle(() => [...fields().querySelectorAll<HTMLOptionElement>('#groups-group-0 option')].some((o) => o.textContent === 'Staff'), 'the synced group is offered')
    expect(options).not.toContain('Local one')
    const select = fields().querySelector<HTMLSelectElement>('#groups-group-0')!
    await type(select, [...select.options].find((o) => o.textContent === 'Staff')!.value)
    await click(fields().querySelector('[role="switch"]'))
    await click(button(tsec.providers.save, document.querySelector('[data-slot="provider-form"]')!))
    await settle(() => m!.requests.some((r) => r.method === 'PATCH' && r.url === '/api/admin/auth/providers/company') || document.querySelector('[role="alert"]') !== null, 'saved')
    expect(document.querySelector('[role="alert"]')?.textContent ?? '').toBe('')
    const body = JSON.parse(m!.requests.find((r) => r.method === 'PATCH')!.body!) as { groups: { enabled: boolean; map: { value: string }[] }; oidc?: unknown }
    expect(body.groups.enabled).toBe(true)
    expect(body.groups.map).toEqual([{ value: '/staff', groupId: expect.any(String) }])
    expect(body.oidc).toBeUndefined()
  })

  it('finds a synced group past the first 200 by its name', async () => {
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts', providers: [{ slug: 'company', kind: 'oidc', name: 'Company SSO', oidc: { issuer: 'https://sso.example', clientId: 'x' } }] },
      entries: (server) => {
        const admin = adminRequests(server)
        for (let i = 0; i < 200; i++) admin('POST', '/api/admin/groups', { name: `A${String(i).padStart(3, '0')}`, source: 'synced' })
        admin('POST', '/api/admin/groups', { name: 'Staff', source: 'synced' })
        return ['/admin/security']
      },
    })
    await settle(() => text().includes('Company SSO'), 'the providers')
    await click(byLabel(tsec.providers.editFor('Company SSO')))
    await settle(() => document.querySelector('[data-slot="groups-fields"]') !== null, 'the form')
    const fields = () => document.querySelector<HTMLElement>('[data-slot="groups-fields"]')!
    await click(button(tg.provider.addRow, fields()))
    const offered = () => [...fields().querySelectorAll<HTMLOptionElement>('#groups-group-0 option')].map((o) => o.textContent)
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
