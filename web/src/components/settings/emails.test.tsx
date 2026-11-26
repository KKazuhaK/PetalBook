// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { MockServer } from '@/api/mock/server'
import { tm } from '@/i18n/mail'
import { t } from '@/i18n'
import {
  adminRequests,
  byLabel,
  byText,
  click,
  installDomShims,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// More than one address per account (accounts.multipleEmails), against the mock server: off, the one address as before;
// on, the list with what each address is, adding one, making a verified one primary and removing one.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
    <Route path="*" element={<div>elsewhere</div>} />
  </Routes>
)
const list = () => document.querySelector<HTMLElement>('[data-slot="email-list"]')
const row = (email: string) => list()?.querySelector<HTMLElement>(`[data-email="${email}"]`) ?? null

// Mail on and the primary address set; `multiple` turns the switch on, and `second` adds a confirmed second address.
async function open(server: (s: MockServer) => void = () => undefined, wait = () => list() !== null) {
  m = await mount(routes, {
    entries: (s) => {
      const admin = adminRequests(s)
      admin('PUT', '/api/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' })
      admin('PATCH', '/api/account', { email: 'admin@example.test' })
      server(s)
      return ['/settings/profile']
    },
    signIn: true,
    server: { mode: 'accounts' },
  })
  await settle(wait, 'the page')
}
const multiple = (s: MockServer) =>
  adminRequests(s)('PUT', '/api/admin/settings/accounts.multipleEmails', { value: true })
const second = (s: MockServer) => {
  multiple(s)
  const admin = adminRequests(s)
  admin('POST', '/api/account/emails', { email: 'work@example.test' })
  const token = s.mail.outbox.find((x) => x.to === 'work@example.test')!.token!
  admin('POST', '/api/email/confirm', { token })
}

describe('the email addresses of an account', () => {
  it('shows the one address as before while the switch is off', async () => {
    await open(
      () => undefined,
      () => document.querySelector('[data-slot="email-verification"]') !== null,
    )
    expect(list()).toBeNull()
    expect(byText('button', tm.emails.add)).toBeNull()
  })

  it('adds an address: it is sent the link, and shows unverified with nothing but Verify and Remove', async () => {
    await open(multiple)
    expect(row('admin@example.test')!.textContent).toContain(tm.emails.primary)
    await click(byText('button', tm.emails.add))
    await settle(() => document.querySelector('#add-email-form input') !== null, 'the dialog')
    await type(document.querySelector<HTMLInputElement>('#add-email-form input')!, 'two@example.test')
    await click(byText('button', tm.emails.send))
    await settle(() => row('two@example.test') !== null, 'the new address')
    expect(m!.server.mail.outbox.filter((x) => x.kind === 'verify').map((x) => x.to)).toEqual(['two@example.test'])
    expect(row('two@example.test')!.textContent).toContain(tm.emails.unverified)
    expect(byLabel(t.fmt.labelled(tm.emails.verify, 'two@example.test'))).not.toBeNull()
    expect(byLabel(t.fmt.labelled(tm.emails.remove, 'two@example.test'))).not.toBeNull()
    expect(byLabel(t.fmt.labelled(tm.emails.makePrimary, 'two@example.test'))).toBeNull()
    // The primary is never removed.
    expect(byLabel(t.fmt.labelled(tm.emails.remove, 'admin@example.test'))).toBeNull()
  })

  it('makes a verified address primary, then removes the old one after asking', async () => {
    await open(second, () => row('work@example.test') !== null)
    expect(row('work@example.test')!.textContent).toContain(tm.emails.verified)
    await click(byLabel(t.fmt.labelled(tm.emails.makePrimary, 'work@example.test')))
    await settle(() => row('work@example.test')!.textContent!.includes(tm.emails.primary), 'the new primary')
    expect(row('admin@example.test')!.textContent).not.toContain(tm.emails.primary)
    await click(byLabel(t.fmt.labelled(tm.emails.remove, 'admin@example.test')))
    await settle(() => byText('[role="alertdialog"] button', tm.emails.remove) !== null, 'the question')
    await click(byText('[role="alertdialog"] button', tm.emails.remove))
    await settle(() => row('admin@example.test') === null, 'the removal')
    expect(m!.requests.some((r) => r.method === 'DELETE' && r.url === '/api/account/emails/admin%40example.test')).toBe(
      true,
    )
  })
})
