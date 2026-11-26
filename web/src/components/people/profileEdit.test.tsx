// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { createMockServer } from '@/api/mock/server'
import type { User } from '@/api/types'
import { EditPersonDialog } from '@/components/admin/users/PersonEditDialogs'
import SettingsRoute from '@/components/settings/SettingsRoute'
import { tp } from '@/i18n/people'
import {
  adminRequests,
  asPerson,
  click,
  installDomShims,
  makePerson,
  mount,
  settle,
  type,
  type Mounted,
} from '@/testing/harness'

// Editing a profile in dialogs, an administrator's edits of a person, and the temporary password's hold, against
// the mock server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const dialogs = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"]')]
const lastDialog = () => dialogs().at(-1)!
const button = (label: string, root: ParentNode = document) =>
  [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === label) ?? null
const sent = () => m!.requests.filter((r) => r.method !== 'GET' && r.url !== '/api/login')

describe('my profile', () => {
  const openProfile = async () => {
    m = await mount(
      <Routes>
        <Route path="/settings/:section?" element={<SettingsRoute />} />
      </Routes>,
      {
        signIn: true,
        server: { mode: 'accounts' },
        entries: (server) => {
          makePerson(server, 'olive')
          return ['/settings/profile']
        },
      },
    )
    await settle(() => document.querySelector('[data-slot="profile-values"]') !== null, 'the profile')
  }

  it('edits the name and username in a dialog, and says when the username is taken', async () => {
    await openProfile()
    await click(button(tp.profile.editProfile))
    await settle(() => document.querySelector('#edit-profile-form') !== null, 'the dialog')
    const form = () => document.querySelector<HTMLFormElement>('#edit-profile-form')!
    await type(form().querySelector<HTMLInputElement>('input[autocomplete="username"]')!, 'olive')
    await type(form().querySelector<HTMLInputElement>('input[autocomplete="name"]')!, 'New Name')
    await click(button(tp.profile.save, lastDialog()))
    await settle(() => form()?.querySelector('p[role="alert"]') != null, 'the refusal')
    await type(form().querySelector<HTMLInputElement>('input[autocomplete="username"]')!, 'renamed')
    await click(button(tp.profile.save, lastDialog()))
    await settle(() => document.querySelector('#edit-profile-form') === null, 'saved')
    const patch = sent()
      .filter((r) => r.method === 'PATCH' && r.url === '/api/account')
      .at(-1)!
    expect(JSON.parse(patch.body!)).toEqual({ name: 'New Name', username: 'renamed' })
    await settle(
      () => document.querySelector('[data-slot="profile-values"]')!.textContent!.includes('renamed'),
      'shown',
    )
  })

  it('changes the email in its own dialog: the new address is asked to verify', async () => {
    await openProfile()
    await click(button(tp.profile.change, document.querySelector('[data-slot="profile-values"]')!))
    await settle(() => document.querySelector('#change-email-form') !== null, 'the dialog')
    await type(document.querySelector<HTMLInputElement>('#change-email-form input[type="email"]')!, 'me@example.test')
    await click(button(tp.profile.changeEmail, lastDialog()))
    await settle(() => lastDialog().textContent!.includes(tp.profile.emailNotSent), 'the result')
    expect(
      sent()
        .filter((r) => r.method === 'PATCH')
        .map((r) => JSON.parse(r.body!)),
    ).toEqual([{ email: 'me@example.test' }])
  })
})

describe('an administrator edits a person', () => {
  it('shows what the organization sign-in manages, with the warning', async () => {
    const u: User = {
      id: 'x',
      username: 'carol',
      name: 'Carol',
      email: 'c@example.test',
      emailVerified: true,
      role: 'member',
      disabled: false,
      passkeyRequired: false,
      created: '2026-01-01T00:00:00Z',
      sso: 1,
    }
    m = await mount(<EditPersonDialog u={u} open onOpenChange={() => undefined} />, {
      signIn: true,
      server: { mode: 'accounts' },
    })
    await settle(() => document.querySelector('#edit-person-form') !== null, 'the dialog')
    expect(lastDialog().textContent).toContain(tp.admin.managed)
    expect(lastDialog().textContent).toContain(tp.admin.managedWarn)
  })
})

describe('a temporary password', () => {
  it('ends their sessions, holds the next one until they choose their own, and lifts the hold', () => {
    const server = createMockServer({ now: () => Date.now(), mode: 'accounts' })
    const carol = makePerson(server, 'carol')
    adminRequests(server)('POST', `/api/users/${carol.id}/password`, { password: 'temporary-pass-1' })
    // Their old session is gone.
    expect((carol.request('GET', '/api/session').body as { user: unknown }).user).toBeNull()
    // The next sign-in with the temporary password is held.
    const login = server.handle(
      'POST',
      '/api/login',
      JSON.stringify({ username: 'carol', password: 'temporary-pass-1' }),
      'application/json',
      null,
    )
    expect(login.status).toBe(200)
    const as = asPerson(server, login.headers!['Set-Cookie'].split(';')[0])
    expect((as('GET', '/api/session').body as { hold: string }).hold).toBe('password')
    // Choosing a password of their own lifts it.
    as('POST', '/api/account/password', { current: 'temporary-pass-1', new: 'my-own-password-2' })
    expect((as('GET', '/api/session').body as { hold: unknown }).hold).toBeNull()
  })
})
