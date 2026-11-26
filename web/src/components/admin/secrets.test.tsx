// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tt } from '@/i18n/tenants'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import { AdminGate } from './AdminGate'

// The secrets of the site and of a tenant on the settings page: write-only, so they show whether one is set and never
// a value, and they are replaced in a dialog whose field starts empty, or cleared after a question.

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
const ts = tt.secrets
const rowOf = (name: string, scope: 'site' | 'tenant' = 'site') =>
  document.querySelector<HTMLElement>(
    `section[aria-labelledby="secrets-${scope}-heading"] [data-slot="secret"][data-name="${name}"]`,
  )
const button = (root: ParentNode | null, label: string) =>
  ([...(root?.querySelectorAll('button') ?? [])].find(
    (b) => b.textContent?.trim() === label || b.getAttribute('aria-label') === label,
  ) ?? null) as HTMLButtonElement | null
const dlg = () => document.querySelector<HTMLElement>('[role="dialog"]')
const writes = () =>
  m!.requests.filter((r) => r.method !== 'GET' && r.url.includes('/secrets')).map((r) => `${r.method} ${r.url}`)

// The GitHub App is set up in this scene; `scene` makes the rest.
const open = (scene: (fx: FxMounted['fx']) => void = () => undefined) =>
  mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      data: (d) => {
        for (const name of ['github.appKey', 'github.clientSecret', 'github.webhookSecret'])
          d.admin.secrets.site[name] = { name, set: true }
      },
    },
    entries: (fx) => {
      scene(fx)
      return ['/admin/settings']
    },
  })

describe('the secrets of the site', () => {
  it('lists the names the site may hold as set or not, with no value, and the GitHub ones as changed elsewhere', async () => {
    m = await open()
    await settle(() => rowOf('mail.password') !== null, 'the secrets')
    expect(rowOf('mail.password')!.textContent).toContain(ts.notSet)
    expect(rowOf('captcha.secret')!.textContent).toContain(ts.notSet)
    // The GitHub App is set up in this scene: listed as set, with no way to change it here.
    for (const name of ['github.appKey', 'github.clientSecret', 'github.webhookSecret']) {
      expect(rowOf(name)!.textContent).toContain(ts.set)
      expect(rowOf(name)!.textContent).toContain(ts.managedBy)
      expect(rowOf(name)!.querySelectorAll('button')).toHaveLength(0)
    }
  })

  it('replaces one in a dialog whose field starts empty, never shows it again, and clears it after a question', async () => {
    m = await open()
    await settle(() => rowOf('mail.password') !== null, 'the secrets')
    await click(button(rowOf('mail.password'), ts.replaceFor(ts.names['mail.password'])))
    await settle(() => dlg() !== null, 'the dialog')
    const field = dlg()!.querySelector<HTMLInputElement>('input')!
    expect(field.type).toBe('password')
    expect(field.value).toBe('')
    // Nothing to send without a value.
    expect(button(dlg(), ts.save)!.disabled).toBe(true)
    await type(field, 'smtp-secret-1')
    await click(button(dlg(), ts.save))
    await settle(() => rowOf('mail.password')!.textContent!.includes(ts.set), 'set')
    expect(writes()).toEqual(['PUT /api/admin/secrets/mail.password'])
    expect(JSON.parse(m.requests.find((r) => r.method === 'PUT')!.body!)).toEqual({ value: 'smtp-secret-1' })
    expect(rowOf('mail.password')!.textContent).toContain(ts.changed('').trim())
    // Not on the page, not in the list the server answered, and not in the dialog when it is opened again.
    await settle(() => dlg() === null, 'the dialog is closed')
    expect(document.body.innerHTML).not.toContain('smtp-secret-1')
    expect(JSON.stringify(m.fx.call('GET', '/api/admin/secrets').body)).not.toContain('smtp-secret-1')
    await click(button(rowOf('mail.password'), ts.replaceFor(ts.names['mail.password'])))
    await settle(() => dlg() !== null, 'the dialog again')
    expect(dlg()!.querySelector<HTMLInputElement>('input')!.value).toBe('')
    await click(button(dlg(), ts.cancel))
    await settle(() => dlg() === null, 'closed')

    await click(button(rowOf('mail.password'), ts.clearFor(ts.names['mail.password'])))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await click(button(document.querySelector('[role="alertdialog"]'), ts.confirmClear))
    await settle(() => rowOf('mail.password')!.textContent!.includes(ts.notSet), 'cleared')
    expect(writes()).toEqual(['PUT /api/admin/secrets/mail.password', 'DELETE /api/admin/secrets/mail.password'])
    // Nothing to clear when none is set.
    expect(button(rowOf('mail.password'), ts.clearFor(ts.names['mail.password']))).toBeNull()
  })
})

describe('the secrets of a tenant', () => {
  it('holds only the mail password, and not while the site has not allowed the tenant its own sender', async () => {
    m = await open()
    await settle(() => rowOf('mail.password', 'tenant') !== null, 'the tenant secrets')
    const row = rowOf('mail.password', 'tenant')!
    expect(row.textContent).toContain(ts.notSet)
    expect(
      document.querySelectorAll('section[aria-labelledby="secrets-tenant-heading"] [data-slot="secret"]'),
    ).toHaveLength(1)
    // The feature is off for the tenant: the message says so and Replace cannot be used.
    await settle(() => row.querySelector('[data-slot="feature-off"]') !== null, 'the message')
    expect(row.querySelector('[data-slot="feature-off"]')!.textContent).toBe(ts.featureOff)
    expect(button(row, ts.replaceFor(ts.names['mail.password']))!.disabled).toBe(true)
    expect(writes()).toEqual([])
  })

  it('is replaced and cleared once the site allows the tenant its own sender', async () => {
    m = await open((fx) => {
      fx.call('PUT', '/api/admin/tenants/0000000000/settings/mail.ownSender', { siteRule: { value: true } })
    })
    await settle(() => rowOf('mail.password', 'tenant') !== null, 'the tenant secrets')
    const row = () => rowOf('mail.password', 'tenant')!
    await settle(() => button(row(), ts.replaceFor(ts.names['mail.password']))?.disabled === false, 'allowed')
    expect(row().querySelector('[data-slot="feature-off"]')).toBeNull()
    await click(button(row(), ts.replaceFor(ts.names['mail.password'])))
    await settle(() => dlg() !== null, 'the dialog')
    await type(dlg()!.querySelector<HTMLInputElement>('input')!, 'tenant-pass')
    await click(button(dlg(), ts.save))
    await settle(() => row().textContent!.includes(ts.set), 'set')
    expect(writes()).toEqual(['PUT /api/admin/tenants/0000000000/secrets/mail.password'])
    expect(document.body.innerHTML).not.toContain('tenant-pass')
  })
})
