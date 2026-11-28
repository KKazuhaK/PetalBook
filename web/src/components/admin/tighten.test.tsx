// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { setSite } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The lower mode tighten and the notes of rules, on the Settings page against the fixture server: the site offers
// "minimum" only for a key that has an order of strictness, a tenant sees the site's note and what its own value may
// not be looser than, and the controls grey out what the server would refuse.

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
const rows = (key: string) => [
  ...document.querySelectorAll<HTMLElement>(`[data-slot="setting-row"][data-key="${key}"]`),
]
const button = (label: string, root: ParentNode) =>
  [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) ?? null

async function open(scene?: (d: FixtureData) => unknown) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void scene?.(d) },
    entries: () => ['/admin/settings'],
  })
  await settle(() => rows('registration.perHour').length === 2, 'both layers')
}

describe('a minimum from the site', () => {
  it('offers the mode only where the key has an order, and sends the note with the rule', async () => {
    await open()
    // storage.partial.unsupported has an order (allow, refuse) and allows everything to begin with.
    const [site] = rows('storage.partial.unsupported')
    const rule = site.querySelector<HTMLSelectElement>(`select[aria-label$="${tt.settings.rule}"]`)!
    expect([...rule.options].map((o) => o.value)).toContain('tighten')
    const [daily] = rows('daily.own')
    const dailyRule = daily.querySelector<HTMLSelectElement>(`select[aria-label$="${tt.settings.rule}"]`)!
    expect([...dailyRule.options].map((o) => o.value)).not.toContain('tighten')
    // No note field while everything is allowed; one appears with the rule.
    const note = () => site.querySelector(`input[aria-label$="${tt.settings.noteLabel}"]`)
    expect(note()).toBeNull()
    await type(rule, 'tighten')
    await type(note() as HTMLInputElement, 'Company policy')
    await click(button(tt.settings.save, site))
    await settle(() => site.textContent!.includes(tt.settings.saved), 'saved')
    expect(
      JSON.parse(
        m!.requests.find((r) => r.method === 'PUT' && r.url === '/api/admin/settings/storage.partial.unsupported')!
          .body!,
      ),
    ).toEqual({ lower: { mode: 'tighten', note: 'Company policy' } })
  })

  it('shows a tenant the note and the floor, and greys out the looser choices and numbers', async () => {
    await open((d) => {
      setSite(d, 'registration.perHour', 20, { mode: 'tighten', note: 'Company policy: at most 20.' })
      setSite(d, 'storage.partial.unsupported', 'refuse', { mode: 'tighten' })
    })
    const [, perHour] = rows('registration.perHour')
    expect(perHour.textContent).toContain(tt.settings.tightenedBy)
    expect(perHour.textContent).toContain(tt.settings.noteOf('Company policy: at most 20.'))
    expect(perHour.textContent).toContain(tt.settings.stricterThan('20'))
    await click(perHour.querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]'))
    await settle(() => perHour.querySelector('input[type="number"]') !== null, 'the number')
    expect(perHour.querySelector('input[type="number"]')!.getAttribute('max')).toBe('20')
    const [, mode] = rows('storage.partial.unsupported')
    await click(mode.querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]'))
    await settle(() => mode.querySelector('select') !== null, 'the choice')
    const options = [
      ...mode.querySelectorAll<HTMLOptionElement>(
        `select[aria-label="${tt.settings.names['storage.partial.unsupported']}"] option`,
      ),
    ]
    expect(options.map((o) => [o.value, o.disabled])).toEqual([
      ['allow', true],
      ['refuse', false],
    ])
  })

  it('says a tenant value the site now overrides is overridden, not just kept', async () => {
    await open((d) => {
      setSite(d, 'registration.perHour', 20, { mode: 'tighten' })
      d.admin.tenantRules['0000000000'] = {
        'registration.perHour': { value: 30, lower: null, inEffect: true, siteLocked: false },
      }
    })
    const [, perHour] = rows('registration.perHour')
    await settle(() => perHour.querySelector('[data-slot="not-in-effect"]') !== null, 'marked')
    expect(perHour.textContent).toContain(tt.settings.overriddenBySite)
  })

  it("takes no rule at the site for a key that governs the site's own people, and shows no badge under an allowing rule", async () => {
    await open((d) => {
      // The site's own layer holds nobody for it; a rule for this tenant that allows everything holds nobody either.
      setSite(d, 'registration.mode', 'closed', { mode: 'lock' })
      d.admin.tenantRules['0000000000'] = {
        'registration.perHour': {
          value: null,
          lower: null,
          inEffect: true,
          siteLocked: false,
          rules: [{ source: 'manual', value: null, lower: { mode: 'allow' }, inEffect: true }],
        },
      }
      setSite(d, 'registration.perHour', 20, { mode: 'tighten' })
    })
    const [site, tenant] = rows('registration.mode')
    expect(site.querySelector('[data-slot="own-people-only"]')).not.toBeNull()
    expect(site.querySelector(`select[aria-label$="${tt.settings.rule}"]`)).toBeNull()
    expect(tenant.textContent).not.toContain(tt.settings.lockedBy.site)
    const [, perHour] = rows('registration.perHour')
    expect(perHour.textContent).not.toContain(tt.settings.tightenedBy)
  })
})
