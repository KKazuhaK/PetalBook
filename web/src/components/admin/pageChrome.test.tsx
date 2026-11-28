// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// What every administration page shares: the title at its top, the brand and the version in the sidebar.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.restoreAllMocks()
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)
const open = (path: string) => mount(routes, { signIn: true, fixtures: { mode: 'accounts' }, entries: [path] })
const header = () => document.querySelector<HTMLElement>('[data-slot="admin-page-header"]')

describe('the page header', () => {
  it('has one h1, a line of description and, where the page has a primary action, the action on its right', async () => {
    m = await open('/admin/users')
    await settle(() => header() !== null, 'the header')
    expect(document.querySelectorAll('h1')).toHaveLength(1)
    expect(header()!.querySelector('h1')!.textContent).toBe(tc.sections.users)
    expect(header()!.querySelector('[data-slot="page-description"]')!.textContent).toBe(tc.pageHelp['users/all'])
    await settle(
      () =>
        header()!.querySelector('[data-slot="admin-page-actions"]')!.textContent === tc.users.create + tc.users.invite,
      'invite',
    )
  })

  it('is on every section, above the tab strip', async () => {
    for (const key of ['groups', 'spaces', 'settings', 'audit', 'status'] as const) {
      m = await open(`/admin/${key}`)
      await settle(() => header() !== null, key)
      expect(header()!.querySelector('h1')!.textContent).toBe(tc.sections[key])
      expect(header()!.querySelector('[data-slot="page-description"]')).not.toBeNull()
      await m.unmount()
      m = undefined
    }
  })
})

describe('the sidebar', () => {
  it('shows the logo and the name above the way back', async () => {
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="admin-brand"]') !== null, 'the brand')
    const brand = document.querySelector('[data-slot="admin-brand"]')!
    expect(brand.querySelector('img')!.getAttribute('src')).toBe('/favicon.svg')
    expect(brand.textContent).toBe(t.appName)
  })

  it('shows the version to a site administrator, with the commit and the time of the commit in their zone', async () => {
    m = await open('/admin/groups')
    await settle(() => document.querySelector('[data-slot="version-badge"]') !== null, 'the badge')
    await click(document.querySelector('[data-slot="version-badge"]'))
    await settle(() => document.querySelector('[data-slot="popover-content"]') !== null, 'the popover')
    const text = document.querySelector('[data-slot="popover-content"]')!.textContent!
    expect(text).toContain('fixture')
    expect(text).toContain('3f9c2ab')
    expect(text).toContain(tc.version.commitTime)
    expect(text).toContain(Intl.DateTimeFormat().resolvedOptions().timeZone)
    expect(text).not.toContain(tc.version.modified)
  })
})
