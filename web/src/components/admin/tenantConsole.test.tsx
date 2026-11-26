// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, locationText, mount, settle, type FxMounted } from '@/testing/harness'
import { addTenant, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// A tenant's page for the site (/admin/tenants/<id>/<tab>) and the console in its scope (/admin/t/<id>/...): the scope
// header goes out only from the scope, the banner says so and leads out, and the sidebar is what a tenant's
// administrator has.

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

let tenant = ''
async function open(path: (id: string) => string) {
  await m?.unmount()
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        tenancyOn(d)
        d.features['features.auditDownload'] = true
        tenant = addTenant(d, 'acme', 'Acme').id
      },
    },
    entries: () => [path(tenant)],
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
    // The page speaks of the tenant, not of the server.
    await settle(
      () =>
        document.querySelector('[data-slot="page-description"]')?.textContent ===
        tc.pageHelpTenant['users/all']('Acme'),
      'the tenant-worded help',
    )
    // The session is not an administration call: it never carries the header.
    for (const r of m!.requests.filter((x) => x.url.startsWith('/api/session'))) expect(r.scope).toBeFalsy()

    // Out of the console (once it has loaded: the way back leads to the space shown last, known once the spaces are
    // read), nothing carries the header, even what the site's cache reads again.
    const backLink = () =>
      [...document.querySelectorAll<HTMLAnchorElement>('aside a[aria-label]')].find((a) =>
        a.getAttribute('aria-label')!.startsWith(tc.back('').trim()),
      )
    await settle(() => backLink() !== undefined, 'the console')
    const before = m!.requests.length
    await click(backLink())
    await settle(() => !locationText().startsWith('/admin'), 'out of the console')
    await act(() => m!.qc.invalidateQueries())
    await act(() => m!.qc.refetchQueries({ type: 'all' }))
    expect(m!.requests.length).toBeGreaterThan(before)
    expect(m!.requests.slice(before).filter((r) => r.scope)).toEqual([])
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

    // The page around the tab is the site's: its overview and the server's status, read again while the members
    // show, carry no header.
    const before = m!.requests.length
    await act(() => m!.qc.invalidateQueries())
    const site = () =>
      m!.requests
        .slice(before)
        .filter((r) => r.url === `/api/admin/tenants/${tenant}/overview` || r.url.startsWith('/api/admin/status'))
    await settle(
      () => site().some((r) => r.url.includes('/overview')) && site().some((r) => r.url.includes('/status')),
      'the overview and the status, again',
    )
    for (const r of site()) expect(r.scope, r.url).toBeFalsy()
  })

  it("downloads the audit of the scope it shows: the tenant's tab names the scope in the address, the site's not", async () => {
    await open((id) => `/admin/tenants/${id}/audit`)
    await settle(() => document.querySelector('[data-slot="audit-download"]') !== null, 'the download')
    const scopedHref = document.querySelector<HTMLAnchorElement>('[data-slot="audit-download"]')!.getAttribute('href')!
    expect(new URL(scopedHref, 'http://x').searchParams.get('scope')).toBe(`tenant:${tenant}`)

    await open(() => '/admin/audit')
    await settle(() => document.querySelector('[data-slot="audit-download"]') !== null, 'the download')
    const siteHref = document.querySelector<HTMLAnchorElement>('[data-slot="audit-download"]')!.getAttribute('href')!
    expect(new URL(siteHref, 'http://x').searchParams.has('scope')).toBe(false)
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

    const before = m!.requests.length
    await click(exit)
    await settle(() => document.querySelector('[data-slot="tenant-overview"]') !== null, 'the tenant page')
    expect(document.querySelector('[data-slot="scope-banner"]')).toBeNull()
    expect(locationText()).toBe(`/admin/tenants/${tenant}`)
    // After Exit, no request carries the header.
    expect(m!.requests.slice(before).length).toBeGreaterThan(0)
    expect(m!.requests.slice(before).filter((r) => r.scope)).toEqual([])
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

  it('shows the stored counts as not measured before the first measurement, and the administrators live', async () => {
    await open((id) => `/admin/tenants/${id}/overview`)
    await settle(() => document.querySelector('[data-slot="tenant-overview"]') !== null, 'the overview')
    const stat = (label: string) => document.querySelector(`[data-stat="${label}"] dd`)!.textContent
    expect(stat(tt.detail.overview.people)).toBe(tt.detail.overview.notMeasured)
    expect(stat(tt.detail.overview.spaces)).toBe(tt.detail.overview.notMeasured)
    expect(stat(tt.detail.overview.pages)).toBe(tt.detail.overview.notMeasured)
    expect(stat(tt.detail.overview.admins)).toBe('0')
  })
})
