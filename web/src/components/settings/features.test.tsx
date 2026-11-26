// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { addToken, approveApp, withAuthorizationServer } from '@/api/fixtures/data/area'
import type { FixtureData } from '@/api/fixtures/data/model'
import { byLabel, click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { oa } from '@/i18n/oauth'
import { tk } from '@/i18n/tokens'
import { t } from '@/i18n'
import SettingsRoute from './SettingsRoute'

// What needs a feature that an administrator switched off is not shown (no disabled stand-ins), against the fixture server.

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
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')!
const text = () => dialog()?.textContent ?? ''
const nav = () => document.querySelector<HTMLElement>('nav')?.textContent ?? ''

// What an administrator switched off: the features say so.
const off =
  (...keys: string[]) =>
  (d: FixtureData) =>
    keys.forEach((k) => (d.features[k] = false))

// The server has an authorization server, and the scene is made first.

const open = async (seed: (d: FixtureData) => void, wait: string) => {
  m = await mount(routes, {
    entries: ['/settings/tokens'],
    signIn: true,
    fixtures: { mode: 'accounts', data: (d) => (withAuthorizationServer(d), seed(d)) },
  })
  await settle(() => dialog() !== null && text().includes(wait), 'the page')
}

describe('a feature that is off is not shown', () => {
  it('shows tokens and connected apps while both are on', async () => {
    await open(() => undefined, tk.create)
    await settle(() => text().includes(oa.yours.title), 'the connected apps')
  })

  // A token and a connected app the person made while the features were on.
  const seed = (d: FixtureData) => {
    addToken(d, 'mine')
    approveApp(d, 'Drop')
  }
  const confirm = () =>
    click(
      [...document.querySelectorAll<HTMLButtonElement>('[role="alertdialog"] button')].find(
        (b) => b.textContent?.trim() === tk.revokeConfirm || b.textContent?.trim() === oa.yours.revokeConfirm,
      ),
    )

  it('keeps the tokens and their Revoke while features.tokens is off, with no way to make one', async () => {
    await open((d) => {
      seed(d)
      off('features.tokens')(d)
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
    await open((d) => {
      seed(d)
      off('features.mcp')(d)
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
      entries: ['/settings/tokens'],
      signIn: true,
      fixtures: {
        mode: 'accounts',
        data: (d) => {
          withAuthorizationServer(d)
          seed(d)
          off('features.tokens', 'features.mcp')(d)
        },
      },
    })
    await settle(() => dialog() !== null && text().includes('mine'), 'the page')
    expect(nav()).toContain(t.settingsUi.sections.tokens)
    expect(text()).not.toContain(tk.create)
  })
})
