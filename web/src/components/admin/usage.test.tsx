// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { sharedSpace } from '@/api/fixtures/data/model'
import type { UserRec } from '@/api/fixtures/data/people'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tt } from '@/i18n/tenants'
import { tu } from '@/i18n/usage'
import { click, installDomShims, mount, settle, type, type FxMounted, openTenantPage } from '@/testing/harness'
import { addPerson, addTenant, asTenantAdmin, measured, setting, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The administration's Usage page, the limits on a user, and the usage of a tenant, against the fixture server. Which people are over a limit, and what a tenant uses, are what the server
// measured: a scene puts the numbers the page lists.

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
const people = () => [...document.querySelectorAll<HTMLElement>('[data-slot="people-usage"] > li')]
const personRow = (name: string) => people().find((r) => r.textContent?.includes(name))!
const usageCalls = () =>
  m!.requests.filter((r) => r.method === 'GET' && r.url.startsWith('/api/admin/usage')).map((r) => r.url)
const switchOf = (label: string) => document.querySelector<HTMLElement>(`[role="switch"][aria-label="${label}"]`)!

async function open(section: string, scene?: (d: FixtureData) => unknown, flag?: (d: FixtureData) => void) {
  let where: unknown
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        where = scene?.(d)
        flag?.(d)
      },
    },
    // A scene may send the page to one person's own address.
    entries: () => [typeof where === 'string' ? where : `/admin/${section}`],
  })
}

// Somebody with a measured space of a given size (a page of `chars` characters is that many bytes).
function sized(d: FixtureData, username: string, chars: number, extra: Partial<UserRec> = {}) {
  const person = addPerson(d, username, extra)
  measured(d, person, { bytes: chars, pages: 1, spaces: 1 })
  return person
}

