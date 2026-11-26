// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { FixtureServer } from '@/api/fixtures/server'
import { AppSidebar } from '@/components/layout/AppSidebar'
import { FileOpsProvider } from '@/components/files/FileOps'
import { QuickCaptureProvider } from '@/components/layout/QuickCapture'
import { SidebarProvider } from '@/components/ui/sidebar'
import { t } from '@/i18n'
import { ct } from '@/i18n/checks'
import { click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// The page of the problems of a space is no longer in the sidebar: it is reached from the space's settings, and a red
// dot says there is something to see.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: FxMounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <SidebarProvider>
    <QuickCaptureProvider>
      <FileOpsProvider>
        <AppSidebar />
      </FileOpsProvider>
    </QuickCaptureProvider>
    <Routes>
      <Route path="/settings/:section?" element={<SettingsRoute />} />
      <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
    </Routes>
  </SidebarProvider>
)

// One page without an id is the problem: the check answers it.
const withProblem = (fx: FixtureServer) =>
  fx.reply('GET', '/api/spaces/default/checks', {
    counts: {
      bad_header: 0,
      broken_link: 0,
      duplicate_id: 0,
      duplicate_title: 0,
      missing_asset: 0,
      no_id: 1,
      not_in_type: 0,
      type_mismatch: 0,
    },
    items: [{ kind: 'no_id', path: 'Notes/noid.md', message: 'no id', rev: 'r1', fix: null }],
    next: '',
  })
const dots = () => document.querySelectorAll('[data-slot="problem-dot"]')
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')

describe('the problems of a space', () => {
  it('is not in the sidebar any more', async () => {
    m = await mount(routes, { entries: (s) => (withProblem(s), ['/s/default/recent']), fixtures: { mode: 'single' } })
    await settle(() => document.querySelector('[data-slot="sidebar"]') !== null, 'sidebar')
    const links = [...document.querySelectorAll('[data-slot="sidebar"] a')].map((a) => a.getAttribute('href'))
    expect(links.some((h) => h?.includes('/check'))).toBe(false)
    expect(document.body.textContent).not.toContain('校验')
  })

  it('is a section of the space settings, with how many there are and a link to the list', async () => {
    m = await mount(routes, { entries: (s) => (withProblem(s), ['/settings/general']), fixtures: { mode: 'single' } })
    await settle(() => document.querySelector('[data-testid="problem-count"]') !== null, 'the count')
    expect(dialog()!.textContent).toContain(t.general.checkTitle)
    expect(t.general.checkTitle).toBe('问题')
    expect(document.querySelector('[data-testid="problem-count"]')!.textContent).toBe(t.general.checkCount(1))
    const link = [...dialog()!.querySelectorAll('a')].find((a) => a.textContent === t.general.checkLink)!
    expect(link.getAttribute('href')).toBe('/s/default/check')
    // The dot is on the settings entry of the dialog's navigation, too.
    expect(dots().length).toBeGreaterThan(0)
  })

  it('has no dot, and says so, when nothing is wrong', async () => {
    m = await mount(routes, { entries: ['/settings/general'], fixtures: { mode: 'single' } })
    await settle(() => document.querySelector('[data-testid="problem-count"]') !== null, 'the count')
    expect(document.querySelector('[data-testid="problem-count"]')!.textContent).toBe(t.general.checkNone)
    expect(dots()).toHaveLength(0)
  })

  it('is named Problems on the page itself', () => {
    expect(ct.title).toBe('问题')
  })
})

describe('the close button of the settings dialog', () => {
  it('is in the corner of the dialog, not in the header of the right pane, and closes it', async () => {
    m = await mount(routes, { entries: ['/', '/settings/profile'], signIn: true, fixtures: { mode: 'accounts' } })
    await settle(() => dialog() !== null, 'dialog')
    const close = dialog()!.querySelector<HTMLElement>('[data-slot="settings-close"]')!
    expect(close.parentElement).toBe(dialog())
    expect(dialog()!.querySelector('header [data-slot="settings-close"]')).toBeNull()
    expect(close.textContent).toBe(t.settingsUi.close)
    // On a phone the corner is bigger, and a way back is drawn next to the title.
    expect(close.className).toContain('size-11')
    const back = [...dialog()!.querySelectorAll('button')].find((b) => b.textContent === t.settingsUi.back)
    expect(back).toBeDefined()
    await click(close)
    await settle(() => dialog() === null, 'closed')
  })

  it('keeps clear of the notch and the home bar of a phone: the dialog and its corner button are padded by the safe area', async () => {
    m = await mount(routes, { entries: ['/', '/settings/profile'], signIn: true, fixtures: { mode: 'accounts' } })
    await settle(() => dialog() !== null, 'dialog')
    for (const side of ['top', 'right', 'bottom', 'left'])
      expect(dialog()!.className, side).toContain(`env(safe-area-inset-${side})`)
    const close = dialog()!.querySelector<HTMLElement>('[data-slot="settings-close"]')!
    expect(close.className).toContain('env(safe-area-inset-top)')
    expect(close.className).toContain('env(safe-area-inset-right)')
    // On a desktop it is a window with no inset.
    expect(dialog()!.className).toContain('md:p-0')
  })
})
