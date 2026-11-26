// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureData } from '@/api/fixtures/data/model'
import { tm } from '@/i18n/mail'
import { t } from '@/i18n'
import { byLabel, byText, click, installDomShims, mount, settle, type, type FxMounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// More than one address per account (accounts.multipleEmails), against the fixture server: off, the one address as before;
// on, the list with what each address is, adding one, making a verified one primary and removing one.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
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

const created = '2026-01-05T08:00:00Z'
// Mail on and the primary address set; `multiple` turns the switch on, and `second` adds a confirmed second address.
async function open(scene: (d: FixtureData) => void = () => undefined, wait = () => list() !== null) {
  m = await mount(routes, {
    entries: ['/settings/profile'],
    signIn: true,
    fixtures: {
      mode: 'accounts',
      data: (d) => {
        d.auth.forgot = true
        // The address was typed in the profile, so it is not verified yet.
        Object.assign(d.auth.users[0], { email: 'admin@example.test', emailVerified: false })
        scene(d)
      },
    },
  })
  await settle(wait, 'the page')
}
const multiple = (d: FixtureData) => void (d.features['accounts.multipleEmails'] = true)
const second = (d: FixtureData) => {
  multiple(d)
  d.area.account.emails.push({ email: 'work@example.test', primary: false, verified: true, created })
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
    expect(m!.fx.sent.mail.filter((x) => x.kind === 'verify').map((x) => x.to)).toEqual(['two@example.test'])
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

  it('lists a claim of an address another account holds as waiting for confirmation, and withdraws it', async () => {
    // Another account holds the address, not verified: the list says this claim is waiting.
    const claim = (d: FixtureData) => {
      multiple(d)
      d.area.account.emails.push({
        email: 'held@example.test',
        primary: false,
        verified: false,
        pending: true,
        created,
      })
    }
    await open(claim, () => row('held@example.test') !== null)
    expect(row('held@example.test')!.textContent).toContain(tm.emails.pending)
    expect(byLabel(t.fmt.labelled(tm.emails.verify, 'held@example.test'))).toBeNull()
    expect(byLabel(t.fmt.labelled(tm.emails.makePrimary, 'held@example.test'))).toBeNull()
    await click(byLabel(t.fmt.labelled(tm.emails.remove, 'held@example.test')))
    await settle(() => byText('[role="alertdialog"] button', tm.emails.remove) !== null, 'the question')
    await click(byText('[role="alertdialog"] button', tm.emails.remove))
    await settle(() => row('held@example.test') === null, 'the withdrawal')
    expect(m!.requests.some((r) => r.method === 'DELETE' && r.url === '/api/account/emails/held%40example.test')).toBe(
      true,
    )
  })
})
