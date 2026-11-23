// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { MOCK_ADMIN } from '@/api/mock/server'
import { tm } from '@/i18n/mail'
import { click, installDomShims, mount, settle, type Mounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The account's address: whether it counts, and the button that asks for the link (only while the server can send mail).

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
const panel = () => document.querySelector<HTMLElement>('[data-slot="email-verification"]')
const button = (label: string) => [...(panel()?.querySelectorAll('button') ?? [])].find((b) => b.textContent?.trim() === label) ?? null

async function open(mailOn: boolean) {
  m = await mount(routes, {
    entries: (server) => {
      const login = server.handle('POST', '/api/login', JSON.stringify(MOCK_ADMIN), 'application/json')
      const cookie = login.headers!['Set-Cookie'].split(';')[0]
      server.handle('PATCH', '/api/account', JSON.stringify({ email: 'admin@example.test' }), 'application/json', cookie)
      if (mailOn) server.handle('PUT', '/api/admin/mail', JSON.stringify({ enabled: true, host: 'smtp.example.test', from: 'notes@example.test' }), 'application/json', cookie)
      return ['/settings/profile']
    },
    signIn: true,
    server: { mode: 'accounts' },
  })
  await settle(() => panel() !== null, 'the email line')
}

describe('the address on the profile', () => {
  it('says the address is not verified, and offers no button while the server cannot send mail', async () => {
    await open(false)
    expect(panel()!.textContent).toContain(tm.account.unverified)
    await new Promise((r) => setTimeout(r, 50))
    expect(button(tm.account.verify)).toBeNull()
  })

  it('asks for the link, which goes to the address and nowhere else', async () => {
    await open(true)
    await settle(() => button(tm.account.verify) !== null, 'the button')
    await click(button(tm.account.verify))
    await settle(() => panel()!.textContent!.includes(tm.account.sent('admin@example.test')), 'the notice')
    expect(m!.server.mail.outbox.map((x) => [x.kind, x.to])).toEqual([['verify', 'admin@example.test']])
    expect(JSON.parse(m!.requests.find((r) => r.url === '/api/account/email/verify')!.body!)).toHaveProperty('lang')
  })
})
