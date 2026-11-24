// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import type { SettingsNotice } from '@/api/types'
import { SpaceGate } from '@/components/layout/SpaceGate'
import { t } from '@/i18n'
import { tp } from '@/i18n/props'
import { absoluteTime } from '@/lib/time'
import {
  byLabel,
  click,
  installDomShims,
  locationState,
  locationText,
  mount,
  settle,
  shareSpace,
  type Mounted,
} from '@/testing/harness'
import SettingsRoute from './SettingsRoute'

// What the server says to those who manage a space about settings files it could not use: in the settings (Types),
// and as a line above the pages while a file cannot be read or lost entries.

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const UNREADABLE: SettingsNotice = {
  file: '.petalbook/space.yml',
  unreadable: true,
  since: { commit: '9f2c0aa1b2c3d4', time: '2026-11-22T16:20:11Z' },
  restoredFrom: { commit: '41ab55e6f7a8b9', time: '2026-11-20T09:02:40Z' },
  lost: ['Tang', 'Song'],
}
const REPAIRED: SettingsNotice = {
  file: '.petalbook/tags.yml',
  unreadable: false,
  since: { time: '2026-11-21T08:00:00Z' },
  restoredFrom: { commit: '77d0aa', time: '2026-11-19T12:00:00Z' },
  copy: '.petalbook/tags.yml.unreadable-20261121T081502Z',
  repairedAt: '2026-11-21T08:15:02Z',
}
const ENTRY: SettingsNotice = { file: '.petalbook/space.yml', entry: 'Tang: views', unreadable: true }

const routes = (
  <Routes>
    <Route path="/settings/:section?" element={<SettingsRoute />} />
    <Route path="/s/:space" element={<SpaceGate />}>
      <Route path="f/*" element={<p data-testid="folder">folder</p>} />
    </Route>
  </Routes>
)
const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')
const banner = () => document.querySelector<HTMLElement>('[data-slot="settings-notice"]')
const noticeItems = () => [...document.querySelectorAll<HTMLElement>('[data-slot="settings-notices"] li')]

async function openSingle(path: string, notices: SettingsNotice[]) {
  m = await mount(routes, {
    server: { mode: 'single' },
    entries: (server) => (server.seedSettingsNotices(notices), [path]),
  })
}
async function openShared(
  role: 'owner' | 'editor' | 'viewer',
  path: (space: string) => string,
  notices: SettingsNotice[],
) {
  m = await mount(routes, {
    signIn: true,
    server: { mode: 'accounts' },
    entries: (server) => {
      const { space } = shareSpace(server, role)
      server.seedSettingsNotices(notices, space)
      localStorage.setItem('pb.space', space)
      return [path(space)]
    },
  })
}

describe('settings notices in the settings', () => {
  it('are told plainly to someone who manages the space: since when, restored from what, where the copy is, what was lost', async () => {
    await openSingle('/settings/types', [UNREADABLE, REPAIRED, ENTRY])
    await settle(() => noticeItems().length === 3, 'the notices')
    expect(dialog()!.textContent).toContain(tp.notices.title)
    const [a, b, c] = noticeItems().map((e) => e.textContent ?? '')
    expect(a).toContain('.petalbook/space.yml')
    expect(a).toContain(tp.notices.unreadable(tp.notices.commit(absoluteTime(UNREADABLE.since!.time), '9f2c0aa')))
    expect(a).toContain(tp.notices.restored(tp.notices.commit(absoluteTime(UNREADABLE.restoredFrom!.time), '41ab55e')))
    expect(a).toContain(tp.notices.lost('Tang, Song'))
    expect(b).toContain(tp.notices.fixed(absoluteTime(REPAIRED.repairedAt!)))
    expect(b).toContain(tp.notices.copy(REPAIRED.copy!))
    expect(b).toContain(tp.notices.restored(tp.notices.commit(absoluteTime(REPAIRED.restoredFrom!.time), '77d0aa')))
    expect(c).toContain(tp.notices.entry('.petalbook/space.yml', 'Tang: views'))
    expect(c).toContain(tp.notices.unreadableNow)
  })

  it('say when nothing of a file could be read, and when no earlier version could be used', async () => {
    await openSingle('/settings/types', [
      { file: '.petalbook/space.yml', unreadable: true, since: { time: '2026-11-22T16:20:11Z' }, lost: ['*'] },
    ])
    await settle(() => noticeItems().length === 1, 'the notice')
    const text = noticeItems()[0].textContent ?? ''
    expect(text).toContain(tp.notices.unreadable(tp.notices.version(absoluteTime('2026-11-22T16:20:11Z'))))
    expect(text).toContain(tp.notices.restoredNone)
    expect(text).toContain(tp.notices.lostAll)
  })

  it('are not there when there is nothing to tell', async () => {
    await openSingle('/settings/types', [])
    await settle(() => dialog() !== null && dialog()!.textContent!.includes(tp.types.title), 'the section')
    expect(noticeItems()).toEqual([])
    expect(dialog()!.textContent).not.toContain(tp.notices.title)
  })

  it('are never shown to someone who does not manage the space (the server leaves them out)', async () => {
    for (const role of ['editor', 'viewer'] as const) {
      await openShared(role, () => '/settings/types', [UNREADABLE])
      await settle(() => dialog() !== null && dialog()!.textContent!.includes(tp.types.title), 'the section')
      expect(noticeItems()).toEqual([])
      const types = m!.requests.find((r) => r.method === 'GET' && /\/types$/.test(r.url))
      expect(types).toBeDefined()
      await m!.unmount()
      m = undefined
    }
  })
})

