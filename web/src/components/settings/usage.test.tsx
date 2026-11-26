// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { ApiError } from '@/api/client'
import { NameDialog } from '@/components/layout/NameDialog'
import type { FixtureServer } from '@/api/fixtures/server'
import type { AccountUsage } from '@/api/types'
import { click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { formatBytes } from '@/lib/bytes'
import { t } from '@/i18n'
import { tu } from '@/i18n/usage'
import SettingsRoute from './SettingsRoute'

// The "Usage" section of the account settings, against the fixture server.

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
const meter = (k: string) => document.querySelector<HTMLElement>(`[data-slot="usage-meter"][data-quota="${k}"]`)!
const open = async (
  scene?: (fx: FixtureServer) => void,
  fixtures: { mode: 'accounts' | 'single'; empty?: boolean } = { mode: 'accounts' },
) => {
  m = await mount(routes, {
    entries: (fx) => {
      scene?.(fx)
      return ['/settings/usage']
    },
    signIn: fixtures.mode === 'accounts',
    fixtures,
  })
  await settle(
    () =>
      dialog() !== null && (text().includes(tu.account.title) || text().includes(t.settingsUi.sections.preferences)),
    'the dialog',
  )
}

// What the server says the person has used, measured some time ago (the numbers are stored, not counted for the request).
const usage = (over: Partial<AccountUsage>): AccountUsage => ({
  spaces: { used: 1, limit: 20 },
  bytes: { used: 3000, limit: null },
  pages: { used: 1, limit: null },
  measuredAt: '2026-11-25T10:00:00Z',
  notices: [],
  ...over,
})

describe('Usage in the account settings', () => {
  it('is an entry of the account group, and shows spaces, disk and pages with their limits; what is not limited says so', async () => {
    await open()
    await settle(() => document.querySelector('[data-slot="usage-meters"]') !== null, 'the meters')
    expect([...dialog().querySelectorAll('nav a')].map((a) => a.textContent)).toContain(t.settingsUi.sections.usage)
    // The personal space is one of 20 (the default); disk and pages have no limit, so no bar.
    expect(meter('spaces').textContent).toContain(tu.account.used('1', '20'))
    expect(meter('spaces').querySelector('[role="progressbar"]')).not.toBeNull()
    expect(meter('bytes').textContent).toContain(tu.noLimit)
    expect(meter('bytes').querySelector('[role="progressbar"]')).toBeNull()
    expect(meter('pages').textContent).toContain(tu.noLimit)
    // Nothing has been measured: it says so, and says why the numbers lag.
    expect(text()).toContain(tu.neverMeasured)
    expect(text()).toContain(tu.measuredHelp)
    expect(document.querySelector('[data-slot="usage-notices"]')).toBeNull()
    // The numbers come from one request: nothing walks files for it.
    expect(m!.requests.filter((r) => r.url === '/api/account/usage')).toHaveLength(1)
  })

  it('shows measured numbers in human units against the limit, with the bar full only up to the limit', async () => {
    await open(
      (fx) =>
        fx.reply(
          'GET',
          '/api/account/usage',
          usage({ bytes: { used: 3000, limit: 10 * 1024 * 1024 }, pages: { used: 1, limit: null } }),
        ),
      { mode: 'accounts', empty: true },
    )
    await settle(() => document.querySelector('[data-slot="usage-meters"]') !== null, 'the meters')
    const bytes = meter('bytes')
    expect(bytes.textContent).toMatch(/\d+(\.\d)? KB \/ 10 MB/)
    expect(bytes.getAttribute('data-over')).toBeNull()
    const bar = bytes.querySelector<HTMLElement>('[role="progressbar"]')!
    expect(bar.getAttribute('aria-valuemax')).toBe(String(10 * 1024 * 1024))
    expect(bar.getAttribute('aria-label')).toContain(tu.quotas.bytes)
    expect(text()).not.toContain(tu.neverMeasured)
    expect(meter('pages').textContent).toContain(tu.account.used('1', tu.noLimit))
  })

  it("tells a person who is over a limit, and the tenant's total, without stopping them", async () => {
    await open(
      (fx) =>
        fx.reply(
          'GET',
          '/api/account/usage',
          usage({
            pages: { used: 3, limit: 2 },
            bytes: { used: 12, limit: 1 },
            notices: [
              { quota: 'pages', scope: 'person', limit: 2, used: 3 },
              { quota: 'bytes', scope: 'tenant', limit: 1, used: 12 },
            ],
          }),
        ),
      { mode: 'accounts', empty: true },
    )
    await settle(() => document.querySelector('[data-slot="usage-notices"]') !== null, 'the notices')
    const notices = [...document.querySelectorAll('[data-notice]')].map((n) => n.getAttribute('data-notice'))
    expect(notices.sort()).toEqual(['person-pages', 'tenant-bytes'])
    expect(document.querySelector('[data-notice="person-pages"]')!.textContent).toBe(
      tu.account.notices.person(tu.quotas.pages, '3', '2'),
    )
    expect(meter('pages').getAttribute('data-over')).toBe('true')
    expect(text()).toContain(tu.account.overHelp)
    expect(text()).toContain(formatBytes(1))
  })

  it('nothing on it can be wider than a phone', async () => {
    await open()
    await settle(() => document.querySelector('[data-slot="usage-meters"]') !== null, 'the meters')
    expect(dialog().querySelector('section[aria-labelledby="usage-heading"]')!.classList.contains('grid-cols-1')).toBe(
      true,
    )
    for (const li of document.querySelectorAll('[data-slot="usage-meter"]')) {
      expect(li.classList.contains('min-w-0')).toBe(true)
      expect(li.firstElementChild!.classList.contains('flex-wrap')).toBe(true)
    }
  })

  it('does not exist in single-user mode', async () => {
    await open(undefined, { mode: 'single' })
    expect(text()).not.toContain(tu.account.title)
    expect(m!.requests.filter((r) => r.url === '/api/account/usage')).toEqual([])
  })

  it("a refusal to make a space says what the limit is, in the page's language", async () => {
    m = await mount(
      <NameDialog
        open
        onOpenChange={() => {}}
        title="x"
        initial="Notes"
        submitLabel="make"
        onSubmit={() =>
          Promise.reject(
            new ApiError(
              403,
              'quota_exceeded',
              'you already own 3 spaces',
              undefined,
              undefined,
              undefined,
              undefined,
              { quota: 'spaces', limit: 3, used: 3 },
            ),
          )
        }
      />,
      { fixtures: { mode: 'accounts' } },
    )
    await settle(() => document.querySelector('[role="dialog"] form') !== null, 'the dialog')
    await click([...document.querySelectorAll('button')].find((b) => b.textContent === 'make'))
    await settle(
      () => (document.querySelector('[role="dialog"]')?.textContent ?? '').includes(tu.account.createRefused(3)),
      'the refusal',
    )
  })
})
