// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { setSite } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The sign-in policy an organisation decides inside what the site allows: sessions.*, signin.passkeyRoles,
// captcha.contexts and signin.mode are on the tenant's page, under the site's minimum the same way as the others.

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
const follow = (row: HTMLElement) => row.querySelector('[role="switch"][aria-label$="' + tt.settings.follow + '"]')

async function open(scene?: (d: FixtureData) => unknown) {
  m = await mount(routes, {
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => void scene?.(d) },
    entries: () => ['/admin/settings'],
  })
  await settle(() => rows('sessions.maxDays').length === 2, 'both layers')
}

describe('the sign-in policy on the tenant page', () => {
  it('has each key a tenant may decide, with the site at its own layer', async () => {
    await open()
    for (const key of [
      'signin.mode',
      'signin.passkeyRoles',
      'sessions.idleDays',
      'sessions.maxDays',
      'captcha.contexts',
    ]) {
      expect(rows(key), key).toHaveLength(2)
    }
    // The server-wide ones stay at the site's alone.
    for (const key of ['signin.lockout.enabled', 'signin.perMinute', 'captcha.provider']) {
      expect(rows(key), key).toHaveLength(1)
    }
    // A site-only mode never offers "minimum"; a key with an order does.
    const [siteMode, siteMax] = [rows('signin.mode')[0], rows('sessions.maxDays')[0]]
    const options = (row: HTMLElement) =>
      [...row.querySelector<HTMLSelectElement>(`select[aria-label$="${tt.settings.rule}"]`)!.options].map(
        (o) => o.value,
      )
    expect(options(siteMode)).not.toContain('tighten')
    expect(options(siteMax)).toContain('tighten')
  })

  it('bounds the lifetimes by the site (a minimum of days, at most)', async () => {
    await open()
    const [, max] = rows('sessions.maxDays')
    expect(max.textContent).toContain(tt.settings.tightenedBy)
    expect(max.textContent).toContain(tt.settings.stricterThan('90'))
    await click(follow(max))
    await settle(() => max.querySelector('input[type="number"]') !== null, 'the number')
    expect(max.querySelector('input[type="number"]')!.getAttribute('max')).toBe('90')
  })

  it('keeps the roles of the site: they cannot be dropped, more can be added', async () => {
    await open((d) => setSite(d, 'signin.passkeyRoles', ['admin'], { mode: 'tighten' }))
    const [, roles] = rows('signin.passkeyRoles')
    await click(follow(roles))
    await settle(() => roles.querySelector('input[type="checkbox"]') !== null, 'the boxes')
    const box = (role: string) =>
      [...roles.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')].find((b) =>
        b.parentElement!.textContent!.includes(tt.settings.choices['signin.passkeyRoles'][role]),
      )!
    expect(box('admin').checked).toBe(true)
    expect(box('admin').disabled).toBe(true)
    expect(box('member').disabled).toBe(false)
  })

  it("keeps the forms the site asks, and sends only an organisation's forms", async () => {
    await open((d) =>
      setSite(
        d,
        'captcha.contexts',
        {
          login: { on: true, when: 'always', failures: 3 },
          invite: { on: false, when: 'after_failures', failures: 3 },
          setup: { on: false, when: 'after_failures', failures: 3 },
          forgot: { on: false, when: 'after_failures', failures: 3 },
          register: { on: false, when: 'after_failures', failures: 3 },
        },
        { mode: 'tighten' },
      ),
    )
    const [, captcha] = rows('captcha.contexts')
    await click(follow(captcha))
    await settle(() => captcha.querySelector('[data-slot="captcha-forms"]') !== null, 'the forms')
    const form = (name: string) => captcha.querySelector<HTMLElement>(`[data-form="${name}"]`)!
    // The site asks login always: it cannot be switched off or asked less often.
    expect(form('login').querySelector('[role="switch"]')!.getAttribute('aria-disabled')).toBe('true')
    const when = form('login').querySelector<HTMLSelectElement>('select')!
    expect([...when.options].map((o) => [o.value, o.disabled])).toEqual([
      ['always', false],
      ['after_failures', true],
    ])
    // Only login, forgot and register are an organisation's.
    expect([...captcha.querySelectorAll('[data-form]')].map((f) => f.getAttribute('data-form'))).toEqual([
      'login',
      'forgot',
      'register',
    ])
    await click(form('register').querySelector('[role="switch"]'))
    await click([...captcha.querySelectorAll('button')].find((b) => b.textContent?.trim() === tt.settings.save))
    await settle(() => captcha.textContent!.includes(tt.settings.saved), 'saved')
    const put = m!.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/settings/captcha.contexts'))!
    const sent = JSON.parse(put.body!).value as Record<string, { on: boolean }>
    expect(Object.keys(sent).sort()).toEqual(['forgot', 'login', 'register'])
    expect(sent.register.on).toBe(true)
    expect(sent.login.on).toBe(true)
  })
})
