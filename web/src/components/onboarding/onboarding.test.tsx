// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { Route, Routes } from 'react-router'
import { AppShell } from '@/components/layout/AppShell'
import { SpaceGate } from '@/components/layout/SpaceGate'
import { onboardingKey, readOnboarding, tourSteps, writeOnboarding } from '@/lib/onboarding'
import { MOCK_ADMIN } from '@/api/mock/server'
import { to } from '@/i18n/onboard'
import { adminRequests, click, installDomShims, locationText, mount, settle, type Mounted } from '@/testing/harness'
import type { MockServer } from '@/api/mock/server'
import { createSyncedPrefs } from '@/lib/syncedPrefs'
import { browserPrefs, setPrefsStore } from '@/lib/prefsStore'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
})

const routes = (
  <Routes>
    <Route element={<AppShell />}>
      <Route path="/s/:space" element={<SpaceGate />}>
        <Route index element={<p data-testid="home">home</p>} />
      </Route>
      <Route path="/settings/:section" element={<p data-testid="settings">settings</p>} />
      <Route path="/admin/*" element={<p data-testid="admin">admin</p>} />
    </Route>
  </Routes>
)

const tour = () => document.querySelector<HTMLElement>('[data-slot="tour"]')
const guide = () => document.querySelector<HTMLElement>('[data-slot="repo-guide"]')
const progress = () => document.querySelector('[data-slot="tour-progress"]')?.textContent
const buttonNamed = (name: string, root: ParentNode = document) =>
  [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === name)
const title = () => tour()!.querySelector('[data-slot="dialog-title"], h2')?.textContent ?? ''

const firstSpace = (server: MockServer) =>
  (adminRequests(server)('GET', '/api/spaces').body as { spaces: { id: string }[] }).spaces[0].id

async function openApp(scene?: (server: MockServer) => void, accounts = true) {
  m = await mount(routes, {
    signIn: accounts,
    server: { mode: accounts ? 'accounts' : 'single' },
    entries: (server) => {
      scene?.(server)
      return [`/s/${firstSpace(server)}`]
    },
  })
}
const userId = () => (m!.qc.getQueryData(['session']) as { user: { id: string } } | undefined)?.user.id ?? ''

describe('the steps of the tour', () => {
  it('are the basics for everyone, the repository step only when one is asked for, and the administrators’ only for them', () => {
    const base = ['welcome', 'page', 'editor', 'favorites', 'search']
    expect(tourSteps({ admin: false, repo: false })).toEqual([...base, 'done'])
    expect(tourSteps({ admin: false, repo: true })).toEqual([...base, 'repo', 'done'])
    expect(tourSteps({ admin: true, repo: false })).toEqual([...base, 'admin', 'done'])
    expect(tourSteps({ admin: true, repo: true })).toEqual([...base, 'repo', 'admin', 'done'])
  })
})

