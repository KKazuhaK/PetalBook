// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { t } from '@/i18n'
import { tt } from '@/i18n/tenants'
import { adminRequests, click, installDomShims, makePerson, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The administration's Tenants and Settings pages, the administrator kind on a user, and the pages
// a tenant administrator does not get, against the mock server.

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
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const navLabels = () =>
  [...document.querySelectorAll(`nav[aria-label="${tc.nav}"] a`)].map((a) => a.textContent?.trim())
const rowOf = (key: string) => document.querySelector<HTMLElement>(`[data-slot="setting-row"][data-key="${key}"]`)!
const switchOf = (label: string) => document.querySelector<HTMLElement>(`[role="switch"][aria-label="${label}"]`)!

async function open(section: string, scene?: (server: MockServer) => unknown) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const where = scene?.(server)
      // A scene may send the page to one person's own address.
      return [typeof where === 'string' ? where : `/admin/${section}`]
    },
  })
}

describe('the Tenants page', () => {
  it("has the switch off to begin with and no list; switching it on lets a tenant be made, with the first administrator's invite", async () => {
    await open('tenants')
    await settle(() => switchOf(tt.tenancy.switchLabel) !== null, 'the switch')
    expect(switchOf(tt.tenancy.switchLabel).getAttribute('aria-checked')).toBe('false')
    expect(text()).toContain(tt.tenancy.count(1))
    expect(document.querySelector('[data-slot="tenant-list"]')).toBeNull()
    await click(switchOf(tt.tenancy.switchLabel))
    await settle(
      () => switchOf(tt.tenancy.switchLabel).getAttribute('aria-checked') === 'true' && button(tt.list.create) !== null,
      'the switch is on',
    )
    await click(button(tt.list.create))
    await settle(() => dlg()?.textContent?.includes(tt.list.createTitle) === true, 'the form')
    const inputs = dlg().querySelectorAll<HTMLInputElement>('input')
    await type(inputs[0], 'Acme')
    await type(inputs[1], 'acme')
    await type(inputs[2], 'wu')
    await click(button(tt.list.create, dlg()))
    await settle(() => document.querySelector('[data-slot="tenant-invite"]') !== null, 'the invite')
    expect(document.querySelector('[data-slot="tenant-invite"]')!.textContent).toContain(tt.list.created('Acme'))
    expect(m!.requests.find((r) => r.method === 'POST' && r.url === '/api/admin/tenants')!.body).toBe(
      JSON.stringify({ name: 'Acme', slug: 'acme', admin: { username: 'wu' } }),
    )
    await settle(() => document.querySelectorAll('[data-slot="tenant-list"] li').length === 2, 'the list')
    // The default tenant is first and cannot be disabled or deleted.
    const items = [...document.querySelectorAll('[data-slot="tenant-list"] li')]
    expect(items[0].textContent).toContain(tt.list.default)
    await click(button(`${tt.list.manage}: Default`))
    await settle(() => items[0].querySelector('form') !== null, "the default tenant's details")
    expect(button(tt.list.disable, items[0])).toBeNull()
    expect(button(tt.list.delete, items[0])).toBeNull()
  })

  it('renames, disables and deletes a tenant, and says a slug is taken', async () => {
    await open('tenants', (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/tenancy', { enabled: true })
      admin('POST', '/api/admin/tenants', { slug: 'acme', name: 'Acme' })
      admin('POST', '/api/admin/tenants', { slug: 'zed', name: 'Zed' })
    })
    await settle(() => document.querySelectorAll('[data-slot="tenant-list"] li').length === 3, 'the tenants')
    await click(button(`${tt.list.manage}: Acme`))
    await settle(() => button(tt.list.disable) !== null, 'the details')
    const li = [...document.querySelectorAll('[data-slot="tenant-list"] li')].find((x) =>
      x.textContent!.includes('Acme'),
    )!
    // A slug that is taken.
    const slug = li.querySelectorAll<HTMLInputElement>('form input')[1]
    await type(slug, 'zed')
    await click(button(tt.list.save, li))
    await settle(() => li.textContent!.includes(tt.list.errors.exists), 'the slug is taken')
    await type(slug, 'acme-inc')
    await click(button(tt.list.save, li))
    await settle(() => li.textContent!.includes(tt.list.saved), 'saved')
    await click(button(tt.list.disable, li))
    await settle(() => li.textContent!.includes(tt.list.disabled) && button(tt.list.enable, li) !== null, 'disabled')
    await click(button(tt.list.delete, li))
    await settle(() => dlg()?.textContent?.includes(tt.list.deleteTitle('Acme')) === true, 'asked')
    await click(button(tt.list.delete, dlg()))
    await settle(() => document.querySelectorAll('[data-slot="tenant-list"] li').length === 2, 'gone')
  })

  it('nothing on it can be wider than a phone', async () => {
    await open('tenants', (server) => adminRequests(server)('PUT', '/api/admin/tenancy', { enabled: true }))
    await settle(() => button(tt.list.create) !== null, 'the page')
    const has = (el: Element | null, ...c: string[]) => c.every((x) => el?.classList.contains(x))
    // The page column may shrink, rows wrap, and the long help texts break.
    for (const li of document.querySelectorAll('[data-slot="tenant-list"] li > div:first-child'))
      expect(has(li, 'flex-wrap')).toBe(true)
    for (const id of ['tenancy-heading', 'tenants-heading'])
      expect(document.querySelector(`section[aria-labelledby="${id}"]`)!.classList.contains('grid-cols-1')).toBe(true)
    // The rows of the settings wrap as well (see the Settings page).
  })
})

