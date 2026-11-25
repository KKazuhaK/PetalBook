// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
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

// The users list and a person's page: passkeys, the email and what a person has.

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
      <Route path="/admin/*" element={<AdminGate />} />
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const calls = (method: string, url: string) => m!.requests.filter((r) => r.method === method && r.url.startsWith(url))

// A person with a passkey, made before the page opens (at `where`: the list, or a page of the person).
async function open(
  where: (id: string, admin: ReturnType<typeof adminRequests>) => string = () => '/admin/users',
  scene?: (admin: ReturnType<typeof adminRequests>) => void,
) {
  let carol!: ReturnType<typeof makePerson>
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      carol = makePerson(server, 'carol')
      const ceremony = (
        carol.request('POST', '/api/account/passkeys/begin', { name: 'Lost phone' }).body as { ceremony: string }
      ).ceremony
      carol.request('POST', `/api/account/passkeys/finish?ceremony=${ceremony}`, { id: 'cred-carol' })
      const admin = adminRequests(server)
      scene?.(admin)
      return [where(carol.id, admin)]
    },
  })
  return carol
}
const person = (tab: string) => (id: string) => `/admin/user/${id}${tab ? `/${tab}` : ''}`

describe('users and their sign-in', () => {
  it('shows how many passkeys and single sign-on identities each person has', async () => {
    await open()
    await settle(
      () =>
        [...document.querySelectorAll('[data-slot="user-counts"]')].some(
          (e) => e.textContent === tc.users.signInCounts(1, 0),
        ),
      'the counts',
    )
  })

  it('requiring a passkey is one switch, sent on its own', async () => {
    const carol = await open(person('security'))
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
    const sw = (name: string) => byLabel(tsec.users.passkeyRequiredFor(name))
    await open(
      (_id, admin) =>
        `/admin/user/${(admin('GET', '/api/users?q=admin').body as { users: { id: string }[] }).users[0].id}/security`,
      (admin) => {
        // The administrator has a passkey first, so the requirement does not hold their own session.
        const ceremony = (admin('POST', '/api/account/passkeys/begin', { name: 'Laptop' }).body as { ceremony: string })
          .ceremony
        admin('POST', `/api/account/passkeys/finish?ceremony=${ceremony}`, { id: 'cred-admin' })
        admin('PUT', '/api/admin/security', { passkeys: { requireRoles: ['admin'] } })
      },
    )
    await settle(() => sw(MOCK_ADMIN.name)?.getAttribute('aria-disabled') === 'true', 'the switch, held by the role')
    expect(sw(MOCK_ADMIN.name)!.getAttribute('aria-checked')).toBe('true')
    expect(sw(MOCK_ADMIN.name)!.closest('label')!.querySelector('[data-slot="passkey-by-role"]')?.textContent).toBe(
      tsec.users.passkeyByRole,
    )
  })

  it('a member is not required by the role: their switch works and says nothing of it', async () => {
    await open(person('security'), (admin) =>
      admin('PUT', '/api/admin/security', { passkeys: { requireRoles: ['admin'] } }),
    )
    await settle(() => byLabel(tsec.users.passkeyRequiredFor('名 carol')) != null, 'the switch')
    const sw = byLabel(tsec.users.passkeyRequiredFor('名 carol'))!
    expect(sw.getAttribute('aria-disabled')).toBeNull()
    expect(sw.closest('label')!.querySelector('[data-slot="passkey-by-role"]')).toBeNull()
  })

  it("lists a person's passkeys, and deletes a lost one after asking", async () => {
    const carol = await open(person('security'))
    await settle(() => document.querySelector('[data-slot="person-security"] [data-passkey]') !== null, 'the passkey')
    expect(document.querySelector('[data-slot="person-security"]')!.textContent).toContain('Lost phone')
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
    await settle(() => document.querySelector('[data-slot="person-security"] [data-passkey]') === null, 'deleted')
    expect(calls('DELETE', `/api/users/${carol.id}/passkeys/`)).toHaveLength(1)
    // It is in the audit log.
    const audit = adminRequests(m!.server)('GET', '/api/admin/audit?kind=passkey_removed').body as {
      items: { detail: { by: string } }[]
    }
    expect(audit.items[0].detail.by).toBe('admin')
  })

  it('edits the email, and says whether it is verified', async () => {
    const carol = await open(person(''))
    await settle(
      () => document.querySelector('[data-slot="person-profile"] input[type="email"]') !== null,
      'the details',
    )
    await type(
      document.querySelector<HTMLInputElement>('[data-slot="person-profile"] input[type="email"]')!,
      'carol@example.com',
    )
    await click(
      [...document.querySelectorAll<HTMLElement>('[data-slot="person-profile"] button')].find(
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
    await settle(() => text().includes('carol@example.com'), 'shown in the header')
  })
})
