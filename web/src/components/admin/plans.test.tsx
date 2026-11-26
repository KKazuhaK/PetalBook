// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import {
  adminRequests,
  click,
  installDomShims,
  mount,
  settle,
  type,
  type Mounted,
  openTenantPage,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The plans page (list, make, edit a plan's rules, the cap a limit must have), the plan of a tenant with the
// confirmation that lists the manual rules a change clears, and the site settings grouped by key prefix, against the
// mock server.

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
const tp = tt.plans
const tx = tt.rules
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const writes = (method: string, url: string) =>
  m!.requests.filter((r) => r.method === method && r.url === url).map((r) => JSON.parse(r.body ?? 'null'))

async function open(
  path: string,
  scene: (server: MockServer) => void = () => {},
  delay?: (method: string, url: string) => number,
) {
  m = await mount(routes, {
    signIn: true,
    delay,
    server: { mode: 'accounts', empty: true },
    entries: (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/settings/features.tenancy', { value: true })
      scene(server)
      return [path]
    },
  })
}
describe('the plans page', () => {
  it('lists the plans, makes one, and opens it', async () => {
    await open('/admin/tenants/plans', (server) => {
      adminRequests(server)('POST', '/api/admin/plans', { slug: 'team', name: 'Team', rules: {} })
    })
    await settle(() => document.querySelector('[data-plan="team"]') !== null, 'the list')
    expect(document.body.textContent).toContain('Team')
    await click(button(tp.create))
    await settle(() => dlg() !== null, 'the dialog')
    const inputs = dlg().querySelectorAll<HTMLInputElement>('input')
    await type(inputs[0], 'Business')
    await type(inputs[1], 'business')
    await click(button(tp.create, dlg()))
    await settle(() => document.querySelector('[data-slot="plan-detail"][data-plan="business"]') !== null, 'its page')
    expect(writes('POST', '/api/admin/plans')[0]).toMatchObject({ slug: 'business', name: 'Business' })
  })

  it('needs a maximum or a lock for a limit a tenant also sets, before anything is sent', async () => {
    await open('/admin/tenants/plans', (server) => {
      adminRequests(server)('POST', '/api/admin/plans', { slug: 'team', name: 'Team', rules: {} })
    })
    await settle(() => document.querySelector('[data-plan="team"]') !== null, 'the list')
    await click(button(tp.manageFor('Team')))
    await settle(() => button(tp.addRule) !== null, 'the plan')
    await click(button(tp.addRule))
    await settle(() => dlg()?.querySelector('#plan-rule-key') !== null, 'the picker')
    await type(dlg().querySelector<HTMLSelectElement>('#plan-rule-key')!, 'registration.perHour')
    await settle(() => dlg().querySelector('#rule-value') !== null, 'the value')
    await type(dlg().querySelector<HTMLInputElement>('#rule-value')!, '20')
    // The form proposes a restriction; asking for no rule at all is what the server would refuse.
    await type(dlg().querySelector<HTMLSelectElement>('select[aria-label="' + tx.lowerLabel + '"]')!, 'allow')
    await click(button(tx.save, dlg()))
    await settle(() => dlg().textContent!.includes(tx.needsCap), 'the refusal')
    expect(writes('PATCH', '/api/admin/plans/team')).toHaveLength(0)
    // A lock caps it.
    await type(dlg().querySelector<HTMLSelectElement>('select[aria-label="' + tx.lowerLabel + '"]')!, 'lock')
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    expect(writes('PATCH', '/api/admin/plans/team')[0]).toEqual({
      rules: { 'registration.perHour': { value: 20, lower: { mode: 'lock' } } },
    })
    await settle(() => document.querySelector('[data-key="registration.perHour"]') !== null, 'the rule in the list')
    // Taking it out sends null for the key.
    await click(button(tp.removeFor('registration.perHour')))
    // It leaves every tenant on the plan, so it asks first, naming the setting and the tenants.
    await settle(() => document.querySelector('[data-slot="rule-remove"]') !== null, 'the confirmation')
    expect(dlg().textContent).toContain(tp.removeTitle(tt.settings.names['registration.perHour']))
    expect(dlg().textContent).toContain(tp.removeHelp(0))
    expect(button(tp.remove, dlg())!.querySelector('svg')).toBeNull()
    expect(writes('PATCH', '/api/admin/plans/team')).toHaveLength(1)
    await click(button(tp.cancel, dlg()))
    await settle(() => document.querySelector('[data-slot="rule-remove"]') === null, 'cancelled')
    expect(writes('PATCH', '/api/admin/plans/team')).toHaveLength(1)
    await click(button(tp.removeFor('registration.perHour')))
    await settle(() => document.querySelector('[data-slot="rule-remove"]') !== null, 'asked again')
    await click(button(tp.remove, dlg()))
    await settle(() => document.querySelector('[data-key="registration.perHour"]') === null, 'removed')
    expect(writes('PATCH', '/api/admin/plans/team')[1]).toEqual({ rules: { 'registration.perHour': null } })
  })

  it('refuses a plan a tenant still uses, with the count', async () => {
    await open('/admin/tenants/plans', (server) => {
      const admin = adminRequests(server)
      admin('POST', '/api/admin/plans', { slug: 'team', name: 'Team', rules: {} })
      admin('PUT', '/api/admin/tenants/0000000000/plan', { plan: 'team' })
    })
    await settle(() => document.querySelector('[data-plan="team"]') !== null, 'the list')
    await click(button(tp.deleteFor('Team')))
    await settle(() => dlg() !== null, 'the dialog')
    await click(button(tp.delete, dlg()))
    await settle(() => dlg().textContent!.includes(tp.inUse(1)), 'the refusal')
  })
})

describe('the plan of a tenant', () => {
  it('asks before a change, lists the manual rules it clears, and says what was cleared', async () => {
    await open('/admin/tenants', (server) => {
      const admin = adminRequests(server)
      admin('POST', '/api/admin/plans', { slug: 'team', name: 'Team', rules: {} })
      admin('PUT', '/api/admin/tenants/0000000000/settings/quota.people', { siteRule: { value: 7 } })
    })
    await openTenantPage(`${tt.list.open}: Default`, tt.detail.tabs.plan)
    await settle(
      () => document.querySelector('[data-slot="tenant-plan"] option[value="team"]') !== null,
      'the plan block',
    )
    const select = document.querySelector<HTMLSelectElement>('[data-slot="tenant-plan"] select')!
    expect(button(tp.tenantPlan.change)!.hasAttribute('disabled')).toBe(true)
    await type(select, 'team')
    await click(button(tp.tenantPlan.change))
    await settle(() => document.querySelector('[data-slot="plan-change"]') !== null, 'the confirmation')
    // It lists what will go, and nothing is sent until it is confirmed.
    expect(dlg().querySelector('[data-slot="to-clear"] [data-key="quota.people"]')).not.toBeNull()
    expect(dlg().textContent).toContain(tp.tenantPlan.changeHelp)
    expect(writes('PUT', '/api/admin/tenants/0000000000/plan')).toHaveLength(0)
    await click(button(tp.tenantPlan.confirm, dlg()))
    await settle(() => document.querySelector('[data-slot="plan-change"]') === null, 'changed')
    expect(writes('PUT', '/api/admin/tenants/0000000000/plan')[0]).toEqual({ plan: 'team' })
    expect(document.querySelector('[data-slot="tenant-plan"]')!.textContent).toContain(
      tp.tenantPlan.cleared(tt.settings.names['quota.people']),
    )
    // The manual rule is gone: the setting no longer says manual.
    await settle(
      () =>
        document.querySelector('[data-slot="tenant-rules"] [data-key="quota.people"]')?.getAttribute('data-source') !==
        'manual',
      'the rule cleared',
    )
  })
})

describe('the plan change confirmation while the rules load', () => {
  it('cannot be confirmed until the tenant rules are read', async () => {
    await open(
      '/admin/tenants',
      (server) => adminRequests(server)('POST', '/api/admin/plans', { slug: 'team', name: 'Team', rules: {} }),
      (method, url) => (method === 'GET' && url.startsWith('/api/admin/tenants/0000000000/settings') ? 400 : 0),
    )
    await openTenantPage(`${tt.list.open}: Default`, tt.detail.tabs.plan)
    await settle(
      () => document.querySelector('[data-slot="tenant-plan"] option[value="team"]') !== null,
      'the plan block',
    )
    await type(document.querySelector<HTMLSelectElement>('[data-slot="tenant-plan"] select')!, 'team')
    await click(button(tp.tenantPlan.change))
    await settle(() => document.querySelector('[data-slot="plan-change"]') !== null, 'the confirmation')
    expect(dlg().querySelector('[data-slot="rules-loading"]')).not.toBeNull()
    expect(dlg().querySelector('[data-slot="nothing-to-clear"]')).toBeNull()
    expect(button(tp.tenantPlan.confirm, dlg())!.hasAttribute('disabled')).toBe(true)
    await settle(() => dlg().querySelector('[data-slot="rules-loading"]') === null, 'the rules')
    expect(button(tp.tenantPlan.confirm, dlg())!.hasAttribute('disabled')).toBe(false)
    expect(writes('PUT', '/api/admin/tenants/0000000000/plan')).toHaveLength(0)
  })
})

describe('the site settings', () => {
  it('are grouped by the first word of their key, with a strip to jump to a group', async () => {
    await open('/admin/settings')
    await settle(() => document.querySelector('[data-slot="setting-group"]') !== null, 'the groups')
    const groups = [...document.querySelectorAll('[data-slot="setting-group"]')].map((g) =>
      g.getAttribute('data-group'),
    )
    expect(groups.slice(0, 3)).toEqual(['features', 'quota', 'rate'])
    // Each row is in the group of its key's prefix.
    const row = document.querySelector('[data-slot="setting-row"][data-key="cache.partial"]')!
    expect(row.closest('[data-slot="setting-group"]')!.getAttribute('data-group')).toBe('cache')
    // A button of the strip for each group.
    const strip = [...document.querySelectorAll('[data-slot="setting-groups"] button')].map((b) =>
      b.getAttribute('data-group'),
    )
    expect(strip).toEqual(groups)
  })
})
