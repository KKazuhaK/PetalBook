// @vitest-environment jsdom
import { act } from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, locationText, mount, settle, type FxMounted } from '@/testing/harness'
import { addTenant, asTenantAdmin, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The console's scope selector: the site and every tenant the person administers, chosen from the header. A choice is
// an address, so the scope survives a reload, and the requests of each scope carry their own header.

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

const ids: Record<string, string> = {}
async function open(path: (id: Record<string, string>) => string, scene: (d: FixtureData) => void) {
  await m?.unmount()
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        tenancyOn(d)
        ids.acme = addTenant(d, 'acme', 'Acme').id
        ids.beta = addTenant(d, 'beta', 'Beta Labs').id
        scene(d)
      },
    },
    entries: () => [path(ids)],
  })
}

// The selector as a person sees it: its button (or, with one place only, its name).
const trigger = () => document.querySelector<HTMLElement>('[data-slot="scope-bar"] [data-slot="dropdown-menu-trigger"]')
const bar = () => document.querySelector<HTMLElement>('[data-slot="scope-bar"]')
const items = () => [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')]
const label = (el: Element) => el.querySelector('[data-slot="scope-name"]')?.textContent
const choose = async (name: string) => {
  await settle(() => trigger() !== null, 'the selector')
  await click(trigger())
  await settle(() => items().some((i) => label(i) === name), `the choice ${name}`)
  await click(items().find((i) => label(i) === name))
  await settle(() => items().length === 0, 'the menu closed')
}
const adminCalls = () => m!.requests.filter((r) => r.url.startsWith('/api/users') || r.url.startsWith('/api/invites'))

// A site administrator who administers two tenants of their own besides.
const dual = (d: FixtureData) => void d.admin.scopeTenants.push(ids.acme, ids.beta)

describe("the console's scope selector", () => {
  it('lists the site, then the tenants the person administers by name, and the current one is checked', async () => {
    await open(() => '/admin/users/all', dual)
    await settle(() => trigger() !== null, 'the selector')
    expect(trigger()!.getAttribute('aria-label')).toBe(tt.scope.selector.current(tt.scope.selector.site))
    await click(trigger())
    await settle(() => items().length === 3, 'the three choices')
    expect(items().map(label)).toEqual([tt.scope.selector.site, 'Acme', 'Beta Labs'])
    expect(items().map((i) => i.getAttribute('aria-checked'))).toEqual(['true', 'false', 'false'])
  })

  it("switching changes the requests' scope header, and back leaves none", async () => {
    await open(() => '/admin/users/all', dual)
    await settle(() => adminCalls().length > 0, 'the people of the site')
    for (const r of adminCalls()) expect(r.scope).toBeFalsy()

    await choose('Acme')
    await settle(() => locationText() === `/admin/t/${ids.acme}/users`, 'in Acme')
    await settle(() => adminCalls().some((r) => r.scope === `tenant:${ids.acme}`), "Acme's people, in its scope")
    expect(trigger()!.getAttribute('aria-label')).toBe(tt.scope.selector.current('Acme'))

    await choose('Beta Labs')
    await settle(() => locationText() === `/admin/t/${ids.beta}/users`, 'in Beta')
    await settle(() => adminCalls().some((r) => r.scope === `tenant:${ids.beta}`), "Beta's people, in its scope")

    const before = m!.requests.length
    await choose(tt.scope.selector.site)
    await settle(() => locationText() === '/admin/users', 'back in the site')
    // The site's own cache is kept (fresh), and read again it carries no scope; the tenants' are not shown here.
    await act(() => m!.qc.refetchQueries({ type: 'all' }))
    expect(m!.requests.length).toBeGreaterThan(before)
    // Nothing read after the way back carries a scope, and the scope list itself never did.
    expect(m!.requests.slice(before).filter((r) => r.scope)).toEqual([])
    for (const r of m!.requests.filter((x) => x.url.startsWith('/api/admin/scopes'))) expect(r.scope).toBeFalsy()
  })

  it('keeps the scope in the address: a reload, or a shared link, opens the console in it', async () => {
    await open((id) => `/admin/t/${id.beta}/users/all`, dual)
    await settle(() => trigger()?.getAttribute('aria-label') === tt.scope.selector.current('Beta Labs'), 'Beta chosen')
    await settle(() => adminCalls().some((r) => r.scope === `tenant:${ids.beta}`), "Beta's people")
    expect(locationText()).toBe(`/admin/t/${ids.beta}/users/all`)
  })

  it("lets a tenant's administrator who holds a role in another tenant act there, and refuses any other", async () => {
    await open(
      (id) => `/admin/t/${id.acme}/users/all`,
      (d) => {
        asTenantAdmin(d)
        d.admin.scopeTenants.push(ids.acme)
      },
    )
    await settle(() => adminCalls().some((r) => r.scope === `tenant:${ids.acme}`), "Acme's people")
    expect(locationText()).toBe(`/admin/t/${ids.acme}/users/all`)
    await click(trigger())
    await settle(() => items().length === 2, 'the choices')
    expect(items().map(label)).toEqual(['Acme', 'Default'])

    // A tenant they hold nothing in is not theirs to enter: the address goes to their own console.
    await open(
      (id) => `/admin/t/${id.beta}/users/all`,
      (d) => {
        asTenantAdmin(d)
        d.admin.scopeTenants.push(ids.acme)
      },
    )
    await settle(() => locationText() === '/admin/users/all', 'sent to their own console')
    expect(adminCalls().filter((r) => r.scope)).toEqual([])
    // Their own tenant is the console without a scope.
    await choose('Acme')
    await settle(() => locationText() === `/admin/t/${ids.acme}/users`, 'in Acme')
    await choose('Default')
    await settle(() => locationText() === '/admin/users', 'in their own')
  })

  it('shows the one tenant a tenant administrator has as its name, without a menu', async () => {
    await open(() => '/admin/users/all', asTenantAdmin)
    await settle(() => bar() !== null, 'the strip')
    expect(bar()!.textContent).toBe('Default')
    expect(trigger()).toBeNull()
    expect(bar()!.querySelector('[role="group"]')?.getAttribute('aria-label')).toBe(tt.scope.selector.label)
  })

  it("shows a site administrator who administers no tenant of their own nothing, until they enter one's scope", async () => {
    await open(
      () => '/admin/users/all',
      () => {},
    )
    await settle(() => adminCalls().length > 0, 'the people')
    expect(bar()).toBeNull()
    // Entered from a tenant's page: the way back is in the menu.
    await open(
      (id) => `/admin/t/${id.acme}/users/all`,
      () => {},
    )
    await settle(() => trigger()?.getAttribute('aria-label') === tt.scope.selector.current('Acme'), 'Acme chosen')
    await click(trigger())
    await settle(() => items().length === 2, 'the choices')
    expect(items().map(label)).toEqual([tt.scope.selector.site, 'Acme'])
  })

  it('truncates a long name instead of widening the page, and keeps it whole in the title', async () => {
    const long = 'An organisation with a very long name that would not fit in any header'
    await open(
      () => '/admin/users/all',
      (d) => {
        d.admin.tenants.find((t) => t.id === ids.acme)!.name = long
        dual(d)
      },
    )
    await settle(() => trigger() !== null, 'the selector')
    await choose(long)
    await settle(() => trigger()?.getAttribute('aria-label') === tt.scope.selector.current(long), 'chosen')
    expect(trigger()!.title).toBe(long)
    expect(trigger()!.querySelector('span')?.className).toContain('truncate')
  })
})