describe('the Usage page', () => {
  it('lists people with the most disk first, their numbers against their limits, and says when nothing was measured; the tenant list is not shown while there is one tenant', async () => {
    await open('storage/usage', (d) => {
      sized(d, 'amy', 100)
      sized(d, 'bob', 9000)
      addPerson(d, 'cat')
    })
    await settle(() => people().length === 4, 'four people')
    // bob is the largest, then amy; cat and the administrator were never measured.
    expect(people()[0].textContent).toContain('名 bob')
    expect(people()[1].textContent).toContain('名 amy')
    expect(personRow('名 cat').textContent).toContain(tu.neverMeasured)
    expect(personRow('名 bob').textContent).not.toContain(tu.neverMeasured)
    // The default limit on spaces is shown against each count; disk and pages have none.
    expect(personRow('名 bob').querySelector('[data-quota="spaces"]')!.textContent).toContain('1 / 3')
    expect(personRow('名 bob').querySelector('[data-quota="bytes"]')!.textContent).not.toContain('/')
    expect(document.querySelector('[data-slot="tenant-usage"]')).toBeNull()
    expect(document.querySelector('#usage-tenant')).toBeNull()
    // One request for the list, however many people.
    expect(usageCalls()).toHaveLength(1)
    expect(document.title).toContain(t.appName)
  })

  it('narrows to the people over a limit and says so when nobody is', async () => {
    await open('storage/usage', (d) => {
      const amy = sized(d, 'amy', 100)
      setting(d, 'quota.bytesPerPerson').site.value = 1
      // The server counts amy over the disk limit.
      measured(d, amy, { bytes: 100, pages: 1, spaces: 1, over: ['bytes'] })
    })
    await settle(() => people().length === 2, 'two people')
    expect(personRow('名 amy').querySelector('[data-slot="over"]')!.textContent).toBe(tu.admin.overFor(tu.quotas.bytes))
    expect(personRow('名 amy').querySelector('[data-quota="bytes"]')!.className).toContain('text-destructive')
    await click(switchOf(tu.admin.overOnly))
    await settle(() => people().length === 1, 'only the person over')
    expect(people()[0].textContent).toContain('名 amy')
    expect(usageCalls().some((u) => u.includes('over=1'))).toBe(true)
    // Raise the limit: nobody is over.
    m!.fx.call('PUT', '/api/admin/settings/quota.bytesPerPerson', { value: 1024 * 1024 * 1024 })
    m!.fx.data.admin.people[m!.fx.data.auth.users.find((u) => u.username === 'amy')!.id].usage!.over = []
    await m!.qc.invalidateQueries()
    await settle(() => text().includes(tu.admin.noneOver), 'nobody over')
  })

  it("loads more people on request, and edits one person's own limits in a dialog", async () => {
    await open('storage/usage', (d) => {
      for (let i = 1; i <= 55; i++) addPerson(d, `p${String(i).padStart(2, '0')}`)
    })
    await settle(() => people().length === 50, 'the first page')
    await click(button(tu.admin.more))
    await settle(() => people().length === 56, 'the rest')
    expect(button(tu.admin.more)).toBeNull()
    // The dialog starts from the person's own limits (none) and saves them.
    await click(button(tu.admin.editLimitsFor('名 p03')))
    await settle(() => dlg()?.querySelector('[data-slot="own-limits"]') !== null, 'the form')
    const fields = [...dlg().querySelectorAll<HTMLInputElement>('input')]
    expect(fields.map((f) => f.value)).toEqual(['', '', ''])
    await type(fields[0], '40')
    await type(fields[1], '1.5')
    await click(button(tu.own.save, dlg()))
    await settle(() => dlg().textContent?.includes(tu.own.saved) === true, 'saved')
    const patch = m!.requests.find((r) => r.method === 'PATCH' && r.url.startsWith('/api/users/'))!
    expect(JSON.parse(patch.body!)).toEqual({ quota: { spaces: 40, bytes: Math.round(1.5 * 1024 ** 3) } })
    // The page reads the list again: the person's limit is on their row now.
    await settle(
      () => personRow('名 p03').querySelector('[data-quota="spaces"]')!.textContent?.includes('/ 40') === true,
      'the limit on the row',
    )
  })

  it('refuses what is not a number, before asking the server', async () => {
    await open('storage/usage', (d) => void addPerson(d, 'amy'))
    await settle(() => people().length === 2, 'two people')
    await click(button(tu.admin.editLimitsFor('名 amy')))
    await settle(() => dlg()?.querySelector('[data-slot="own-limits"]') !== null, 'the form')
    await type(dlg().querySelectorAll<HTMLInputElement>('input')[2], 'lots')
    await click(button(tu.own.save, dlg()))
    await settle(() => dlg().textContent?.includes(tu.own.invalid) === true, 'the refusal')
    expect(m!.requests.some((r) => r.method === 'PATCH')).toBe(false)
  })

  it("shows a site administrator the tenants with what each uses, and a tenant's people when it is picked", async () => {
    await open('storage/usage', (d) => {
      tenancyOn(d)
      addTenant(d, 'acme', 'Acme')
      sized(d, 'amy', 100)
      // The default tenant is limited to one person and has two: the server counts it over.
      d.admin.tenants[0].quota.people = 1
      d.admin.tenants[0].usage.over = ['people']
    })
    await settle(() => document.querySelectorAll('[data-slot="tenant-usage"] > li').length === 2, 'two tenants')
    const rows = [...document.querySelectorAll('[data-slot="tenant-usage"] > li')]
    expect(rows[0].textContent).toContain('Default')
    expect(rows[0].textContent).toContain(tu.admin.tenantPeople(2))
    expect(rows[0].querySelector('[data-slot="over"]')!.textContent).toBe(tu.admin.overFor(tu.quotas.people))
    expect(rows[0].querySelector('[data-slot="tenant-totals"]')!.textContent).toContain(`${tu.quotas.people} 2 / 1`)
    expect(rows[1].textContent).toContain('Acme')
    expect(rows[1].textContent).toContain(tu.admin.tenantPeople(0))
    // Picking a tenant narrows the people to it (a site administrator chooses; the list reads again).
    await click(button(`${tu.admin.people}: Default`))
    await settle(() => usageCalls().some((u) => u.includes('tenant=0000000000')), 'the narrowed request')
    await settle(() => people().length === 2, 'its people')
    const select = document.querySelector<HTMLSelectElement>('#usage-tenant')!
    expect(select.value).toBe('0000000000')
  })

  it('nothing on it can be wider than a phone: rows wrap and shrink, the numbers stack in one column', async () => {
    await open('storage/usage', (d) => {
      sized(d, 'amy', 100)
      tenancyOn(d)
      addTenant(d, 'acme', 'Acme')
    })
    await settle(
      () => people().length === 2 && document.querySelectorAll('[data-slot="tenant-usage"] > li').length === 2,
      'the page',
    )
    const has = (el: Element | null, ...c: string[]) => c.every((x) => el?.classList.contains(x))
    for (const li of people()) {
      expect(has(li, 'flex-wrap', 'min-w-0')).toBe(true)
      expect(has(li.querySelector('dl'), 'grid-cols-1', 'min-w-0')).toBe(true)
    }
    for (const li of document.querySelectorAll('[data-slot="tenant-usage"] > li'))
      expect(has(li, 'flex-wrap', 'min-w-0')).toBe(true)
    for (const id of ['usage-tenants-heading', 'usage-people-heading'])
      expect(document.querySelector(`section[aria-labelledby="${id}"]`)!.classList.contains('grid-cols-1')).toBe(true)
    // The limits form is one column on a phone and three from the small breakpoint up.
    await click(button(tu.admin.editLimitsFor('名 amy')))
    await settle(() => dlg()?.querySelector('[data-slot="own-limits"]') !== null, 'the form')
    expect(has(dlg().querySelector('[data-slot="own-limits"] > div:nth-of-type(2)'), 'grid-cols-1', 'min-w-0')).toBe(
      true,
    )
  })

  it('shows a tenant administrator only their people, with no tenant list', async () => {
    await open('storage/usage', (d) => {
      sized(d, 'amy', 100)
      asTenantAdmin(d)
    })
    await settle(() => people().length > 0, 'the people')
    expect(document.querySelector('[data-slot="tenant-usage"]')).toBeNull()
    expect(document.querySelector('#usage-tenant')).toBeNull()
  })
})

