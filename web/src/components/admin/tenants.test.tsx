// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { keys } from '@/api/hooks'
import { tc } from '@/i18n/console'
import { t } from '@/i18n'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, mount, settle, type, type FxMounted, openTenantPage } from '@/testing/harness'
import { addPerson, addTenant, asTenantAdmin, ownRule, setting, setSite, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The administration's Tenants and Settings pages, the administrator kind on a user, and the pages
// a tenant administrator does not get, against the fixture server. What a lock or a maximum does to a value is the
// server's to work out: a scene puts the rules as the server would list them.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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

async function open(section: string, scene?: (d: FixtureData) => unknown, setup?: (fx: FxMounted['fx']) => void) {
  let where: unknown
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void (where = scene?.(d)) },
    // A scene may send the page to one person's own address.
    entries: (fx) => {
      setup?.(fx)
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
    await openTenantPage(`${tt.list.open}: Default`, tt.detail.tabs.danger)
    await settle(() => document.querySelector('main form') !== null, "the default tenant's details")
    expect(button(tt.list.disable)).toBeNull()
    expect(button(tt.list.delete)).toBeNull()
  })

  it('renames, disables and deletes a tenant, and says a slug is taken', async () => {
    // The server refuses a slug that another tenant has.
    await open(
      'tenants',
      (d) => {
        tenancyOn(d)
        addTenant(d, 'acme', 'Acme')
        addTenant(d, 'zed', 'Zed')
      },
      (fx) => fx.fail('PATCH', '/api/admin/tenants/:id', 409, { code: 'exists' }, { times: 1 }),
    )
    await settle(() => document.querySelectorAll('[data-slot="tenant-list"] li').length === 3, 'the tenants')
    await openTenantPage(`${tt.list.open}: Acme`, tt.detail.tabs.danger)
    await settle(() => button(tt.list.disable) !== null, 'the details')
    const li = document.querySelector<HTMLElement>('main')!
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

  it('tells the organisations people made themselves from the others, and the filter shows only those', async () => {
    await open('tenants', (d) => {
      tenancyOn(d)
      addTenant(d, 'acme', 'Acme')
      addTenant(d, 'bee-co', 'Bee Co', { origin: 'self' })
    })
    await settle(() => document.querySelectorAll('[data-slot="tenant-list"] li').length === 3, 'the tenants')
    expect(document.querySelectorAll('[data-slot="tenant-list"] li')[2].textContent).toContain(tt.list.originSelf)
    expect(document.querySelectorAll('[data-slot="tenant-list"] li')[1].textContent).not.toContain(tt.list.originSelf)
    await click(button(tt.list.filterSelf(1)))
    await settle(() => document.querySelectorAll('[data-slot="tenant-list"] li').length === 1, 'only the made ones')
    expect(text()).toContain('Bee Co')
    // The server did the filtering: its own list is cached under the origin.
    expect(m!.qc.getQueryData([...keys.tenants, 'self'])).toHaveLength(1)
  })

  it('nothing on it can be wider than a phone', async () => {
    await open('tenants', (d) => tenancyOn(d))
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
  it("has features.offline on both layers, named and explained, and a tenant's switch writes the tenant layer", async () => {
    await open('settings')
    await settle(
      () => document.querySelectorAll('[data-slot="setting-row"][data-key="features.offline"]').length === 2,
      'the rows',
    )
    const rows = [...document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="features.offline"]')]
    expect(rows[0].textContent).toContain(tt.settings.names['features.offline'])
    expect(rows[0].textContent).toContain(tt.settings.helps['features.offline'])
    expect(rows[0].querySelector('[role="switch"]')!.getAttribute('aria-checked')).toBe('true')
    expect(tt.settings.helps['features.offline']).toMatch(/feature_off/)
    await click(rows[1].querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]'))
    await click(rows[1].querySelector(`[role="switch"][aria-label="${tt.settings.names['features.offline']}"]`))
    await click(button(tt.settings.save, rows[1]))
    await settle(
      () =>
        m!.requests.some(
          (r) => r.method === 'PUT' && r.url === '/api/admin/tenants/0000000000/settings/features.offline',
        ),
      'saved',
    )
    expect(
      JSON.parse(m!.requests.find((r) => r.url === '/api/admin/tenants/0000000000/settings/features.offline')!.body!),
    ).toEqual({ value: false })
  })

  it('shows the site layer and the tenant layer with the lock source, keeps a value a lock hides and brings it back', async () => {
    // A setting the site locks for tenants: the tenant layer says so and cannot change it.
    await open('settings', (d) => void (setting(d, 'legal.termsUrl').site.lower = { mode: 'lock' }))
    await settle(
      () => rowOf('registration.perHour') !== undefined && rowOf('registration.perHour') !== null,
      'the settings',
    )
    await settle(() => document.querySelectorAll('[data-slot="setting-row"]').length >= 16, 'both layers')
    const rows = [...document.querySelectorAll('[data-slot="setting-row"][data-key="legal.termsUrl"]')]
    expect(rows).toHaveLength(2)
    expect(rows[1].textContent).toContain(tt.settings.lockedBy.site)
    expect(
      rows[1].querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]')!.hasAttribute('data-disabled'),
    ).toBe(true)
    // The site tightens perHour to 10 after the tenant took 30: kept, marked as not in effect.
    // (The server works out that the tenant's 30 is above the new maximum: its rule is not in effect.)
    const d = m!.fx.data
    ownRule(d, '0000000000', 'registration.perHour', { value: 30, inEffect: false })
    setting(d, 'registration.perHour').site.lower = { mode: 'restrict', max: 10 }
    await m!.qc.invalidateQueries()
    await settle(() => document.querySelector('[data-slot="not-in-effect"]') !== null, 'the kept value is marked')
    expect(document.querySelector('[data-slot="not-in-effect"]')!.closest('li')!.textContent).toContain(
      tt.settings.keptHint,
    )
    ownRule(d, '0000000000', 'registration.perHour', { value: 30, inEffect: true })
    setting(d, 'registration.perHour').site.lower = { mode: 'allow' }
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
    // The registration mode is a choice with its own words; a bad value is refused in words.
    const open1 = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.mode"]')[0]
    const mode = open1.querySelector<HTMLSelectElement>('select')!
    expect([...mode.options].map((o) => o.textContent)).toEqual([
      tt.settings.choices['registration.mode'].open,
      tt.settings.choices['registration.mode'].invite,
      tt.settings.choices['registration.mode'].closed,
    ])
    expect(mode.value).toBe('invite')
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
    const got = m!.fx.call('GET', '/api/admin/tenants/0000000000/settings').body
    const s = (got.settings as { key: string; effective: unknown }[]).find((x) => x.key === 'storage.assets')!
    expect(s.effective).toBe('space')
  })

  it('keeps a forced value forced when its row is switched to follow: the save names the value in effect', async () => {
    await open('settings', (d) =>
      ownRule(d, '0000000000', 'storage.assets', {
        value: 'space',
        siteLocked: true,
        rules: [{ source: 'manual', value: 'space', lower: { mode: 'lock' }, inEffect: true }],
      }),
    )
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
    const got = m!.fx.call('GET', '/api/admin/tenants/0000000000/settings').body
    const s = (got.settings as { key: string; effective: unknown; tenant: { siteLocked?: boolean } }[]).find(
      (x) => x.key === 'storage.assets',
    )!
    expect([s.effective, s.tenant.siteLocked]).toEqual(['space', true])
  })

  it('starts the editor of a value not in effect from the value in effect, with nothing to save', async () => {
    await open('settings', (d) => {
      // The tenant took 30 and the site tightened the maximum to 10 after: the rule is kept, not in effect.
      ownRule(d, '0000000000', 'registration.perHour', { value: 30, inEffect: false })
      setSite(d, 'registration.perHour', 10, { mode: 'restrict', max: 10 })
    })
    await settle(() => document.querySelector('[data-slot="not-in-effect"]') !== null, 'the kept value')
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.perHour"]')[1]
    const name = tt.settings.names['registration.perHour']
    expect(row.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!.value).toBe('10')
    expect(button(tt.settings.save, row)!.disabled).toBe(true)
  })

  it('refuses a site value above its own maximum and says so in the row', async () => {
    // The server refuses a value above the maximum the site's own rule for perHour allows (50 by default).
    await open('settings', undefined, (fx) =>
      fx.fail('PUT', '/api/admin/settings/registration.perHour', 400, { code: 'setting_above_max' }),
    )
    await settle(() => rowOf('registration.perHour') != null, 'the settings')
    const row = document.querySelectorAll<HTMLElement>('[data-slot="setting-row"][data-key="registration.perHour"]')[0]
    const name = tt.settings.names['registration.perHour']
    await type(row.querySelector<HTMLInputElement>(`input[aria-label="${name}"]`)!, '80')
    await click(button(tt.settings.save, row))
    await settle(() => row.textContent!.includes(tt.settings.errors.setting_above_max), 'the refusal')
    expect(
      JSON.parse(
        m!.requests.find((r) => r.method === 'PUT' && r.url === '/api/admin/settings/registration.perHour')!.body!,
      ),
    ).toMatchObject({ value: 80 })
  })
})

describe('administrators and tenants in the Users page and the navigation', () => {
  it('names the kind of administrator, and a site administrator can make or end another', async () => {
    await open('users', (d) => void addPerson(d, 'second', { role: 'admin', siteAdmin: true }))
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
    await open('users', (d) => {
      addPerson(d, 'second', { role: 'admin', siteAdmin: true })
      asTenantAdmin(d)
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
    await open('users', (d) => {
      second = addPerson(d, 'second', { role: 'admin', siteAdmin: true }).id
      asTenantAdmin(d)
      return `/admin/user/${second}/security`
    })
    await settle(() => document.querySelector('[data-slot="person-security"]') !== null, 'the page')
    expect(m!.requests.filter((r) => r.url.startsWith('/api/admin/security'))).toEqual([])
    await m!.unmount()
    // A site administrator's does: it holds the roles a passkey is required of.
    await open('users', (d) => `/admin/user/${addPerson(d, 'second', { role: 'admin', siteAdmin: true }).id}/security`)
    await settle(
      () => m!.requests.some((r) => r.method === 'GET' && r.url.startsWith('/api/admin/security')),
      'the security settings',
    )
  })
})