describe('the Settings page', () => {
  it("has offline.copies on both layers, named and explained, and a tenant's switch writes the tenant layer", async () => {
    await open('settings')
    await settle(
      () => document.querySelectorAll('[data-slot="setting-row"][data-key="offline.copies"]').length === 2,
      'the rows',
    )
    const rows = [...document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="offline.copies"]')]
    expect(rows[0].textContent).toContain(tt.settings.names['offline.copies'])
    expect(rows[0].textContent).toContain(tt.settings.helps['offline.copies'])
    expect(rows[0].querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('true')
    expect(tt.settings.helps['offline.copies']).toMatch(/offline_off/)
    await click(rows[1].querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]'))
    await click(rows[1].querySelector(`[role="switch"][aria-label="${tt.settings.names['offline.copies']}"]`))
    await click(button(tt.settings.save, rows[1]))
    await settle(
      () =>
        m!.requests.some(
          (r) => r.method === 'PUT' && r.url === '/api/admin/tenants/0000000000/settings/offline.copies',
        ),
      'saved',
    )
    expect(
      JSON.parse(m!.requests.find((r) => r.url === '/api/admin/tenants/0000000000/settings/offline.copies')!.body!),
    ).toEqual({ value: false })
  })

  it('shows the site layer and the tenant layer with the lock source, keeps a value a lock hides and brings it back', async () => {
    await open('settings', (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/settings/registration.perHour', { lower: { mode: 'restrict', max: 50 } })
    })
    await settle(
      () => rowOf('registration.perHour') !== undefined && rowOf('registration.perHour') !== null,
      'the settings',
    )
    // registration.open is locked for tenants by default: the tenant layer says so and cannot change it.
    await settle(() => document.querySelectorAll('[data-slot="setting-row"]').length >= 16, 'both layers')
    const rows = [...document.querySelectorAll('[data-slot="setting-row"][data-key="registration.open"]')]
    expect(rows).toHaveLength(2)
    expect(rows[1].textContent).toContain(tt.settings.lockedBy.site)
    expect(
      rows[1].querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]')!.hasAttribute('data-disabled'),
    ).toBe(true)
    // The site tightens perHour to 10 after the tenant took 30: kept, marked as not in effect.
    const admin = adminRequests(m!.server)
    admin('PUT', '/api/admin/tenants/0000000000/settings/registration.perHour', { value: 30 })
    admin('PUT', '/api/admin/settings/registration.perHour', { lower: { mode: 'restrict', max: 10 } })
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('[data-slot="not-in-effect"]') !== null, 'the kept value is marked')
    expect(document.querySelector('[data-slot="not-in-effect"]')!.closest('li')!.textContent).toContain(
      tt.settings.keptHint,
    )
    admin('PUT', '/api/admin/settings/registration.perHour', { lower: { mode: 'allow' } })
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('[data-slot="not-in-effect"]') === null, 'it counts again')
  })

  it("saves a rule at the site, says when it makes tenants' values ineffective, and refuses a bad value in words", async () => {
    await open('settings')
    await settle(() => rowOf('registration.perHour') != null, 'the settings')
    // The site layer's perHour row is the first of the two with that key.
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.perHour"]')[0]
    const select = row.querySelector<HTMLSelectElement>(`select[aria-label$="${tt.settings.rule}"]`)!
    await type(select, 'lock')
    await click(button(tt.settings.save, row))
    await settle(() => row.textContent!.includes(tt.settings.saved), 'saved')
    expect(
      m!.requests.find((r) => r.method === 'PUT' && r.url === '/api/admin/settings/registration.perHour')!.body,
    ).toBe(JSON.stringify({ lower: { mode: 'lock' } }))
    // Opening registration is refused while mail is not enabled.
    const open1 = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.open"]')[0]
    await click(open1.querySelector('[role="switch"]'))
    await click(button(tt.settings.save, open1))
    await settle(() => open1.textContent!.includes(tt.settings.errors.mail_not_configured), 'the refusal')
  })

  it('shows where attachments go (separate by default) and the exemption switch, and writes both at the site', async () => {
    await open('settings')
    await settle(() => rowOf('storage.assets') != null, 'the settings')
    const name = tt.settings.names['storage.assets']
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="storage.assets"]')[0]
    expect(row.textContent).toContain(tt.settings.helps['storage.assets'])
    const select = row.querySelector<HTMLSelectElement>(`select[aria-label="${name}"]`)!
    expect(select.value).toBe('repository')
    expect([...select.options].map((o) => o.textContent)).toEqual([
      tt.settings.choices['storage.assets'].space,
      tt.settings.choices['storage.assets'].repository,
    ])
    await type(select, 'space')
    await click(button(tt.settings.save, row))
    await settle(() => row.textContent!.includes(tt.settings.saved), 'saved')
    expect(
      JSON.parse(m!.requests.find((r) => r.method === 'PUT' && r.url === '/api/admin/settings/storage.assets')!.body!),
    ).toEqual({ value: 'space' })
    // The switch is the site's alone, on by default.
    const exempt = tt.settings.names['storage.assets.ownStorageExempt']
    const rows = document.querySelectorAll('[data-slot="setting-row"][data-key="storage.assets.ownStorageExempt"]')
    expect(rows).toHaveLength(1)
    expect(switchOf(exempt).getAttribute('aria-checked')).toBe('true')
    await click(switchOf(exempt))
    await click(button(tt.settings.save, rows[0] as HTMLElement))
    await settle(
      () => m!.requests.some((r) => r.method === 'PUT' && r.url.endsWith('/storage.assets.ownStorageExempt')),
      'saved the switch',
    )
    expect(JSON.parse(m!.requests.find((r) => r.url.endsWith('/storage.assets.ownStorageExempt'))!.body!)).toEqual({
      value: false,
    })
  })

  it('lets a site administrator force where attachments go for one tenant, and shows that it is forced', async () => {
    await open('settings')
    await settle(
      () => document.querySelectorAll('[data-slot="setting-row"][data-key="storage.assets"]').length === 2,
      'both layers',
    )
    const name = tt.settings.names['storage.assets']
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="storage.assets"]')[1]
    // Nothing to force while the tenant follows the site.
    expect(switchOf(`${name}: ${tt.settings.siteLock}`).hasAttribute('data-disabled')).toBe(true)
    await click(switchOf(`${name}: ${tt.settings.follow}`))
    await type(row.querySelector<HTMLSelectElement>(`select[aria-label="${name}"]`)!, 'space')
    await click(switchOf(`${name}: ${tt.settings.siteLock}`))
    await click(button(tt.settings.save, row))
    await settle(
      () => m!.requests.some((r) => r.method === 'PUT' && r.url.endsWith('/settings/storage.assets')),
      'saved',
    )
    expect(
      JSON.parse(
        m!.requests.find(
          (r) => r.method === 'PUT' && r.url === '/api/admin/tenants/0000000000/settings/storage.assets',
        )!.body!,
      ),
    ).toEqual({ value: 'space', siteLock: true })
    await settle(
      () =>
        document
          .querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="storage.assets"]')[1]
          ?.textContent?.includes(tt.settings.siteLockedByYou) === true,
      'the badge',
    )
  })

  it("forces the tenant's saved value as it is: forcing alone names the value and changes nothing the tenant gets", async () => {
    await open('settings')
    await settle(
      () => document.querySelectorAll('[data-slot="setting-row"][data-key="storage.assets"]').length === 2,
      'both layers',
    )
    const name = tt.settings.names['storage.assets']
    const row = () => document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="storage.assets"]')[1]
    const puts = () =>
      m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/tenants/0000000000/settings/storage.assets')
    await click(switchOf(`${name}: ${tt.settings.follow}`))
    await type(row().querySelector<HTMLSelectElement>(`select[aria-label="${name}"]`)!, 'space')
    await click(button(tt.settings.save, row()))
    await settle(() => puts().length === 1, 'the value saved')
    await click(switchOf(`${name}: ${tt.settings.siteLock}`))
    await click(button(tt.settings.save, row()))
    await settle(() => puts().length === 2, 'forced')
    expect(JSON.parse(puts()[1].body!)).toEqual({ value: 'space', siteLock: true })
    const got = await fetch('/api/admin/tenants/0000000000/settings').then((r) => r.json())
    const s = (got.settings as { key: string; effective: unknown }[]).find((x) => x.key === 'storage.assets')!
    expect(s.effective).toBe('space')
  })

  it('keeps a forced value forced when its row is switched to follow: the save names the value in effect', async () => {
    await open('settings', (server) => {
      adminRequests(server)('PUT', '/api/admin/tenants/0000000000/settings/storage.assets', {
        value: 'space',
        siteLock: true,
      })
    })
    await settle(
      () => document.querySelectorAll('[data-slot="setting-row"][data-key="storage.assets"]').length === 2,
      'both layers',
    )
    const name = tt.settings.names['storage.assets']
    const row = () => document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="storage.assets"]')[1]
    const puts = () =>
      m!.requests.filter((r) => r.method === 'PUT' && r.url === '/api/admin/tenants/0000000000/settings/storage.assets')
    await click(switchOf(`${name}: ${tt.settings.follow}`))
    await click(button(tt.settings.save, row()))
    await settle(() => puts().length === 1, 'saved')
    expect(JSON.parse(puts()[0].body!)).toEqual({ value: 'space', siteLock: true })
    const got = await fetch('/api/admin/tenants/0000000000/settings').then((r) => r.json())
    const s = (got.settings as { key: string; effective: unknown; tenant: { siteLocked?: boolean } }[]).find(
      (x) => x.key === 'storage.assets',
    )!
    expect([s.effective, s.tenant.siteLocked]).toEqual(['space', true])
  })

  it('starts the editor of a value not in effect from the value in effect, with nothing to save', async () => {
    await open('settings', (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/tenants/0000000000/settings/registration.perHour', { value: 30 })
      admin('PUT', '/api/admin/settings/registration.perHour', { lower: { mode: 'restrict', max: 10 } })
    })
    await settle(() => document.querySelector('[data-slot="not-in-effect"]') !== null, 'the kept value')
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.perHour"]')[1]
    const name = tt.settings.names['registration.perHour']
    expect(row.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!.value).toBe('10')
    expect(button(tt.settings.save, row)!.disabled).toBe(true)
  })

  it('refuses a site value above its own maximum and says so in the row', async () => {
    await open('settings')
    await settle(() => rowOf('registration.perHour') != null, 'the settings')
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.perHour"]')[0]
    const name = tt.settings.names['registration.perHour']
    // The site's own rule for perHour is at most 50 by default.
    await type(row.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!, '80')
    await click(button(tt.settings.save, row))
    await settle(() => row.textContent!.includes(tt.settings.errors.setting_above_max), 'the refusal')
  })
})