describe('the line above the pages of a space', () => {
  it('shows for a manager while a file cannot be read, links to the details, and once dismissed stays away', async () => {
    await openSingle('/s/default/f/Work', [UNREADABLE])
    await settle(() => banner() !== null, 'the line')
    expect(banner()!.textContent).toContain(t.settingsNotice.unreadable)
    expect(banner()!.querySelector('a')!.getAttribute('href')).toBe('/settings/types')
    await click(byLabel(t.settingsNotice.dismiss))
    expect(banner()).toBeNull()
    await m!.unmount()
    m = undefined
    await openSingle('/s/default/f/Work', [UNREADABLE])
    await settle(() => document.querySelector('[data-testid="folder"]') !== null, 'the page')
    await new Promise((r) => setTimeout(r, 50))
    expect(banner()).toBeNull()
  })

  it('comes back when a notice says something new, and says when entries were lost', async () => {
    await openSingle('/s/default/f/Work', [UNREADABLE])
    await settle(() => banner() !== null, 'the line')
    await click(byLabel(t.settingsNotice.dismiss))
    await m!.unmount()
    m = undefined
    await openSingle('/s/default/f/Work', [{ ...REPAIRED, lost: ['Ming'] }])
    await settle(() => banner() !== null, 'the line again')
    expect(banner()!.textContent).toContain(t.settingsNotice.lost)
  })

  it('opens the details in the settings', async () => {
    await openSingle('/s/default/f/Work', [UNREADABLE])
    await settle(() => banner() !== null, 'the line')
    await click(banner()!.querySelector('a'))
    await settle(() => locationText().startsWith('/settings/types') && noticeItems().length === 1, 'the details')
  })

  it('closing the settings opened from the line goes back to the page it was opened on', async () => {
    await openSingle('/s/default/f/Work', [UNREADABLE])
    await settle(() => banner() !== null, 'the line')
    await click(banner()!.querySelector('a'))
    await settle(() => locationText().startsWith('/settings/types'), 'the details')
    expect(locationState()).toMatchObject({ from: '/s/default/f/Work' })
  })

  it('does not show for a file that was put right with nothing lost', async () => {
    await openSingle('/s/default/f/Work', [REPAIRED])
    await settle(() => document.querySelector('[data-testid="folder"]') !== null, 'the page')
    await new Promise((r) => setTimeout(r, 50))
    expect(banner()).toBeNull()
  })

  it('never shows to someone who does not manage the space, who does not even ask for the types', async () => {
    for (const role of ['editor', 'viewer'] as const) {
      await openShared(role, (space) => `/s/${space}/f/Work`, [UNREADABLE])
      await settle(() => document.querySelector('[data-testid="folder"]') !== null, 'the page')
      await new Promise((r) => setTimeout(r, 50))
      expect(banner()).toBeNull()
      expect(m!.requests.some((r) => /\/types$/.test(r.url))).toBe(false)
      await m!.unmount()
      m = undefined
    }
  })

  it('shows to an owner of a shared space', async () => {
    await openShared('owner', (space) => `/s/${space}/f/Work`, [UNREADABLE])
    await settle(() => banner() !== null, 'the line')
  })
})
