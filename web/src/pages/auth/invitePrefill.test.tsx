// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { click, installDomShims, mount, settle, type, type Mounted } from '@/testing/harness'
import { tu } from '@/i18n/usage'
import InvitePage from './InvitePage'

// The page that accepts an invite starts with what the invite fills in (a new tenant's first administrator).

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  window.location.hash = ''
})

const routes = (
  <Routes>
    <Route path="/invite" element={<InvitePage />} />
    <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
  </Routes>
)
const text = () => document.body.textContent ?? ''
const field = (autocomplete: string) =>
  document.querySelector<HTMLInputElement>(`input[autocomplete="${autocomplete}"]`)!

async function open(fill?: { username: string; name: string; email: string }) {
  window.location.hash = '#inv-fill'
  m = await mount(routes, {
    entries: ['/invite'],
    server: { mode: 'accounts', invite: { token: 'inv-fill', role: 'admin', fill } },
  })
  await settle(() => document.querySelector('form input[autocomplete="username"]') !== null, 'the form')
}

describe('an invite that fills in the form', () => {
  it("starts the username and name with what it says, shows the address that will be the account's, and accepts with the password alone", async () => {
    await open({ username: 'wu', name: 'Wu Wei', email: 'wu@acme.example' })
    await settle(() => field('username').value === 'wu', 'the username')
    expect(field('name').value).toBe('Wu Wei')
    expect(document.querySelector('[data-slot="invite-prefill"]')!.textContent).toContain(tu.invite.prefilled)
    expect(text()).toContain(tu.invite.email('wu@acme.example'))
    const pw = [...document.querySelectorAll<HTMLInputElement>('input[type="password"]')]
    await type(pw[0], 'a-long-password')
    await type(pw[1], 'a-long-password')
    await click(document.querySelector('form button[type="submit"]'))
    await settle(() => document.querySelector('[data-testid="elsewhere"]') !== null, 'signed in')
    const accept = m!.requests.find((r) => r.url === '/api/invites/accept')!
    expect(JSON.parse(accept.body!)).toMatchObject({ username: 'wu', name: 'Wu Wei' })
  })

  it('lets the person change what was filled in, and says nothing when the invite fills in nothing', async () => {
    await open({ username: 'wu', name: 'Wu Wei', email: '' })
    await settle(() => field('username').value === 'wu', 'the username')
    await type(field('username'), 'wu2')
    expect(field('username').value).toBe('wu2')
    expect(text()).not.toContain(tu.invite.email(''))
    await m!.unmount()
    await open()
    expect(field('username').value).toBe('')
    expect(document.querySelector('[data-slot="invite-prefill"]')).toBeNull()
  })
})
