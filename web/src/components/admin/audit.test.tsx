// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tsec } from '@/i18n/security'
import { click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'
import { auditFiltersOf } from './AuditSection'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.unstubAllGlobals()
})

const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)
const q = <T extends HTMLElement = HTMLElement>(sel: string) => document.querySelector<T>(sel)
const chip = (c: string) => q<HTMLButtonElement>(`[data-slot="audit-categories"] [data-category="${c}"]`)!
const listCalls = () => m!.requests.filter((r) => r.method === 'GET' && r.url.startsWith('/api/admin/audit?'))
const lastList = () => listCalls().at(-1)?.url ?? ''

async function open(path: string) {
  m = await mount(routes, { entries: [path], signIn: true, server: { mode: 'accounts' } })
  await settle(() => q('#audit-kind') !== null && lastList() !== '', 'the audit log')
}

describe('the filters of the audit log page', () => {
  it('reads the filters from the address, the same names as the API, and ignores what is not one', () => {
    expect(
      auditFiltersOf(
        new URLSearchParams(
          'category=security,nope,data&kind=login&from=2026-11-01&to=yesterday&capacity=root&tenant=T1',
        ),
      ),
    ).toEqual({
      category: ['security', 'data'],
      kind: 'login',
      user: '',
      actor: '',
      capacity: '',
      from: '2026-11-01',
      to: '',
      tenant: 'T1',
    })
  })

  it('asks for what the address says, and a link reproduces the view', async () => {
    await open('/admin/audit?category=security,data&kind=login_failed&from=2026-11-01')
    expect(lastList()).toBe('/api/admin/audit?category=security%2Cdata&kind=login_failed&from=2026-11-01&limit=50')
    expect(chip('security').getAttribute('aria-pressed')).toBe('true')
    expect(chip('admin').getAttribute('aria-pressed')).toBe('false')
    expect(q<HTMLSelectElement>('#audit-kind')!.value).toBe('login_failed')
    expect(q<HTMLInputElement>('#audit-from')!.value).toBe('2026-11-01')
    // The CSV is the same filter, by link.
    expect(q<HTMLAnchorElement>('[data-slot="audit-download"]')!.getAttribute('href')).toBe(
      '/api/admin/audit/download?category=security%2Cdata&kind=login_failed&from=2026-11-01',
    )
  })

  it('toggles a category, narrows by kind from the groups of the registry, and clears', async () => {
    await open('/admin/audit')
    expect(chip('').getAttribute('aria-pressed')).toBe('true')
    await click(chip('admin'))
    await settle(() => lastList().includes('category=admin'), 'the category')
    expect(chip('admin').getAttribute('aria-pressed')).toBe('true')
    await click(chip('data'))
    await settle(() => lastList().includes('category=admin%2Cdata'), 'two categories')
    // The kinds offered are those of the chosen categories, grouped by category.
    const groups = [...document.querySelectorAll('#audit-kind optgroup')].map((g) => g.getAttribute('label'))
    expect(groups).toEqual([tsec.audit.categories.admin, tsec.audit.categories.data])
    await type(q<HTMLSelectElement>('#audit-kind')!, 'space_deleted')
    await settle(() => lastList().includes('kind=space_deleted'), 'the kind')
    const clear = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === tsec.audit.clear)!
    await click(clear)
    // The unfiltered page is in the cache, so nothing is asked again: the controls show it.
    await settle(() => chip('').getAttribute('aria-pressed') === 'true', 'cleared')
    expect(q<HTMLSelectElement>('#audit-kind')!.value).toBe('')
  })

  it('says that an optional category is off, and where it is switched', async () => {
    await open('/admin/audit')
    await settle(() => chip('reads').hasAttribute('data-off'), 'the registry')
    expect(chip('reads').textContent).toContain(tsec.audit.categoryOff)
    expect(chip('reads').getAttribute('title')).toBe(tsec.audit.categoryOffHint('audit.reads'))
    expect(chip('signins').hasAttribute('data-off')).toBe(false)
    expect(chip('security').hasAttribute('data-off')).toBe(false)
  })
})
