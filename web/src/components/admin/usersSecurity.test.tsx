// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { SidebarProvider } from '@/components/ui/sidebar'
import { tc } from '@/i18n/console'
import { tp } from '@/i18n/people'
import { tsec } from '@/i18n/security'
import { byLabel, click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import type { FixtureData } from '@/api/fixtures/data/model'
import { MOCK_ADMIN } from '@/api/fixtures/data/people'
import { addPerson, setSite } from '@/testing/adminScene'
import { AdminGate } from './AdminGate'

// The users list and a person's page: passkeys, the email and what a person has.

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
    </Routes>
  </SidebarProvider>
)
const text = () => document.body.textContent ?? ''
const calls = (method: string, url: string) => m!.requests.filter((r) => r.method === method && r.url.startsWith(url))

// A person with a passkey, made before the page opens (at `where`: the list, or a page of the person).
async function open(
  where: (id: string, d: FixtureData) => string = () => '/admin/users',
  scene?: (d: FixtureData) => void,
  carolExtra: Parameters<typeof addPerson>[2] = {},
) {
  let carol!: ReturnType<typeof addPerson>
  let at = ''
  m = await mount(routes, {
    signIn: true,
    fixtures: {
      mode: 'accounts',
      data: (d) => {
        carol = addPerson(d, 'carol', carolExtra)
        d.admin.people[carol.id] = {
          sessions: [],
          identities: [],
          passkeys: [{ id: 'cred-carol', name: 'Lost phone', created: '2026-11-20T08:00:00Z', synced: false }],
        }
        scene?.(d)
        at = where(carol.id, d)
      },
    },
    entries: () => [at],
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
      (_id, d) => `/admin/user/${d.auth.users[0].id}/security`,
      (d) => {
        setSite(d, 'signin.passkeyRoles', ['admin'])
        // The server counts the administrator as required, by their role.
        d.auth.users[0].passkeyRequired = true
      },
    )
    await settle(() => sw(MOCK_ADMIN.name)?.getAttribute('aria-disabled') === 'true', 'the switch, held by the role')
    expect(sw(MOCK_ADMIN.name)!.getAttribute('aria-checked')).toBe('true')
    expect(sw(MOCK_ADMIN.name)!.closest('label')!.querySelector('[data-slot="passkey-by-role"]')?.textContent).toBe(
      tsec.users.passkeyByRole,
    )
  })

  it('a member is not required by the role: their switch works and says nothing of it', async () => {
    await open(person('security'), (d) => setSite(d, 'signin.passkeyRoles', ['admin']))
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
  })

  it('edits the email, and says whether it is verified', async () => {
    // A person with no address yet, whose address is not verified.
    const carol = await open(person(''), undefined, { email: '', emailVerified: false })
    await settle(() => document.querySelector('[data-slot="person-values"]') !== null, 'the details')
    // The values are read-only; the address has its own button, which opens a dialog.
    expect(document.querySelector('[data-slot="person-profile"] input[type="email"]')).toBeNull()
    await click(
      [...document.querySelectorAll<HTMLElement>('[data-slot="person-values"] button')].find(
        (b) => b.textContent === tp.profile.change,
      ),
    )
    await settle(() => document.querySelector('#change-person-email-form') !== null, 'the dialog')
    await type(
      document.querySelector<HTMLInputElement>('#change-person-email-form input[type="email"]')!,
      'carol@example.com',
    )
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="dialog"] button')].find(
        (b) => b.textContent === tp.admin.saveEmail,
      ),
    )
    await settle(() => calls('PATCH', `/api/users/${carol.id}`).length === 1, 'patched')
    // Verify is the default way: the person is sent a link; the old address is told by the server.
    expect(JSON.parse(calls('PATCH', `/api/users/${carol.id}`)[0].body!)).toEqual({
      email: 'carol@example.com',
      emailMode: 'verify',
    })
    await click(
      [...document.querySelectorAll<HTMLElement>('[role="dialog"] button')].find((b) => b.textContent === tp.done),
    )
    // Setting an address without saying so leaves it unverified; the switch confirms it.
    await settle(() => byLabel(tsec.users.emailVerified)?.hasAttribute('disabled') === false, 'the switch is usable')
    await click(byLabel(tsec.users.emailVerified))
    await settle(() => calls('PATCH', `/api/users/${carol.id}`).length === 2, 'verified')
    expect(JSON.parse(calls('PATCH', `/api/users/${carol.id}`)[1].body!)).toEqual({ emailVerified: true })
    await settle(() => text().includes('carol@example.com'), 'shown in the header')
  })
})