describe('the tour on a first sign-in', () => {
  it('opens by itself, goes through the steps with back and next, and is remembered as done at the end', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    expect(title()).toBe(to.tour.steps.welcome.title)
    expect(progress()).toBe(to.tour.step(1, 7))
    await click(buttonNamed(to.tour.next))
    await settle(() => title() === to.tour.steps.page.title, 'step two')
    expect(progress()).toBe(to.tour.step(2, 7))
    await click(buttonNamed(to.tour.back))
    await settle(() => title() === to.tour.steps.welcome.title, 'back to the first')
    expect(buttonNamed(to.tour.back)).toBeUndefined()
    for (const step of ['page', 'editor', 'favorites', 'search', 'admin', 'done'] as const) {
      await click(buttonNamed(to.tour.next))
      await settle(() => title() === to.tour.steps[step].title, step)
    }
    // The administrators' step says the daily text is off by default.
    expect(to.tour.steps.admin.body).toContain('默认')
    expect(buttonNamed(to.tour.next)).toBeUndefined()
    expect(readOnboarding(userId())).toBeNull()
    await click(buttonNamed(to.tour.finish))
    await settle(() => tour() === null, 'closed')
    expect(readOnboarding(userId())?.state).toBe('done')
    // It is the person's own state, under their own key.
    expect(localStorage.getItem(onboardingKey(userId()))).toContain('"state":"done"')
  })

  it('can be skipped at the first step, and is then not shown again on the next visit', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    await click(buttonNamed(to.tour.skip))
    await settle(() => tour() === null, 'closed')
    expect(readOnboarding(userId())?.state).toBe('skipped')
    // The next visit, on a server that knows this person by the id they have: nothing starts.
    await m!.unmount()
    m = undefined
    await openApp((server) => writeOnboarding(server.userId(MOCK_ADMIN.username)!, 'skipped'))
    await settle(() => document.querySelector('[data-testid="home"]') !== null, 'the page')
    await new Promise((r) => setTimeout(r, 100))
    expect(tour()).toBeNull()
  })

  it('stays usable on a short or landscape phone screen, and keeps still for those who ask for less motion', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    const cls = tour()!.className
    // It scrolls when the screen is shorter than the step, and keeps clear of the notch and the home bar.
    expect(cls).toMatch(/\boverflow-y-auto\b/)
    expect(cls).toMatch(/max-h-\[100svh\]|max-h-svh/)
    expect(cls).toMatch(/safe-area-inset-top/)
    expect(cls).toMatch(/safe-area-inset-bottom/)
    // The step's slide and the progress dots do not move for prefers-reduced-motion.
    const step = tour()!.querySelector('[data-slot="tour-step"]')!
    expect(step.className).toMatch(/motion-reduce:animate-none/)
    const dot = tour()!.querySelector('[data-slot="tour-dots"] span')!
    expect(dot.className).toMatch(/motion-reduce:transition-none/)
  })

  it('waits for a slow file rather than start again a tour the person finished', async () => {
    let answer!: (v: { personal: boolean; prefs: Record<string, unknown> }) => void
    const synced = createSyncedPrefs({
      api: { get: () => new Promise((r) => (answer = r)), patch: async () => ({ personal: true }) },
    })
    setPrefsStore(synced)
    try {
      let uid = ''
      await openApp((server) => void (uid = server.userId(MOCK_ADMIN.username)!))
      const hydrating = synced.hydrate(uid)
      await settle(() => document.querySelector('[data-testid="home"]') !== null, 'the page')
      // The spaces are known and this browser has nothing: the file is still coming, so nothing starts.
      await new Promise((r) => setTimeout(r, 200))
      expect(tour()).toBeNull()
      answer({ personal: true, prefs: { [onboardingKey(uid)]: { state: 'done', at: '2026-11-01T00:00:00.000Z' } } })
      await hydrating
      await new Promise((r) => setTimeout(r, 200))
      expect(tour()).toBeNull()
    } finally {
      setPrefsStore(browserPrefs)
    }
  })

  it('is reopened from the help menu, and closing it again does not overwrite what was remembered', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    await click(buttonNamed(to.tour.skip))
    await settle(() => tour() === null, 'closed')
    const before = localStorage.getItem(onboardingKey(userId()))
    await click(document.querySelector(`[aria-label="${to.help.label}"]`))
    await settle(() => document.body.textContent!.includes(to.help.tour), 'the menu')
    await click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === to.help.tour))
    await settle(() => tour() !== null, 'the tour again')
    expect(title()).toBe(to.tour.steps.welcome.title)
    await click(buttonNamed(to.tour.skip))
    await settle(() => tour() === null, 'closed again')
    expect(localStorage.getItem(onboardingKey(userId()))).toBe(before)
  })

  it('an action of a step does the thing and leaves the tour', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    await click(buttonNamed(to.tour.next))
    await settle(() => title() === to.tour.steps.page.title, 'the page step')
    await click(buttonNamed(to.tour.steps.page.action))
    await settle(() => tour() === null, 'the tour left')
    expect(readOnboarding(userId())?.state).toBe('skipped')
  })

  it('is full-screen on a phone and has controls of at least 40 px', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    expect(tour()!.className).toContain('max-sm:h-svh')
    for (const b of [to.tour.skip, to.tour.next]) expect(buttonNamed(b)!.className).toContain('h-10')
  })

  it('is not started over the administration, where a person goes on purpose', async () => {
    m = await mount(routes, { signIn: true, server: { mode: 'accounts' }, entries: ['/admin/users'] })
    await settle(() => document.querySelector('[data-testid="admin"]') !== null, 'the page')
    await new Promise((r) => setTimeout(r, 100))
    expect(tour()).toBeNull()
  })
})

