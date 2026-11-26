// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { type MockServer } from '@/api/mock/server'
import { MOCK_ADMIN } from '@/api/mock/server'
import { adminRequests, byLabel, click, installDomShims, mount, settle, type Mounted } from '@/testing/harness'
import { oa } from '@/i18n/oauth'
import { tk } from '@/i18n/tokens'
import { t } from '@/i18n'
import SettingsRoute from './SettingsRoute'

// What needs a feature that an administrator switched off is not shown (no disabled stand-ins), against the mock server.

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
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const text = () => dialog()?.textContent ?? ''
const nav = () => document.querySelector<HTMLElement>('nav')?.textContent ?? ''

const off =
  (...keys: string[]) =>
  (server: MockServer) =>
    keys.forEach((k) =>
      adminRequests(server)('PUT', `/api/admin/settings/${k}`, { value: false, lower: { mode: 'lock' } }),
    )

const open = async (seed: (server: MockServer) => void, wait: string) => {
  m = await mount(routes, {
    entries: (server) => {
      seed(server)
      return ['/settings/tokens']
    },
    signIn: true,
    server: { mode: 'accounts', oauth: true },
  })
  await settle(() => dialog() !== null && text().includes(wait), 'the page')
}

describe('a feature that is off is not shown', () => {
  it('shows tokens and connected apps while both are on', async () => {
    await open(() => undefined, tk.create)
    await settle(() => text().includes(oa.yours.title), 'the connected apps')
  })

  // A token and a connected app the person made while the features were on.
  const seed = (server: MockServer) => {
    const request = adminRequests(server)
    const space = (request('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id
    request('POST', '/api/account/tokens', { name: 'mine', spaces: [space] })
    const req = server.startAuthorization(MOCK_ADMIN.username, { clientName: 'Drop' })
    request('POST', '/api/oauth/consent', { req, approve: true, spaces: [space], access: 'write' })
  }
  const confirm = () =>
    click(
      [...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find(
        (b) => b.textContent?.trim() === tk.revokeConfirm || b.textContent?.trim() === oa.yours.revokeConfirm,
      ),
    )

  it('keeps the tokens and their Revoke while features.tokens is off, with no way to make one', async () => {
    await open((server) => {
      seed(server)
      off('features.tokens')(server)
    }, oa.yours.title)
    await settle(() => text().includes('mine'), 'the token')
    expect(text()).not.toContain(tk.create)
    expect(text()).toContain(tk.off)
    expect(text()).toContain(oa.yours.title)
    await click(byLabel(tk.revokeLabel('mine')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await confirm()
    await settle(
      () => m!.requests.some((r) => r.method === 'DELETE' && r.url.startsWith('/api/account/tokens/')),
      'the revocation',
    )
  })

  it('keeps the connected apps and their Revoke while features.mcp is off, and the tokens too', async () => {
    await open((server) => {
      seed(server)
      off('features.mcp')(server)
    }, tk.create)
    await settle(() => text().includes('Drop'), 'the connected app')
    expect(text()).toContain(oa.yours.off)
    await click(byLabel(oa.yours.revokeLabel('Drop')))
    await settle(() => document.querySelector('[role="alertdialog"]') !== null, 'the question')
    await confirm()
    await settle(
      () => m!.requests.some((r) => r.method === 'DELETE' && r.url.startsWith('/api/account/grants/')),
      'the disconnection',
    )
  })

  it('says nothing of connected apps while features.mcp is off and there are none', async () => {
    await open(off('features.mcp'), tk.create)
    expect(text()).not.toContain(oa.yours.title)
  })

  it('keeps the section while both are off: what there is can be revoked', async () => {
    m = await mount(routes, {
      entries: (server) => {
        seed(server)
        off('features.tokens', 'features.mcp')(server)
        return ['/settings/tokens']
      },
      signIn: true,
      server: { mode: 'accounts', oauth: true },
    })
    await settle(() => dialog() !== null && text().includes('mine'), 'the page')
    expect(nav()).toContain(t.settingsUi.sections.tokens)
    expect(text()).not.toContain(tk.create)
  })
})