describe('administrators and tenants in the Users page and the navigation', () => {
  it('names the kind of administrator, and a site administrator can make or end another', async () => {
    await open('users', (server) => makePerson(server, 'second', 'admin'))
    await settle(() => document.querySelectorAll('[data-slot="admin-kind"]').length === 2, 'two administrators')
    expect(
      [...document.querySelectorAll('[data-slot="admin-kind"]')].every(
        (b) => b.textContent === tc.users.adminKinds.site,
      ),
    ).toBe(true)
    // The switch is on the person's own page.
    await click([...document.querySelectorAll('tbody a')].find((a) => a.textContent === '名 second'))
    await settle(
      () => document.querySelector(`[role="switch"][aria-label="${tt.siteAdminFor('名 second')}"]`) !== null,
      'the switch',
    )
    const sw = document.querySelector<HTMLElement>(`[role="switch"][aria-label="${tt.siteAdminFor('名 second')}"]`)!
    expect(sw.getAttribute('aria-checked')).toBe('true')
    await click(sw)
    await settle(
      () =>
        document
          .querySelector(`[role="switch"][aria-label="${tt.siteAdminFor('名 second')}"]`)
          ?.getAttribute('aria-checked') === 'false',
      'a tenant administrator',
    )
    expect(m!.requests.find((r) => r.method === 'PATCH' && r.url.startsWith('/api/users/'))!.body).toBe(
      JSON.stringify({ siteAdmin: false }),
    )
  })

  it('shows a site administrator every section, and a tenant administrator only those of their tenant', async () => {
    await open('users')
    await settle(() => navLabels().length > 0, 'the navigation')
    expect(navLabels()).toEqual([
      tc.sections.users,
      tc.sections.groups,
      tc.sections.spaces,
      tc.sections.storage,
      tc.sections.security,
      tc.sections.mail,
      tc.sections.daily,
      tc.sections.settings,
      tc.sections.tenants,
      tc.sections.audit,
      tc.sections.status,
    ])
    await m!.unmount()
    // Another site administrator exists; the signed-in one is made a tenant administrator.
    await open('users', (server) => {
      const admin = adminRequests(server)
      const other = makePerson(server, 'second', 'admin')
      const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
        (u) => u.username === 'admin',
      )!
      expect(other.id).not.toBe(me.id)
      admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
      // The server's GitHub App is site-only, as on the server.
      expect(() => admin('GET', '/api/admin/connections/github')).toThrow(/403/)
    })
    await settle(() => navLabels().length > 0, 'the navigation')
    expect(navLabels()).toEqual([
      tc.sections.users,
      tc.sections.groups,
      tc.sections.spaces,
      tc.sections.storage,
      tc.sections.settings,
      tc.sections.audit,
    ])
    expect(t.appName).not.toBe('')
  })

  it("the Users page of a tenant administrator never asks for the server's security settings", async () => {
    let second = ''
    await open('users', (server) => {
      const admin = adminRequests(server)
      second = makePerson(server, 'second', 'admin').id
      const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find(
        (u) => u.username === 'admin',
      )!
      admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
      return `/admin/user/${second}/security`
    })
    await settle(() => document.querySelector('[data-slot="person-security"]') !== null, 'the page')
    expect(m!.requests.filter((r) => r.url.startsWith('/api/admin/security'))).toEqual([])
    await m!.unmount()
    // A site administrator's does: it holds the roles a passkey is required of.
    await open('users', (server) => `/admin/user/${makePerson(server, 'second', 'admin').id}/security`)
    await settle(
      () => m!.requests.some((r) => r.method === 'GET' && r.url.startsWith('/api/admin/security')),
      'the security settings',
    )
  })
})