describe('limits on a user, and the usage of a tenant', () => {
  it("the user's details have the person's own limits, which are saved with the other details of the person unchanged", async () => {
    await open('users', (d) => `/admin/user/${addPerson(d, 'amy').id}`)
    await settle(() => document.querySelector('[data-slot="own-limits"]') !== null, 'the limits form')
    const fields = [...document.querySelectorAll<HTMLInputElement>('[data-slot="own-limits"] input')]
    await type(fields[2], '5000')
    await click(button(tu.own.save))
    await settle(() => text().includes(tu.own.saved), 'saved')
    const patch = m!.requests.find((r) => r.method === 'PATCH' && r.url.startsWith('/api/users/'))!
    expect(JSON.parse(patch.body!)).toEqual({ quota: { pages: 5000 } })
  })

  it('sends only the limits the administrator changed, so a disk limit that is no whole number of GB is kept to the byte', async () => {
    const exact = 1234567891
    await open('users', (d) => {
      const amy = addPerson(d, 'amy')
      d.admin.people[amy.id] = {
        sessions: [],
        links: [],
        passkeys: [],
        quota: { spaces: 7, bytes: exact, pages: null },
      }
      return `/admin/user/${amy.id}`
    })
    await settle(() => document.querySelector('[data-slot="own-limits"]') !== null, 'the limits form')
    const fields = [...document.querySelectorAll<HTMLInputElement>('[data-slot="own-limits"] input')]
    expect(fields.map((f) => f.value)).toEqual(['7', '1.15', ''])
    await type(fields[2], '5000')
    await click(button(tu.own.save))
    await settle(() => text().includes(tu.own.saved), 'saved')
    const patches = () => m!.requests.filter((r) => r.method === 'PATCH' && r.url.startsWith('/api/users/'))
    expect(JSON.parse(patches()[0].body!)).toEqual({ quota: { pages: 5000 } })
    // Saving again with nothing changed sends nothing to change; clearing a field sends null for it alone.
    await type(fields[0], '')
    await click(button(tu.own.save))
    await settle(() => patches().length === 2, 'the second save')
    expect(JSON.parse(patches()[1].body!)).toEqual({ quota: { spaces: null } })
    const users = m!.fx.call('GET', '/api/users').body as {
      users: { username: string; quota: { bytes: number | null } }[]
    }
    expect(users.users.find((u) => u.username === 'amy')!.quota.bytes).toBe(exact)
  })

  it('says what a refusal means: only a site administrator goes above the tenant', async () => {
    await open('users', (d) => {
      const amy = addPerson(d, 'amy')
      asTenantAdmin(d)
      return `/admin/user/${amy.id}`
    })
    // The server refuses a limit above the tenant's to a tenant administrator.
    m!.fx.fail('PATCH', '/api/users/:id', 403, { code: 'forbidden' })
    await settle(() => document.querySelector('[data-slot="own-limits"]') !== null, 'the limits form')
    await type(document.querySelectorAll<HTMLInputElement>('[data-slot="own-limits"] input')[0], '9')
    await click(button(tu.own.save))
    await settle(() => text().includes(tu.own.forbidden), 'the refusal')
  })

  it("a tenant's details show what it uses and its totals, which a site administrator sets", async () => {
    await open('tenants', (d) => {
      tenancyOn(d)
      sized(d, 'amy', 100)
      sharedSpace(d, 'owner', '团队')
    })
    await openTenantPage(`${tt.list.open}: Default`, tt.detail.tabs.plan)
    await settle(() => document.querySelector('[data-slot="tenant-usage-block"]') !== null, 'the usage block')
    const block = document.querySelector<HTMLElement>('[data-slot="tenant-usage-block"]')!
    expect(block.querySelector('[data-slot="tenant-numbers"] [data-quota="people"]')!.textContent).toContain('2')
    expect(block.querySelector('[data-slot="tenant-numbers"] [data-quota="spaces"]')!.textContent).toContain('2')
    // The totals are rules for the tenant (quota.* settings), under the usage: each with where it comes from, changed
    // one at a time.
    await settle(
      () => document.querySelector('[data-slot="tenant-rules"] [data-key="quota.people"]') !== null,
      'the rules',
    )
    const sheet = document.querySelector<HTMLElement>('[data-slot="tenant-rules"]')!
    expect(sheet.querySelector('[data-key="quota.people"]')!.getAttribute('data-source')).toBe('site')
    await click(button(tt.rules.editFor('quota.people'), sheet))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the form')
    await type(dlg().querySelector<HTMLInputElement>('#rule-value')!, '1')
    await click(button(tt.rules.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    const url = '/api/admin/tenants/0000000000/settings/quota.people'
    const put = m!.requests.find((r) => r.method === 'PUT' && r.url === url)!
    expect(JSON.parse(put.body!)).toEqual({ siteRule: { value: 1 } })
    await settle(
      () => document.querySelector('[data-key="quota.people"]')?.getAttribute('data-source') === 'manual',
      'the source is manual now',
    )
    // A disk total is typed in GB.
    await click(button(tt.rules.editFor('quota.bytes'), sheet))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the disk form')
    await type(dlg().querySelector<HTMLInputElement>('#rule-value')!, '2')
    await click(button(tt.rules.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved the disk')
    const bytesUrl = '/api/admin/tenants/0000000000/settings/quota.bytes'
    const puts = m!.requests.filter((r) => r.method === 'PUT' && r.url === bytesUrl)
    expect(JSON.parse(puts[0].body!)).toEqual({ siteRule: { value: 2 * 1024 ** 3 } })
    // Now the server counts it over for people: the block says so.
    m!.fx.data.admin.tenants[0].usage.over = ['people']
    await m!.qc.invalidateQueries()
    await settle(
      () =>
        document
          .querySelector('[data-slot="tenant-usage-block"]')!
          .textContent!.includes(tu.tenant.over(tu.quotas.people)),
      'over',
    )
  })
})

describe('the quotas on the Settings page', () => {
  const rows = (key: string) => [
    ...document.querySelectorAll<HTMLElement>(`[data-slot="setting-row"][data-key="${key}"]`),
  ]

  it('has the three limits on the site layer and the tenant layer, named in words', async () => {
    await open('settings')
    await settle(() => rows('quota.spacesPerPerson').length === 2, 'the quota rows')
    for (const key of ['quota.spacesPerPerson', 'quota.bytesPerPerson', 'quota.pagesPerPerson']) {
      expect(rows(key)).toHaveLength(2)
      expect(rows(key)[0].textContent).toContain((tt.settings.names as Record<string, string>)[key])
    }
    // The site's value of the number of spaces is the default 3; disk and pages are not limited.
    expect(rows('quota.spacesPerPerson')[0].querySelector<HTMLInputElement>('input[type="number"]')!.value).toBe('3')
    expect(rows('quota.bytesPerPerson')[0].querySelector<HTMLInputElement>('input[type="number"]')!.value).toBe('')
  })

  it('--max-spaces makes the site value of the number of spaces read only, and says so', async () => {
    await open('settings', undefined, (d) => {
      const s = setting(d, 'quota.spacesPerPerson')
      s.site.value = 7
      s.site.fromFlag = true
    })
    await settle(() => rows('quota.spacesPerPerson').length === 2, 'the quota rows')
    const site = rows('quota.spacesPerPerson')[0]
    expect(site.querySelector<HTMLInputElement>('input[type="number"]')!.value).toBe('7')
    expect(site.querySelector<HTMLInputElement>('input[type="number"]')!.disabled).toBe(true)
    expect(site.textContent).toContain(tt.settings.fromFlag)
  })
})
