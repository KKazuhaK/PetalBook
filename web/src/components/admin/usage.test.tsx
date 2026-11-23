// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tt } from '@/i18n/tenants'
import { tu } from '@/i18n/usage'
import { adminRequests, click, installDomShims, makePerson, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The administration's Usage page, the limits on a user, and the usage of a tenant, against the mock server.

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
const button = (label: string, root: ParentNode = document) => [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label) ?? null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const people = () => [...document.querySelectorAll<HTMLElement>('[data-slot="people-usage"] > li')]
const personRow = (name: string) => people().find((r) => r.textContent?.includes(name))!
const usageCalls = () => m!.requests.filter((r) => r.method === 'GET' && r.url.startsWith('/api/admin/usage')).map((r) => r.url)
const switchOf = (label: string) => document.querySelector<HTMLElement>(`[role="switch"][aria-label="${label}"]`)!

async function open(section: string, scene?: (server: MockServer) => unknown, server = {}) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts', empty: true, ...server },
    entries: (s) => {
      scene?.(s)
      return [`/admin/${section}`]
    },
  })
}

// Somebody with a measured space of a given size.
function sized(server: MockServer, username: string, chars: number, role: 'member' | 'admin' = 'member') {
  const person = makePerson(server, username, role)
  const space = (person.request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
  person.request('POST', `/api/spaces/${space}/pages`, { folder: '', meta: { title: username }, body: 'x'.repeat(chars) })
  adminRequests(server)('POST', `/api/admin/spaces/${space}/size`)
  return person
}

describe('the Usage page', () => {
  it('lists people with the most disk first, their numbers against their limits, and says when nothing was measured; the tenant list is not shown while there is one tenant', async () => {
    await open('usage', (server) => {
      sized(server, 'amy', 100)
      sized(server, 'bob', 9000)
      makePerson(server, 'cat')
    })
    await settle(() => people().length === 4, 'four people')
    // bob is the largest, then amy; cat and the administrator were never measured.
    expect(people()[0].textContent).toContain('名 bob')
    expect(people()[1].textContent).toContain('名 amy')
    expect(personRow('名 cat').textContent).toContain(tu.neverMeasured)
    expect(personRow('名 bob').textContent).not.toContain(tu.neverMeasured)
    // The default limit on spaces is shown against each count; disk and pages have none.
    expect(personRow('名 bob').querySelector('[data-quota="spaces"]')!.textContent).toContain('1 / 20')
    expect(personRow('名 bob').querySelector('[data-quota="bytes"]')!.textContent).not.toContain('/')
    expect(document.querySelector('[data-slot="tenant-usage"]')).toBeNull()
    expect(document.querySelector('#usage-tenant')).toBeNull()
    // One request for the list, however many people.
    expect(usageCalls()).toHaveLength(1)
    expect(document.title).toContain(t.appName)
  })

  it('narrows to the people over a limit and says so when nobody is', async () => {
    await open('usage', (server) => {
      sized(server, 'amy', 100)
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/settings/quota.bytesPerPerson', { value: 1 })
    })
    await settle(() => people().length === 2, 'two people')
    expect(personRow('名 amy').querySelector('[data-slot="over"]')!.textContent).toBe(tu.admin.overFor(tu.quotas.bytes))
    expect(personRow('名 amy').querySelector('[data-quota="bytes"]')!.className).toContain('text-destructive')
    await click(switchOf(tu.admin.overOnly))
    await settle(() => people().length === 1, 'only the person over')
    expect(people()[0].textContent).toContain('名 amy')
    expect(usageCalls().some((u) => u.includes('over=1'))).toBe(true)
    // Raise the limit: nobody is over.
    adminRequests(m!.server)('PUT', '/api/admin/settings/quota.bytesPerPerson', { value: 1024 * 1024 * 1024 })
    await m!.qc.invalidateQueries()
    await settle(() => text().includes(tu.admin.noneOver), 'nobody over')
  })

  it('loads more people on request, and edits one person\'s own limits in a dialog', async () => {
    await open('usage', (server) => {
      for (let i = 1; i <= 55; i++) makePerson(server, `p${String(i).padStart(2, '0')}`)
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
    expect(JSON.parse(patch.body!)).toEqual({ quota: { spaces: 40, bytes: Math.round(1.5 * 1024 ** 3), pages: null } })
    // The page reads the list again: the person's limit is on their row now.
    await settle(() => personRow('名 p03').querySelector('[data-quota="spaces"]')!.textContent?.includes('/ 40') === true, 'the limit on the row')
  })

  it('refuses what is not a number, before asking the server', async () => {
    await open('usage', (server) => makePerson(server, 'amy'))
    await settle(() => people().length === 2, 'two people')
    await click(button(tu.admin.editLimitsFor('名 amy')))
    await settle(() => dlg()?.querySelector('[data-slot="own-limits"]') !== null, 'the form')
    await type(dlg().querySelectorAll<HTMLInputElement>('input')[2], 'lots')
    await click(button(tu.own.save, dlg()))
    await settle(() => dlg().textContent?.includes(tu.own.invalid) === true, 'the refusal')
    expect(m!.requests.some((r) => r.method === 'PATCH')).toBe(false)
  })

  it('shows a site administrator the tenants with what each uses, and a tenant\'s people when it is picked', async () => {
    await open('usage', (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/tenancy', { enabled: true })
      admin('POST', '/api/admin/tenants', { slug: 'acme', name: 'Acme' })
      sized(server, 'amy', 100)
      admin('PATCH', '/api/admin/tenants/0000000000', { quota: { people: 1 } })
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
    await open('usage', (server) => {
      sized(server, 'amy', 100)
      adminRequests(server)('PUT', '/api/admin/tenancy', { enabled: true })
      adminRequests(server)('POST', '/api/admin/tenants', { slug: 'acme', name: 'Acme' })
    })
    await settle(() => people().length === 2 && document.querySelectorAll('[data-slot="tenant-usage"] > li').length === 2, 'the page')
    const has = (el: Element | null, ...c: string[]) => c.every((x) => el?.classList.contains(x))
    for (const li of people()) {
      expect(has(li, 'flex-wrap', 'min-w-0')).toBe(true)
      expect(has(li.querySelector('dl'), 'grid-cols-1', 'min-w-0')).toBe(true)
    }
    for (const li of document.querySelectorAll('[data-slot="tenant-usage"] > li')) expect(has(li, 'flex-wrap', 'min-w-0')).toBe(true)
    for (const id of ['usage-tenants-heading', 'usage-people-heading']) expect(document.querySelector(`section[aria-labelledby="${id}"]`)!.classList.contains('grid-cols-1')).toBe(true)
    // The limits form is one column on a phone and three from the small breakpoint up.
    await click(button(tu.admin.editLimitsFor('名 amy')))
    await settle(() => dlg()?.querySelector('[data-slot="own-limits"]') !== null, 'the form')
    expect(has(dlg().querySelector('[data-slot="own-limits"] > div:nth-of-type(2)'), 'grid-cols-1', 'min-w-0')).toBe(true)
  })

  it('shows a tenant administrator only their people, with no tenant list', async () => {
    await open('usage', (server) => {
      const admin = adminRequests(server)
      sized(server, 'amy', 100)
      makePerson(server, 'second', 'admin') // another site administrator must remain
      admin('PATCH', `/api/users/${(admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find((u) => u.username === 'admin')!.id}`, { siteAdmin: false })
    })
    await settle(() => people().length > 0, 'the people')
    expect(document.querySelector('[data-slot="tenant-usage"]')).toBeNull()
    expect(document.querySelector('#usage-tenant')).toBeNull()
  })
})

describe('limits on a user, and the usage of a tenant', () => {
  it('the user\'s details have the person\'s own limits, which are saved with the other details of the person unchanged', async () => {
    await open('users', (server) => makePerson(server, 'amy'))
    await settle(() => button(t.fmt.labelled(t.users.disable, '名 amy')) !== null, 'the users')
    const edit = [...document.querySelectorAll('button')].find((b) => b.getAttribute('aria-label')?.includes('名 amy') && b.textContent === '编辑')!
    await click(edit)
    await settle(() => document.querySelector('[data-slot="own-limits"]') !== null, 'the limits form')
    const fields = [...document.querySelectorAll<HTMLInputElement>('[data-slot="own-limits"] input')]
    await type(fields[2], '5000')
    await click(button(tu.own.save))
    await settle(() => text().includes(tu.own.saved), 'saved')
    const patch = m!.requests.find((r) => r.method === 'PATCH' && r.url.startsWith('/api/users/'))!
    expect(JSON.parse(patch.body!)).toEqual({ quota: { spaces: null, bytes: null, pages: 5000 } })
  })

  it('says what a refusal means: only a site administrator goes above the tenant', async () => {
    await open('users', (server) => {
      const admin = adminRequests(server)
      makePerson(server, 'amy')
      admin('PUT', '/api/admin/settings/quota.spacesPerPerson', { value: 3 })
      const me = (admin('GET', '/api/users').body as { users: { id: string; username: string }[] }).users.find((u) => u.username === 'admin')!
      makePerson(server, 'boss', 'admin')
      admin('PATCH', `/api/users/${me.id}`, { siteAdmin: false })
    })
    await settle(() => button(t.fmt.labelled(t.users.disable, '名 amy')) !== null, 'the users')
    const edit = [...document.querySelectorAll('button')].find((b) => b.getAttribute('aria-label')?.includes('名 amy') && b.textContent === '编辑')!
    await click(edit)
    await settle(() => document.querySelector('[data-slot="own-limits"]') !== null, 'the limits form')
    await type(document.querySelectorAll<HTMLInputElement>('[data-slot="own-limits"] input')[0], '9')
    await click(button(tu.own.save))
    await settle(() => text().includes(tu.own.forbidden), 'the refusal')
  })

  it('a tenant\'s details show what it uses and its totals, which a site administrator sets', async () => {
    await open('tenants', (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/tenancy', { enabled: true })
      sized(server, 'amy', 100)
    })
    await settle(() => button(`${tt.list.manage}: Default`) !== null, 'the list')
    await click(button(`${tt.list.manage}: Default`))
    await settle(() => document.querySelector('[data-slot="tenant-usage-block"]') !== null, 'the usage block')
    const block = document.querySelector<HTMLElement>('[data-slot="tenant-usage-block"]')!
    expect(block.querySelector('[data-slot="tenant-numbers"] [data-quota="people"]')!.textContent).toContain('2')
    expect(block.querySelector('[data-slot="tenant-numbers"] [data-quota="spaces"]')!.textContent).toContain('2')
    const fields = [...block.querySelectorAll<HTMLInputElement>('form input')]
    expect(fields.map((f) => f.value)).toEqual(['', '', '', ''])
    await type(fields[0], '1')
    await type(fields[2], '2')
    await click(button(tu.tenant.save, block))
    await settle(() => block.textContent?.includes(tu.tenant.saved) === true, 'saved')
    const patch = m!.requests.find((r) => r.method === 'PATCH' && r.url === '/api/admin/tenants/0000000000')!
    expect(JSON.parse(patch.body!)).toEqual({ quota: { people: 1, spaces: null, bytes: 2 * 1024 ** 3, pages: null } })
    // Now it is over for people: the block says so.
    await settle(() => document.querySelector('[data-slot="tenant-usage-block"]')!.textContent!.includes(tu.tenant.over(tu.quotas.people)), 'over')
  })
})

describe('the quotas on the Settings page', () => {
  const rows = (key: string) => [...document.querySelectorAll<HTMLElement>(`[data-slot="setting-row"][data-key="${key}"]`)]

  it('has the three limits on the site layer and the tenant layer, named in words', async () => {
    await open('settings')
    await settle(() => rows('quota.spacesPerPerson').length === 2, 'the quota rows')
    for (const key of ['quota.spacesPerPerson', 'quota.bytesPerPerson', 'quota.pagesPerPerson']) {
      expect(rows(key)).toHaveLength(2)
      expect(rows(key)[0].textContent).toContain((tt.settings.names as Record<string, string>)[key])
    }
    // The site's value of the number of spaces is the default 20; disk and pages are not limited.
    expect(rows('quota.spacesPerPerson')[0].querySelector<HTMLInputElement>('input[type="number"]')!.value).toBe('20')
    expect(rows('quota.bytesPerPerson')[0].querySelector<HTMLInputElement>('input[type="number"]')!.value).toBe('')
  })

  it('--max-spaces makes the site value of the number of spaces read only, and says so', async () => {
    await open('settings', undefined, { maxSpaces: 7 })
    await settle(() => rows('quota.spacesPerPerson').length === 2, 'the quota rows')
    const site = rows('quota.spacesPerPerson')[0]
    expect(site.querySelector<HTMLInputElement>('input[type="number"]')!.value).toBe('7')
    expect(site.querySelector<HTMLInputElement>('input[type="number"]')!.disabled).toBe(true)
    expect(site.textContent).toContain(tt.settings.fromFlag)
  })
})
