// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Route, Routes } from 'react-router'
import { PreferencesPanel } from '@/components/settings/PreferencesPanel'
import { BrowserNotifier } from '@/components/notifications/BrowserNotifier'
import { browserNotifyState, disableBrowserNotify, enableBrowserNotify } from '@/lib/browserNotify'
import { tn } from '@/i18n/notify'
import { click, installDomShims, mount, settle, shareSpace, type Mounted } from '@/testing/harness'

beforeAll(installDomShims)
beforeEach(() => localStorage.clear())

let m: Mounted | undefined
afterEach(async () => {
  await m?.unmount()
  m = undefined
  vi.unstubAllGlobals()
})

// A stand-in for the Notifications API: what the browser would say, and the notices it was asked to show.
function fakeNotification(permission: NotificationPermission, asked: NotificationPermission = 'granted') {
  const shown: { title: string; body?: string }[] = []
  const requests: number[] = []
  class FakeNotification {
    static permission = permission
    static requestPermission = vi.fn(async () => {
      requests.push(1)
      FakeNotification.permission = asked
      return asked
    })
    onclick: (() => void) | null = null
    title: string
    constructor(title: string, opts?: NotificationOptions) {
      this.title = title
      shown.push({ title, body: opts?.body })
    }
    close() {}
  }
  vi.stubGlobal('Notification', FakeNotification)
  Object.defineProperty(window, 'Notification', { value: FakeNotification, configurable: true, writable: true })
  return { shown, requests, Fake: FakeNotification }
}

describe('the state of the browser notifications', () => {
  it('is off until the person opts in, and the browser is asked only then', async () => {
    const f = fakeNotification('default')
    expect(browserNotifyState()).toBe('off')
    expect(f.requests).toHaveLength(0)
    expect(await enableBrowserNotify()).toBe('on')
    expect(f.requests).toHaveLength(1)
    expect(browserNotifyState()).toBe('on')
    expect(disableBrowserNotify()).toBe('off')
    // Allowed already: switching on again does not ask again.
    expect(await enableBrowserNotify()).toBe('on')
    expect(f.requests).toHaveLength(1)
  })

  it('is denied when the browser refuses, and stays off', async () => {
    fakeNotification('default', 'denied')
    expect(await enableBrowserNotify()).toBe('denied')
    expect(browserNotifyState()).toBe('denied')
  })

  it('knows a browser without them', () => {
    vi.stubGlobal('Notification', undefined)
    delete (window as { Notification?: unknown }).Notification
    expect(browserNotifyState()).toBe('unsupported')
  })
})

describe('the opt-in in the preferences', () => {
  const routes = (
    <Routes>
      <Route path="*" element={<PreferencesPanel />} />
    </Routes>
  )
  // The second switch of the preferences (after the upload of originals).
  const sw = () => document.querySelectorAll<HTMLElement>('[role="switch"]')[1]!
  const stateText = () => document.querySelector('[data-slot="browser-notify-state"]')?.textContent
  const buttonNamed = (name: string) => [...document.querySelectorAll('button')].find((b) => b.textContent === name)

  it('says why before the browser asks, and only then asks', async () => {
    const f = fakeNotification('default')
    m = await mount(routes, { server: { mode: 'single' } })
    expect(stateText()).toBe(tn.browser.off)
    await click(sw())
    await settle(() => document.body.textContent!.includes(tn.browser.pre.title), 'the explanation')
    expect(f.requests).toHaveLength(0)
    await click(buttonNamed(tn.browser.pre.continue))
    await settle(() => stateText() === tn.browser.on, 'on')
    expect(f.requests).toHaveLength(1)
    expect(sw().getAttribute('aria-checked')).toBe('true')
    await click(sw())
    await settle(() => stateText() === tn.browser.off, 'off again')
    expect(localStorage.getItem('pb.notify.browser')).toBeNull()
  })

  it('"not now" asks nothing and changes nothing', async () => {
    const f = fakeNotification('default')
    m = await mount(routes, { server: { mode: 'single' } })
    await click(sw())
    await settle(() => buttonNamed(tn.browser.pre.cancel) !== undefined, 'the explanation')
    await click(buttonNamed(tn.browser.pre.cancel))
    await settle(() => !document.body.textContent!.includes(tn.browser.pre.title), 'closed')
    expect(f.requests).toHaveLength(0)
    expect(stateText()).toBe(tn.browser.off)
  })

  it('explains a refusal and a browser that has none, and cannot be switched on', async () => {
    fakeNotification('denied')
    m = await mount(routes, { server: { mode: 'single' } })
    expect(document.body.textContent).toContain(tn.browser.denied)
    expect(sw().getAttribute('aria-disabled')).toBe('true')
    await m.unmount()
    m = undefined
    vi.stubGlobal('Notification', undefined)
    delete (window as { Notification?: unknown }).Notification
    m = await mount(routes, { server: { mode: 'single' } })
    expect(document.body.textContent).toContain(tn.browser.unsupported)
  })
})

describe('the notice itself', () => {
  it('is shown when the unread count goes up while nobody is looking, and not before the opt-in', async () => {
    const f = fakeNotification('granted')
    localStorage.setItem('pb.notify.browser', '1')
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    m = await mount(
      <>
        <BrowserNotifier />
        <p data-testid="x" />
      </>,
      { signIn: true, server: { mode: 'accounts' } },
    )
    await settle(() => m!.qc.getQueryData(['notifications', 'unread']) !== undefined, 'the first count')
    expect(f.shown).toHaveLength(0)
    shareSpace(m.server, 'editor', '团队')
    await m.qc.invalidateQueries({ queryKey: ['notifications', 'unread'] })
    await settle(() => f.shown.length === 1, 'the notice')
    expect(f.shown[0].title).toContain('把你加入了「团队」')
    expect(f.shown[0].body).toContain('编辑者')
  })

  it('is not shown without the opt-in, nor while the page is in front', async () => {
    const f = fakeNotification('granted')
    m = await mount(<BrowserNotifier />, { signIn: true, server: { mode: 'accounts' } })
    await settle(() => m!.qc.getQueryData(['notifications', 'unread']) !== undefined, 'the first count')
    shareSpace(m.server, 'editor', '团队')
    await m.qc.invalidateQueries({ queryKey: ['notifications', 'unread'] })
    await settle(() => m!.qc.getQueryData(['notifications', 'unread']) === 1, 'the count')
    expect(f.shown).toHaveLength(0)
    localStorage.setItem('pb.notify.browser', '1')
    shareSpace(m.server, 'viewer', '另一个', 'third')
    await m.qc.invalidateQueries({ queryKey: ['notifications', 'unread'] })
    await settle(() => m!.qc.getQueryData(['notifications', 'unread']) === 2, 'two')
    // jsdom's document is visible and has focus: nothing is shown over a page that is being looked at.
    expect(f.shown).toHaveLength(0)
  })
})
