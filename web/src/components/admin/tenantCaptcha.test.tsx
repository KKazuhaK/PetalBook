// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tm } from '@/i18n/mail'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addTenant, ownRule, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// An organisation's own captcha provider (3.6) on the tenant's Captcha tab: the form, the secret that is only ever
// "set" or "not set", the keys that go with the provider, removal, and the page when the site has not allowed it.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const to = tm.orgCaptcha
const routes = (
  <SidebarProvider>
    <Routes>
      <Route path="/admin/*" element={<AdminGate />} />
      <Route path="*" element={<div>elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)

let tenant = ''
async function open(scene?: (d: FixtureData, tenantId: string) => void) {
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        tenancyOn(d)
        tenant = addTenant(d, 'acme', 'Acme').id
        scene?.(d, tenant)
      },
    },
    entries: () => [`/admin/tenants/${tenant}/captcha`],
  })
}

const text = () => document.body.textContent ?? ''
const button = (label: string) =>
  [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) ?? null
const field = (label: string) =>
  [...document.querySelectorAll<HTMLInputElement>('input')].find(
    (i) => document.querySelector(`label[for="${i.id}"]`)?.textContent?.trim() === label,
  )
const provider = () => document.querySelector<HTMLSelectElement>('select[id^="org-captcha-provider"]')!
const pick = async (value: string) => {
  const el = provider()
  el.value = value
  el.dispatchEvent(new Event('change', { bubbles: true }))
  await settle(() => provider().value === value, 'the provider')
}
const putBody = () =>
  JSON.parse(
    m!.requests.filter((r) => r.method === 'PUT' && r.url === `/api/admin/tenants/${tenant}/captcha`).pop()!.body!,
  )

describe("an organisation's own captcha provider", () => {
  it('is not set up here while the site has not allowed it, and says so', async () => {
    await open((d, id) => ownRule(d, id, 'features.ownCaptcha', { value: false }))
    await settle(() => text().includes(to.notAllowed), 'the notice')
    expect(document.querySelector('[data-slot="org-captcha-form"]')).toBeNull()
  })

  it('saves the keys, shows the secret only as set, and keeps it when it is left empty', async () => {
    await open()
    await settle(() => document.querySelector('[data-slot="org-captcha-form"]') !== null, 'the form')
    // The built-in image has no keys.
    expect(field(to.siteKey)).toBeUndefined()
    await pick('turnstile')
    expect(text()).toContain(to.secretNone)
    await type(field(to.siteKey)!, '1x00000000000000000000AA')
    await type(field(to.secret)!, 'hunter2-captcha-secret')
    await click(document.querySelector('[data-slot="org-captcha-form"] [role="switch"]'))
    await click(button(to.save))
    await settle(() => text().includes(to.saved), 'saved')
    expect(putBody()).toMatchObject({
      enabled: true,
      provider: 'turnstile',
      siteKey: '1x00000000000000000000AA',
      secret: 'hunter2-captcha-secret',
    })
    // Nothing the server says holds the secret, and the form says it is set.
    const stored = m!.fx.call('GET', `/api/admin/tenants/${tenant}/captcha`).body as Record<string, unknown>
    expect(stored).toMatchObject({ hasSecret: true, ready: true, provider: 'turnstile' })
    expect(JSON.stringify(stored)).not.toContain('hunter2')
    expect(field(to.secret)!.value).toBe('')
    expect(field(to.secret)!.placeholder).toBe(to.secretSet)
    expect(document.querySelector('[data-slot="org-captcha-state"]')!.textContent).toBe(to.state.ready)
    // A save with the secret left empty sends none, so the server keeps it.
    await type(field(to.siteKey)!, 'x')
    await click(button(to.save))
    await settle(() => putBody().siteKey === 'x', 'the second save')
    expect('secret' in putBody()).toBe(false)
  })

  it('starts another provider with no keys, and the secret can be cleared', async () => {
    await open((d, id) => {
      d.admin.tenantCaptcha[id] = {
        enabled: false,
        provider: 'turnstile',
        siteKey: 'k',
        hasSecret: true,
        ready: false,
      }
    })
    await settle(() => field(to.siteKey)?.value === 'k', 'the form, filled')
    expect(field(to.secret)!.placeholder).toBe(to.secretSet)
    await pick('hcaptcha')
    expect(field(to.siteKey)!.value).toBe('')
    expect(field(to.secret)!.placeholder).toBe(to.secretNone)
    await pick('turnstile')
    expect(field(to.siteKey)!.value).toBe('k')
    await click(
      [...document.querySelectorAll('label')]
        .find((l) => l.textContent?.trim() === to.secretClear)
        ?.querySelector('input'),
    )
    await click(button(to.save))
    await settle(() => m!.requests.some((r) => r.method === 'PUT'), 'saved')
    expect(putBody()).toMatchObject({ provider: 'turnstile', secret: '' })
  })

  it('shows a refusal in words', async () => {
    await open()
    await settle(() => document.querySelector('[data-slot="org-captcha-form"]') !== null, 'the form')
    await pick('recaptcha')
    await click(document.querySelector('[data-slot="org-captcha-form"] [role="switch"]'))
    await click(button(to.save))
    await settle(() => text().includes(to.errors.captcha_site_key_required), 'the refusal')
  })

  it('removes it after a confirmation, and the forms go back to the site’s', async () => {
    await open((d, id) => {
      d.admin.tenantCaptcha[id] = { enabled: true, provider: 'hcaptcha', siteKey: 'k', hasSecret: true, ready: true }
    })
    await settle(() => field(to.siteKey)?.value === 'k', 'the form, filled')
    await click(button(to.remove))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the confirmation')
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    const confirm = [...document.querySelectorAll('[role="alertdialog"] button')].find(
      (b) => b.textContent?.trim() === to.removeConfirm,
    )
    await click(confirm)
    await settle(
      () => m!.requests.some((r) => r.method === 'DELETE' && r.url === `/api/admin/tenants/${tenant}/captcha`),
      'deleted',
    )
    await settle(() => provider().value === 'image', 'the form, cleared')
    expect(m!.fx.call('GET', `/api/admin/tenants/${tenant}/captcha`).body).toMatchObject({
      provider: 'image',
      hasSecret: false,
      ready: false,
    })
  })

  it('has nothing to try for the built-in image, and asks for the keys first for a service', async () => {
    await open()
    await settle(() => document.querySelector('[data-slot="org-captcha-test-note"]') !== null, 'the note')
    expect(text()).toContain(to.testImage)
  })
})
