// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { type MockServer } from '@/api/mock/server'
import { adminRequests, installDomShims, mount, settle, type Mounted } from '@/testing/harness'
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

  it('has no way to make a token while features.tokens is off, and keeps the connected apps', async () => {
    await open(off('features.tokens'), oa.yours.title)
    expect(text()).not.toContain(tk.create)
    expect(text()).toContain(tk.off)
    expect(nav()).toContain(t.settingsUi.sections.tokens)
  })

  it('has no connected apps while features.mcp is off, and keeps the tokens', async () => {
    await open(off('features.mcp'), tk.create)
    expect(text()).not.toContain(oa.yours.title)
  })

  it('does not offer the section at all while both are off', async () => {
    m = await mount(routes, {
      entries: (server) => {
        off('features.tokens', 'features.mcp')(server)
        return ['/settings/tokens']
      },
      signIn: true,
      server: { mode: 'accounts', oauth: true },
    })
    // The address goes to the first section there is, and the navigation has no tokens in it.
    await settle(() => dialog() !== null && !nav().includes(t.settingsUi.sections.tokens) && nav() !== '', 'the page')
    expect(text()).not.toContain(tk.create)
  })
})
