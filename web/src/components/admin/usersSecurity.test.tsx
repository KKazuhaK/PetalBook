// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tsec } from '@/i18n/security'
import {
  adminRequests,
  byLabel,
  click,
  installDomShims,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'
import { AdminGate } from './AdminGate'
import { MOCK_ADMIN } from '@/api/mock/server'

// The users page: passkeys, the email and what a person has.

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
      <Route path="/admin/:section?" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const calls = (method: string, url: string) => m!.requests.filter((r) => r.method === method && r.url.startsWith(url))

// A person with a passkey, made before the page opens.
async function open(server: { passkeysAvailable?: boolean } = {}) {
  m = await mount(routes, { entries: ['/admin/users'], signIn: true, server: { mode: 'accounts', ...server } })
  const carol = makePerson(m.server, 'carol')
  const ceremony = (
    carol.request('POST', '/api/account/passkeys/begin', { name: 'Lost phone' }).body as { ceremony: string }
  ).ceremony
  carol.request('POST', `/api/account/passkeys/finish?ceremony=${ceremony}`, { id: 'cred-carol' })
  await m.qc.invalidateQueries({ queryKey: ['users'] })
  await settle(() => text().includes('carol'), 'the list')
  return carol
}

describe('users and their sign-in', () => {
  it('shows how many passkeys and single sign-on identities each person has', async () => {
    await open()
    await settle(
      () =>
        [...document.querySelectorAll('[data-slot="user-counts"]')].some(
          (e) => e.textContent === tsec.users.counts(1, 0),
        ),
      'the counts',
    )
  })

  it('requiring a passkey is one switch, sent on its own', async () => {
    const carol = await open()
    await settle(
      () => document.querySelector(`[aria-label="${tsec.users.passkeyRequiredFor('名 carol')}"]`) !== null,
      'the switch',
    )
    await click(byLabel(tsec.users.passkeyRequiredFor('名 carol')))
    await settle(() => calls('PATCH', `/api/users/${carol.id}`).length === 1, 'patched')
    expect(JSON.parse(calls('PATCH', `/api/users/${carol.id}`)[0].body!)).toEqual({ passkeyRequired: true })
    await settle(
      () => byLabel(tsec.users.passkeyRequiredFor('名 carol'))?.getAttribute('aria-checked') === 'true',
      'on',
    )
  })

  it("a passkey required by the person's role shows as on, cannot be switched off, and says why", async () => {
    await open()
    // The administrator has a passkey first, so the requirement does not hold their own session.
    const admin = adminRequests(m!.server)
    const ceremony = (admin('POST', '/api/account/passkeys/begin', { name: 'Laptop' }).body as { ceremony: string })
      .ceremony
    admin('POST', `/api/account/passkeys/finish?ceremony=${ceremony}`, { id: 'cred-admin' })
    admin('PUT', '/api/admin/security', { passkeys: { requireRoles: ['admin'] } })
    await m!.qc.invalidateQueries()
    const sw = (name: string) => byLabel(tsec.users.passkeyRequiredFor(name))
    await settle(() => sw(MOCK_ADMIN.name)?.getAttribute('aria-disabled') === 'true', 'the switch, held by the role')
    expect(sw(MOCK_ADMIN.name)!.getAttribute('aria-checked')).toBe('true')
    expect(sw(MOCK_ADMIN.name)!.closest('li')!.querySelector('[data-slot="passkey-by-role"]')?.textContent).toBe(
      tsec.users.passkeyByRole,
    )
    // A member is not required by the role: their switch works and says nothing of it.
    expect(sw('名 carol')!.getAttribute('aria-disabled')).toBeNull()
    expect(sw('名 carol')!.closest('li')!.querySelector('[data-slot="passkey-by-role"]')).toBeNull()
  })

  it("lists a person's passkeys, and deletes a lost one after asking", async () => {
    const carol = await open()
    await click(byLabel(tsec.users.editFor('名 carol')))
    await settle(() => document.querySelector('[data-slot="user-details"] [data-passkey]') !== null, 'the passkey')
    expect(document.querySelector('[data-slot="user-details"]')!.textContent).toContain('Lost phone')
    await click(byLabel(tsec.users.deletePasskeyFor('Lost phone')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    expect(document.querySelector('[role="alertdialog"]')!.textContent).toContain(
      tsec.users.deletePasskeyHelp('Lost phone', '名 carol'),
    )
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="alertdialog"] button')].find(
        (b) => b.textContent === tsec.users.deletePasskey,
      ),
    )
    await settle(() => document.querySelector('[data-slot="user-details"] [data-passkey]') === null, 'deleted')
    expect(calls('DELETE', `/api/users/${carol.id}/passkeys/`)).toHaveLength(1)
    // It is in the audit log.
    const audit = adminRequests(m!.server)('GET', '/api/admin/audit?kind=passkey_removed').body as {
      items: { detail: { by: string } }[]
    }
    expect(audit.items[0].detail.by).toBe('admin')
  })

  it('edits the email, and says whether it is verified', async () => {
    const carol = await open()
    await click(byLabel(tsec.users.editFor('名 carol')))
    await settle(() => document.querySelector('[data-slot="user-details"] input[type="email"]') !== null, 'the details')
    await type(
      document.querySelector<HTMLInputElement>('[data-slot="user-details"] input[type="email"]')!,
      'carol@example.com',
    )
    await click(
      [...document.querySelectorAll<HTMLElement>('[data-slot="user-details"] button')].find(
        (b) => b.textContent === tsec.users.saveEmail,
      ),
    )
    await settle(() => calls('PATCH', `/api/users/${carol.id}`).length === 1, 'patched')
    expect(JSON.parse(calls('PATCH', `/api/users/${carol.id}`)[0].body!)).toEqual({ email: 'carol@example.com' })
    // Setting an address without saying so leaves it unverified; the switch confirms it.
    await settle(() => byLabel(tsec.users.emailVerified)?.hasAttribute('disabled') === false, 'the switch is usable')
    await click(byLabel(tsec.users.emailVerified))
    await settle(() => calls('PATCH', `/api/users/${carol.id}`).length === 2, 'verified')
    expect(JSON.parse(calls('PATCH', `/api/users/${carol.id}`)[1].body!)).toEqual({ emailVerified: true })
    await settle(() => text().includes(tsec.users.verified), 'shown as verified')
  })
})
