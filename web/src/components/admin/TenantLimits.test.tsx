// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import { tu } from '@/i18n/usage'
import { absoluteTime } from '@/lib/time'
import { adminRequests, click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// A tenant's limits sheet: each key with where its value comes from and its window, the edit dialog, and the
// warning when a dated value replaces an undated one, against the mock server.

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
const tx = tt.limits
const TENANT = '/api/admin/tenants/0000000000/limits'
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')!
const sheet = () => document.querySelector<HTMLElement>('[data-slot="tenant-limits"]')!
// The table is the wide layout's, the list the phone's: the same key is in both, so look in the table's.
const rowOf = (key: string) => sheet().querySelector<HTMLElement>(`.max-sm\\:hidden [data-key="${key}"]`)!

async function openSheet(scene: (server: MockServer) => void) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts', empty: true },
    entries: (server) => {
      const admin = adminRequests(server)
      admin('PUT', '/api/admin/tenancy', { enabled: true })
      scene(server)
      return ['/admin/tenants']
    },
  })
  await settle(() => button(`${tt.list.manage}: Default`) !== null, 'the list')
  await click(button(`${tt.list.manage}: Default`))
  await settle(
    () => document.querySelector('[data-slot="tenant-limits"] [data-key="other.people"]') !== null,
    'the sheet',
  )
}

const FROM = '2020-01-01T00:00:00Z'
const UNTIL = '2099-01-01T00:00:00Z'

describe('the limits sheet of a tenant', () => {
  it('shows each key with its source, a window in force, what lies under it, and a value that waits', async () => {
    await openSheet((server) => {
      const admin = adminRequests(server)
      admin('PUT', TENANT, {
        values: {
          'other.people': { value: 7 },
          'storage.quota.spaces': { value: 3, from: FROM, until: UNTIL },
          'storage.quota.pages': { value: 9, from: '2098-01-01T00:00:00Z', until: UNTIL },
        },
      })
    })
    // An undated manual value: its source, and no window.
    expect(rowOf('other.people').getAttribute('data-source')).toBe('manual')
    expect(rowOf('other.people').textContent).toContain(tx.sources.manual)
    expect(rowOf('other.people').textContent).toContain('7')
    // A value inside its window: the window, and what it falls back to when the window ends.
    const inForce = rowOf('storage.quota.spaces')
    expect(inForce.getAttribute('data-source')).toBe('manual')
    expect(inForce.textContent).toContain(tx.window.both(absoluteTime(FROM), absoluteTime(UNTIL)))
    expect(inForce.textContent).toContain(tx.then(tu.noLimit, tx.sources.site))
    // A value whose window has not begun: the site's value applies, and the waiting one is shown.
    const waiting = rowOf('storage.quota.pages')
    expect(waiting.getAttribute('data-source')).toBe('site')
    expect(waiting.textContent).toContain(
      tx.pending('9', tx.window.both(absoluteTime('2098-01-01T00:00:00Z'), absoluteTime(UNTIL))),
    )
    // A key nobody set follows the site, with no window lines.
    expect(rowOf('storage.quota.bytes').getAttribute('data-source')).toBe('site')
    expect(rowOf('storage.quota.bytes').textContent).not.toContain(tx.window.from(absoluteTime(FROM)))
  })

  it('edits one key in a dialog that starts from the manual value, and removes it', async () => {
    await openSheet((server) => {
      adminRequests(server)('PUT', TENANT, { values: { 'other.people': { value: 7 } } })
    })
    await click(button(tx.editFor('other.people'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="limit-form"]') !== null, 'the form')
    const field = dlg().querySelector<HTMLInputElement>('#limit-value')!
    expect(field.value).toBe('7')
    await type(field, '12')
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    const puts = () => m!.requests.filter((r) => r.method === 'PUT' && r.url === TENANT)
    expect(JSON.parse(puts()[0].body!)).toEqual({ values: { 'other.people': { value: 12 } } })
    await settle(() => rowOf('other.people').textContent!.includes('12'), 'the new value')
    // A value that was never typed in is refused in the form, before anything is sent.
    await click(button(tx.editFor('other.people'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="limit-form"]') !== null, 'the form again')
    await type(dlg().querySelector<HTMLInputElement>('#limit-value')!, 'many')
    await click(button(tx.save, dlg()))
    await settle(() => dlg().textContent!.includes(tx.invalidValue), 'the refusal')
    expect(puts()).toHaveLength(1)
    // Remove takes the manual value away: the key follows the site again.
    await click(button(tx.remove, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'removed')
    expect(JSON.parse(puts()[1].body!)).toEqual({ values: { 'other.people': null } })
    await settle(() => rowOf('other.people').getAttribute('data-source') === 'site', 'the site value is back')
  })

  it('says so when a dated value replaced the undated one', async () => {
    await openSheet((server) => {
      adminRequests(server)('PUT', TENANT, { values: { 'storage.quota.spaces': { value: 5 } } })
    })
    await click(button(tx.editFor('storage.quota.spaces'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="limit-form"]') !== null, 'the form')
    // A window: the dialog warns before saving that it replaces the undated value.
    await type(dlg().querySelector<HTMLInputElement>('#limit-until')!, '2099-01-01T00:00')
    expect(dlg().textContent).toContain(tx.replaceNote)
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved')
    // After it, the sheet tells which key was replaced.
    expect(sheet().textContent).toContain(tx.replaced('storage.quota.spaces'))
    // A write that replaces nothing leaves no notice.
    await click(button(tx.editFor('other.people'), sheet()))
    await settle(() => dlg()?.querySelector('[data-slot="limit-form"]') !== null, 'the second form')
    await type(dlg().querySelector<HTMLInputElement>('#limit-value')!, '4')
    await click(button(tx.save, dlg()))
    await settle(() => document.querySelector('[role="dialog"]') === null, 'saved the second')
    expect(sheet().textContent).not.toContain(tx.replaced('storage.quota.spaces'))
  })
})