describe('connecting one’s own repository', () => {
  const ask = (graceDays: number) => (server: MockServer) => {
    for (const [key, value] of Object.entries({
      'storage.remotes.require': 'any',
      'storage.remotes.scope': 'all',
      'storage.remotes.graceDays': graceDays,
    }))
      adminRequests(server)('PUT', `/api/admin/settings/${key}`, { value })
  }
  const steps = () => [...document.querySelectorAll<HTMLElement>('[data-slot="repo-steps"] li')]

  it('is a step of the tour when the site suggests it, with why, what to click, what to authorise and where to get help', async () => {
    await openApp(ask(30))
    await settle(() => tour() !== null, 'the tour')
    for (let i = 0; i < 5; i++) await click(buttonNamed(to.tour.next))
    await settle(() => title() === to.tour.steps.repo.title, 'the repository step')
    expect(tour()!.textContent).toContain(to.tour.steps.repo.bodyAsked)
    const body = tour()!.textContent!
    expect(body).toContain(to.repo.whyTitle)
    expect(body).toContain(to.repo.anyProvider)
    expect(steps().map((s) => s.dataset.step)).toEqual(['connect', 'repo', 'sync'])
    expect(steps().map((s) => s.dataset.state)).toEqual(['now', 'todo', 'todo'])
    expect(body).toContain(to.repo.connect.action)
    expect(body).toContain(to.repo.repo.authorise)
    expect(body).toContain(to.repo.helpTitle)
    expect(body).toContain(to.repo.left(2))
    expect(document.querySelector('[data-slot="repo-steps"]')!.getAttribute('data-forced')).toBe('false')
  })

  it('is told as required, and shows the step that remains in the notice instead of only blocking', async () => {
    await openApp(ask(0))
    await settle(() => tour() !== null, 'the tour')
    await click(buttonNamed(to.tour.skip))
    await settle(() => document.querySelector('[data-slot="storage-notice"]') !== null, 'the notice')
    const note = document.querySelector<HTMLElement>('[data-slot="storage-notice"]')!
    expect(note.dataset.overdue).toBe('true')
    const left = note.querySelector('[data-slot="storage-notice-step"]')!
    expect(left.textContent).toContain(to.repo.bannerLeft(to.repo.connect.title('…')).slice(0, 5))
    await click(buttonNamed(to.repo.bannerAction, note))
    await settle(() => guide() !== null, 'the guide')
    expect(guide()!.querySelector('[data-slot="repo-steps"]')!.getAttribute('data-forced')).toBe('true')
    expect(guide()!.textContent).toContain(to.repo.whyForced)
    // The button of the step to take now goes to where it is done.
    await click(buttonNamed(to.repo.connect.action, guide()!))
    await settle(() => locationText().startsWith('/settings/connections'), 'the connections')
    expect(guide()).toBeNull()
  })

  it('ticks the step of the account once it is connected, and leaves the one after as the step to take', async () => {
    m = await mount(routes, {
      signIn: true,
      server: { mode: 'accounts', github: { configured: true } },
      entries: (server) => {
        ask(0)(server)
        return [`/s/${firstSpace(server)}`]
      },
    })
    await settle(() => tour() !== null, 'the tour')
    await click(buttonNamed(to.tour.skip))
    await settle(() => document.querySelector('[data-slot="storage-notice-step"]') !== null, 'the notice')
    expect(document.querySelector('[data-slot="storage-notice-step"]')!.textContent).toContain('GitHub')
    adminRequests(m.server)('POST', '/api/_mock/github', { connect: 'tang-gh' })
    await m.qc.invalidateQueries({ queryKey: ['connections'] })
    await settle(
      () => document.querySelector('[data-slot="storage-notice-step"]')!.textContent!.includes(to.repo.repo.title),
      'the next step',
    )
    await click(buttonNamed(to.repo.bannerAction))
    await settle(() => guide() !== null, 'the guide')
    const items = [...guide()!.querySelectorAll<HTMLElement>('li')]
    expect(items.map((s) => s.dataset.state)).toEqual(['done', 'now', 'todo'])
    expect(guide()!.textContent).toContain(to.repo.left(1))
    expect(buttonNamed(to.repo.repo.action, guide()!)).toBeTruthy()
    expect(buttonNamed(to.repo.connect.action, guide()!)).toBeUndefined()
  })

  it('says so when nothing is asked', async () => {
    await openApp()
    await settle(() => tour() !== null, 'the tour')
    await click(buttonNamed(to.tour.skip))
    await click(document.querySelector(`[aria-label="${to.help.label}"]`))
    await settle(() => document.body.textContent!.includes(to.help.repo), 'the menu')
    await click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === to.help.repo))
    await settle(() => guide() !== null, 'the guide')
    expect(guide()!.textContent).toContain(to.repo.nothing)
  })
})

describe('single-user mode', () => {
  it('has the tour and the help menu, and nothing about repositories', async () => {
    await openApp(undefined, false)
    await settle(() => tour() !== null, 'the tour')
    expect(progress()).toBe(to.tour.step(1, 7))
    await click(buttonNamed(to.tour.skip))
    await click(document.querySelector(`[aria-label="${to.help.label}"]`))
    await settle(() => document.body.textContent!.includes(to.help.tour), 'the menu')
    expect(document.body.textContent).not.toContain(to.help.repo)
  })
})
