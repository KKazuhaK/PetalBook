// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tm } from '@/i18n/mail'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { addTenant, ownRule, setSite, tenancyOn } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// An organisation's own mail server (3.6) on the tenant's Mail tab: the form, the password that is only ever "set" or
// "not set", a test message, removal, the page when the site has not allowed it, and mail.useSite with a site's lock.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const to = tm.org
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
    entries: () => [`/admin/tenants/${tenant}/mail`],
  })
}

const text = () => document.body.textContent ?? ''
const root = () => document.querySelector<HTMLElement>('[data-slot="tenant-mail"]')
const button = (label: string) =>
  [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) ?? null
const field = (label: string) =>
  [...document.querySelectorAll<HTMLInputElement>('input')].find(
    (i) => document.querySelector(`label[for="${i.id}"]`)?.textContent?.trim() === label,
  )!
const allowed = (d: FixtureData, id: string) => ownRule(d, id, 'mail.ownSender', { value: true })
const sw = (label: string) => document.querySelector<HTMLElement>(`[role="switch"][aria-label="${label}"]`)

describe("an organisation's own mail server", () => {
  it('is not set up here while the site has not allowed it, and says so', async () => {
    await open()
    await settle(() => root() !== null && text().includes(to.notAllowed), 'the notice')
    expect(document.querySelector('[data-slot="org-mail-form"]')).toBeNull()
    // Whether it may borrow the site's mail is still the organisation's to read.
    expect(sw(to.useSite.label)).not.toBeNull()
  })

  it('saves the settings, shows the password only as set, and sends a test message', async () => {
    await open(allowed)
    await settle(() => field(to.host) !== undefined, 'the form')
    expect(text()).toContain(to.passwordNone)
    await type(field(to.host), 'smtp.acme.example')
    await type(field(to.username), 'bot')
    await type(field(to.password), 'hunter2-secret')
    await type(field(to.from), 'hello@acme.example')
    await click(document.querySelector('[data-slot="org-mail-form"] [role="switch"]'))
    await click(button(to.save))
    await settle(() => text().includes(to.saved), 'saved')
    const put = m!.requests.find((r) => r.method === 'PUT' && r.url === `/api/admin/tenants/${tenant}/mail`)!
    expect(JSON.parse(put.body!)).toMatchObject({
      enabled: true,
      host: 'smtp.acme.example',
      security: 'starttls',
      password: 'hunter2-secret',
      from: 'hello@acme.example',
    })
    // Nothing the server says holds the password, and the form says it is set.
    const stored = m!.fx.call('GET', `/api/admin/tenants/${tenant}/mail`).body as Record<string, unknown>
    expect(stored).toMatchObject({ hasPassword: true, sender: 'tenant' })
    expect(JSON.stringify(stored)).not.toContain('hunter2')
    expect(field(to.password).value).toBe('')
    expect(field(to.password).placeholder).toBe(to.passwordSet)
    expect(document.querySelector('[data-slot="org-mail-sender"]')!.textContent).toBe(to.sender.tenant)

    await type(field(to.testTo), 'me@example.test')
    await click(button(to.testSend))
    await settle(() => text().includes(to.testOk), 'the test result')
    expect(m!.fx.sent.mail).toEqual([{ to: 'me@example.test', kind: 'test' }])
  })

  it('security is starttls or tls only, and a refusal is shown in words', async () => {
    await open(allowed)
    await settle(() => field(to.host) !== undefined, 'the form')
    const options = [...document.querySelectorAll<HTMLOptionElement>('select[id^="org-mail-security"] option')]
    expect(options.map((o) => o.value)).toEqual(['starttls', 'tls'])
    await type(field(to.host), '127.0.0.1')
    await click(button(to.save))
    await settle(() => text().includes(to.errors.mail_host_not_public), 'the refusal')
  })

  it('removes it after a confirmation, and its messages go back to the site’s', async () => {
    await open((d, id) => {
      allowed(d, id)
      d.admin.mail.enabled = true
      d.admin.tenantMail[id] = {
        enabled: true,
        host: 'smtp.acme.example',
        port: 587,
        security: 'starttls',
        username: 'bot',
        hasPassword: true,
        from: 'hello@acme.example',
        fromName: '',
        replyTo: '',
        status: { ok: false, checked: null, message: '' },
        sender: 'tenant',
      }
    })
    await settle(() => field(to.host)?.value === 'smtp.acme.example', 'the form, filled')
    expect(field(to.password).placeholder).toBe(to.passwordSet)
    await click(button(to.remove))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the confirmation')
    expect(m!.requests.some((r) => r.method === 'DELETE')).toBe(false)
    const confirm = [...document.querySelectorAll('[role="alertdialog"] button')].find(
      (b) => b.textContent?.trim() === to.removeConfirm,
    )
    await click(confirm)
    await settle(
      () => m!.requests.some((r) => r.method === 'DELETE' && r.url === `/api/admin/tenants/${tenant}/mail`),
      'deleted',
    )
    await settle(() => field(to.host).value === '', 'the form, cleared')
    const stored = m!.fx.call('GET', `/api/admin/tenants/${tenant}/mail`).body as Record<string, unknown>
    expect(stored).toMatchObject({ host: '', hasPassword: false, sender: 'site' })
  })
})

describe('borrowing the site’s mail (mail.useSite)', () => {
  it('is on by default, and the organisation turns it off', async () => {
    await open(allowed)
    await settle(() => sw(to.useSite.label) !== null, 'the switch')
    expect(sw(to.useSite.label)!.getAttribute('aria-checked')).toBe('true')
    await click(sw(to.useSite.label))
    await settle(
      () =>
        m!.requests.some((r) => r.method === 'PUT' && r.url === `/api/admin/tenants/${tenant}/settings/mail.useSite`),
      'the write',
    )
    const put = m!.requests.find((r) => r.method === 'PUT' && r.url.endsWith('/settings/mail.useSite'))!
    expect(JSON.parse(put.body!)).toEqual({ value: false })
  })

  it('is fixed while the site has locked it, with the site’s note', async () => {
    await open((d, id) => {
      allowed(d, id)
      setSite(d, 'mail.useSite', true)
      ownRule(d, id, 'mail.useSite', {
        value: false,
        siteLocked: true,
        rules: [
          {
            source: 'manual',
            value: false,
            lower: { mode: 'lock', note: 'Use your own.' },
            inEffect: true,
            setBy: 'root',
          },
        ],
      })
    })
    await settle(() => sw(to.useSite.label) !== null, 'the switch')
    expect(sw(to.useSite.label)!.getAttribute('aria-checked')).toBe('false')
    expect(sw(to.useSite.label)!.getAttribute('aria-disabled')).toBe('true')
    const rule = document.querySelector('[data-slot="org-mail-usesite-rule"]')!.textContent!
    expect(rule).toContain(to.useSite.lockedOff)
    expect(rule).toContain('Use your own.')
  })
})
