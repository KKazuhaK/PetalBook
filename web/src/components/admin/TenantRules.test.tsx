// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import { absoluteTime } from '@/lib/time'
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

// A tenant's rules: each setting with where its value comes from and its window, the edit dialog, and the warning
// when a dated rule replaces an undated one, against the mock server.

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
const tx = tt.rules
const PUT = (key: string) => `/api/admin/tenants/0000000000/settings/${key}`
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const sheet = () => document.querySelector<HTMLElement>('[data-slot="tenant-rules"]')!
// The table is the wide layout's, the list the phone's: the same key is in both, so look in the table's.
const rowOf = (key: string) => sheet().querySelector<HTMLElement>(`.max-sm\\:hidden [data-key="${key}"]`)!

async function openSheet(scene: (server: MockServer) => void) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts', empty: true },
    entries: (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/settings/features.tenancy', { value: true })
      scene(server)
      return ['/admin/tenants']
    },
  })
  await openTenantPage(`${tt.list.open}: Default`, tt.detail.tabs.plan)
  await settle(
    () => document.querySelector('[data-slot="tenant-rules"] [data-key="quota.people"]') !== null,
    'the rules',
  )
}

const FROM = '2020-01-01T00:00:00Z'
const UNTIL = '2099-01-01T00:00:00Z'

describe('the rules of a tenant', () => {
  it('shows each setting with its source, a window in force, what lies under it, and a rule that waits', async () => {
    await openSheet((server) => {
      const admin = adminRequests(server)
      admin('PUT', PUT('quota.people'), { siteRule: { value: 7 } })
      admin('PUT', PUT('quota.spaces'), { siteRule: { value: 3, from: FROM, until: UNTIL } })
      admin('PUT', PUT('quota.pages'), { siteRule: { value: 9, from: '2098-01-01T00:00:00Z', until: UNTIL } })
    })
    // An undated manual rule: its source, and no window.
    expect(rowOf('quota.people').getAttribute('data-source')).toBe('manual')
    expect(rowOf('quota.people').textContent).toContain(tx.sources.manual)
    expect(rowOf('quota.people').textContent).toContain('7')
    // A rule inside its window: the window, and when it changes next.
    const inForce = rowOf('quota.spaces')
    expect(inForce.getAttribute('data-source')).toBe('manual')
    expect(inForce.textContent).toContain(tx.window.both(absoluteTime(FROM), absoluteTime(UNTIL)))
    expect(inForce.textContent).toContain(tx.next(absoluteTime(UNTIL)))
    // A rule whose window has not begun: the site's value applies, and the waiting one is shown.
    const waiting = rowOf('quota.pages')
    expect(waiting.getAttribute('data-source')).toBe('site')
    expect(waiting.textContent).toContain(
      tx.waiting('9', tx.window.both(absoluteTime('2098-01-01T00:00:00Z'), absoluteTime(UNTIL))),
    )
    // A setting nobody set for the tenant follows the site, with no window lines.
    expect(rowOf('quota.bytes').getAttribute('data-source')).toBe('site')
    expect(rowOf('quota.bytes').textContent).not.toContain(tx.window.from(absoluteTime(FROM)))
  })

  it('edits one setting in a dialog that starts from the manual rule, and removes it', async () => {
    await openSheet((server) => {
      adminRequests(server)('PUT', PUT('quota.people'), { siteRule: { value: 7 } })
    })
    await click(button(tx.editFor('quota.people'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the form')
    const field = dlg().querySelector<HTMLInputElement>('#rule-value')!
    expect(field.value).toBe('7')
    await type(field, '12')
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    const puts = () => m!.requests.filter((r) => r.method === 'PUT' && r.url === PUT('quota.people'))
    expect(JSON.parse(puts()[0].body!)).toEqual({ siteRule: { value: 12 } })
    await settle(() => rowOf('quota.people').textContent!.includes('12'), 'the new value')
    // A value that was never typed in is refused in the form, before anything is sent.
    await click(button(tx.editFor('quota.people'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the form again')
    await type(dlg().querySelector<HTMLInputElement>('#rule-value')!, 'many')
    await click(button(tx.save, dlg()))
    await settle(() => dlg().textContent!.includes(tx.invalidValue), 'the refusal')
    expect(puts()).toHaveLength(1)
    // Remove takes the manual rule away: the setting follows the site again.
    await click(button(tx.remove, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'removed')
    expect(JSON.parse(puts()[1].body!)).toEqual({ siteRule: null })
    await settle(() => rowOf('quota.people').getAttribute('data-source') === 'site', 'the site value is back')
  })

  it('refuses a window that ends before it begins, and a rule that gives nothing, before anything is sent', async () => {
    await openSheet(() => {})
    await click(button(tx.editFor('features.adminView'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the form')
    await type(dlg().querySelector<HTMLInputElement>('#rule-from')!, '2099-01-02T00:00')
    await type(dlg().querySelector<HTMLInputElement>('#rule-until')!, '2099-01-01T00:00')
    await click(button(tx.save, dlg()))
    await settle(() => dlg().textContent!.includes(tx.untilBeforeFrom), 'the refusal')
    expect(m!.requests.some((r) => r.method === 'PUT' && r.url === PUT('features.adminView'))).toBe(false)
  })

  it('says so when a dated rule replaced the undated one', async () => {
    await openSheet((server) => {
      adminRequests(server)('PUT', PUT('quota.spaces'), { siteRule: { value: 5 } })
    })
    await click(button(tx.editFor('quota.spaces'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the form')
    // A window: the dialog warns before saving that it replaces the undated rule.
    await type(dlg().querySelector<HTMLInputElement>('#rule-until')!, '2099-01-01T00:00')
    expect(dlg().textContent).toContain(tx.replaceNote)
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    // After it, the block tells which setting was replaced.
    expect(sheet().textContent).toContain(tx.replaced(tt.settings.names['quota.spaces']))
    // A write that replaces nothing leaves no notice.
    await click(button(tx.editFor('quota.people'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="rule-form"]') !== null, 'the second form')
    await type(dlg().querySelector<HTMLInputElement>('#rule-value')!, '4')
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved the second')
    expect(sheet().textContent).not.toContain(tx.replaced(tt.settings.names['quota.spaces']))
  })
})
