// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { tt } from '@/i18n/tenants'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  locationText,
  mount,
  settle,
  type Mounted,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'

// A tenant's page for the site (/admin/tenants/<id>/<tab>) and the console in its scope (/admin/t/<id>/...): the scope
// header goes out only from the scope, the banner says so and leads out, and the sidebar is what a tenant's
// administrator has.

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

let tenant = ''
async function open(path: (id: string) => string) {
  await m?.unmount()
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts', empty: true },
    entries: (server: MockServer) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/settings/features.tenancy', { value: true })
      tenant = (admin('POST', '/api/admin/tenants', { slug: 'acme', name: 'Acme' }).body as { id: string }).id
      return [path(tenant)]
    },
  })
}
const sidebarLinks = () =>
  [...document.querySelectorAll(`aside nav[aria-label="${tc.nav}"] a`)].map((a) => a.textContent?.trim())
const scoped = () => m!.requests.filter((r) => r.scope)

describe("the console in a tenant's scope", () => {
  it('sends the scope header on administration calls from /admin/t/<id> and nowhere else', async () => {
    await open((id) => `/admin/t/${id}/users/all`)
    await settle(() => m!.requests.some((r) => r.url.startsWith('/api/users')), 'the people')
    const users = m!.requests.filter((r) => r.url.startsWith('/api/users'))
    expect(users.length).toBeGreaterThan(0)
    for (const r of users) expect(r.scope).toBe(`tenant:${tenant}`)
    // The session is not an administration call: it never carries the header.
    for (const r of m!.requests.filter((x) => x.url.startsWith('/api/session'))) expect(r.scope).toBeFalsy()

    // The whole site's console, in the same page, sends none.
    await click(byLabel(tc.back('')) ?? document.querySelector('aside a[href="/"]'))
    await settle(() => locationText() === '/', 'out of the console')
  })

  it('sends none in the whole site, and none on the tenant page except for the people, spaces and audit tabs', async () => {
    await open(() => '/admin/users/all')
    await settle(() => m!.requests.some((r) => r.url.startsWith('/api/users')), 'the people')
    expect(scoped()).toEqual([])

    // The tenant's page for the site: its overview is the site's call.
    await open((id) => `/admin/tenants/${id}`)
    await settle(() => document.querySelector('[data-slot="tenant-overview"]') !== null, 'the overview')
    expect(m!.requests.some((r) => r.url === `/api/admin/tenants/${tenant}/overview`)).toBe(true)
    expect(scoped()).toEqual([])
    // Its people read in its scope.
    const tab = [...document.querySelectorAll<HTMLElement>(`nav[aria-label="${tt.detail.tabsLabel}"] a`)].find(
      (a) => a.textContent === tt.detail.tabs.members,
    )!
    await click(tab)
    await settle(() => scoped().some((r) => r.url.startsWith('/api/users')), 'the members, in scope')
    expect(locationText()).toBe(`/admin/tenants/${tenant}/members`)
  })

  it('shows a banner that names the tenant and leads out to its page, and only what a tenant administrator sees', async () => {
    await open((id) => `/admin/t/${id}/users/all`)
    await settle(
      () => document.querySelector('[data-slot="scope-banner"]')?.textContent?.includes('Acme') === true,
      'the banner',
    )
    const banner = document.querySelector<HTMLElement>('[data-slot="scope-banner"]')!
    expect(banner.textContent).toContain(tt.scope.managing('Acme'))
    const exit = banner.querySelector<HTMLAnchorElement>('a')!
    expect(exit.getAttribute('href')).toBe(`/admin/tenants/${tenant}`)
    // The sidebar: no site-only page.
    const links = sidebarLinks()
    expect(links).toContain(tc.sections.users)
    expect(links).toContain(tc.sections.audit)
    for (const gone of [
      tc.sections.tenants,
      tc.sections.security,
      tc.sections.mail,
      tc.sections.status,
      tc.sections.daily,
    ])
      expect(links).not.toContain(gone)
    // Every link stays in the scope.
    for (const a of document.querySelectorAll<HTMLAnchorElement>(`aside nav[aria-label="${tc.nav}"] a`))
      expect(a.getAttribute('href')).toMatch(new RegExp(`^/admin/t/${tenant}/`))

    await click(exit)
    await settle(() => document.querySelector('[data-slot="scope-banner"]') === null, 'the banner is gone')
    expect(locationText()).toBe(`/admin/tenants/${tenant}`)
  })

  it('has no banner in the whole site, whose sidebar has the site pages', async () => {
    await open(() => '/admin/users/all')
    await settle(() => sidebarLinks().length > 0, 'the sidebar')
    expect(document.querySelector('[data-slot="scope-banner"]')).toBeNull()
    expect(sidebarLinks()).toContain(tc.sections.tenants)
  })

  it('does not open a site page inside a tenant scope', async () => {
    await open((id) => `/admin/t/${id}/tenants`)
    await settle(() => locationText() === `/admin/t/${tenant}/users`, 'sent to the people')
  })

  it('answers an unknown tenant as an address that does not exist', async () => {
    await open(() => '/admin/t/nope/users/all')
    await settle(
      () => document.querySelector('[data-slot="admin-layout"]') === null && document.body.textContent !== '',
      'not found',
    )
    expect(document.querySelector('[data-slot="scope-banner"]')).toBeNull()
  })
})

describe("a tenant's page", () => {
  it('shows the overview from one call, with the way into the console', async () => {
    await open((id) => `/admin/tenants/${id}/overview`)
    await settle(() => document.querySelector('[data-slot="tenant-overview"]') !== null, 'the overview')
    expect(document.querySelector('h1')!.textContent).toBe('Acme')
    const calls = m!.requests.filter((r) => r.url.includes(`/admin/tenants/${tenant}`))
    expect(calls.map((r) => r.url)).toEqual([`/api/admin/tenants/${tenant}/overview`])
    const manage = [...document.querySelectorAll<HTMLAnchorElement>('a')].find(
      (a) => a.textContent?.trim() === tt.scope.manageAs,
    )!
    expect(manage.getAttribute('href')).toBe(`/admin/t/${tenant}/users/all`)
    await click(manage)
    await settle(() => document.querySelector('[data-slot="scope-banner"]') !== null, 'in the console')
  })
})
