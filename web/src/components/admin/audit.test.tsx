// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tsec } from '@/i18n/security'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'
import { auditFiltersOf } from './AuditSection'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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
  m = await mount(routes, {
    entries: [path],
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void (d.features['features.auditDownload'] = true) },
  })
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
    expect(q('[data-slot="audit-download-note"]')!.textContent).toMatch(/10 万|100,000/)
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

describe('purging the audit log by hand', () => {
  const dialog = () => q('[data-slot="audit-purge-dialog"]')
  const countText = () => q('[data-slot="audit-purge-count"]')?.textContent ?? ''
  const posts = () => m!.requests.filter((r) => r.method === 'POST' && r.url.startsWith('/api/admin/audit/purge'))
  const button = (text: string) =>
    [...document.querySelectorAll('[data-slot="audit-purge-dialog"] button')].find(
      (b) => b.textContent?.trim() === text,
    )

  it('counts with a dry run, says what will go, and deletes only when confirmed', async () => {
    await open('/admin/audit')
    await click(q('[data-slot="audit-purge-open"]'))
    await settle(() => dialog() !== null && countText().includes('5'), 'the count of every row')
    // Nothing was deleted by looking: only a dry run was asked, and the button names the number.
    expect(posts().map((r) => r.url)).toEqual(['/api/admin/audit/purge?dry_run=true'])
    expect(button(tsec.audit.purge.confirm(5))).toBeDefined()
    await type(dialog()!.querySelector('select')!, 'security')
    await settle(
      () => countText() === tsec.audit.purge.will(2, tsec.audit.categories.security, ''),
      'the security count',
    )
    expect(button(tsec.audit.purge.confirm(2))).toBeDefined()
    await click(button(tsec.audit.purge.confirm(2)))
    // The purge is a job: the dialog follows it and says what it did, then closes on request.
    await settle(
      () => q('[data-slot="audit-purge-result"]')?.textContent === tsec.audit.purge.done(2),
      'the result of the job',
    )
    await click(button(tsec.audit.purge.close))
    await settle(() => dialog() === null, 'the dialog closing')
    const real = posts().filter((r) => !r.url.includes('dry_run'))
    expect(real).toHaveLength(1)
    expect(JSON.parse(real[0].body!)).toEqual({ category: 'security' })
    // The log is read again and shows the one record of the purge.
    await settle(
      () => q('[data-slot="audit-rows"]')?.textContent?.includes(tsec.audit.kinds.audit_purged) === true,
      'the record',
    )
  })

  it('does not delete when cancelled', async () => {
    await open('/admin/audit')
    await click(q('[data-slot="audit-purge-open"]'))
    await settle(() => countText().includes('5'), 'the count')
    await click(button(tsec.audit.purge.cancel))
    await settle(() => dialog() === null, 'the dialog closing')
    expect(posts().filter((r) => !r.url.includes('dry_run'))).toHaveLength(0)
  })
})
