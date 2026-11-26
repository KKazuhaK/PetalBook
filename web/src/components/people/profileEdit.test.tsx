// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { addPerson } from '@/api/fixtures/data/scenes'
import type { User } from '@/api/types'
import { EditPersonDialog } from '@/components/admin/users/PersonEditDialogs'
import SettingsRoute from '@/components/settings/SettingsRoute'
import { tp } from '@/i18n/people'
import { click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'

// Editing a profile in dialogs, an administrator's edits of a person, and the temporary password's hold, against
// the fixture server.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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
        fixtures: { mode: 'accounts' },
        entries: (fx) => {
          addPerson(fx.data, 'olive')
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
    // Somebody else has that username: the server refuses once.
    m!.fx.fail('PATCH', '/api/account', 409, { code: 'exists', message: 'that username is taken' }, { times: 1 })
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
      fixtures: { mode: 'accounts' },
    })
    await settle(() => document.querySelector('#edit-person-form') !== null, 'the dialog')
    expect(lastDialog().textContent).toContain(tp.admin.managed)
    expect(lastDialog().textContent).toContain(tp.admin.managedWarn)
  })
})
