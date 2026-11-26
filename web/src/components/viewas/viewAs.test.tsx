// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { api, ApiError, request } from '@/api/client'
import { manageApi } from '@/api/manage'
import type { SessionInfo, SpaceInfo } from '@/api/types'
import { t } from '@/i18n'
import { forgetViewAs, startViewAs, viewAsId } from '@/lib/viewAs'
import { click, installDomShims, makePerson, mount, settle, type Mounted } from '@/testing/harness'
import { ViewAsBanner, exitViewAs } from './Banner'

// Viewing the app as a person (docs/api.md, "Viewing as a person"): the tab opened at /as/<id> keeps the id in its own
// sessionStorage and sends it on every request; the banner says so and leads out; the mock answers as the server does.

beforeAll(installDomShims)
beforeEach(() => {
  sessionStorage.clear()
  forgetViewAs()
})

let m: Mounted | undefined
afterEach(async () => {
  forgetViewAs()
  await m?.unmount()
  m = undefined
})

// The signed-in administrator, a person with a space of their own making, and a view of them opened by this browser.
async function scene() {
  let amy = { id: '', space: '' }
  m = await mount(<div />, {
    signIn: true,
    server: { mode: 'accounts', empty: true },
    entries: (server) => {
      const p = makePerson(server, 'amy')
      const space = (p.request('POST', '/api/spaces', { name: '团队' }).body as { id: string }).id
      amy = { id: p.id, space }
      return ['/']
    },
  })
  const view = await manageApi.startViewAs(amy.id, '检查一个问题')
  return { amy, view }
}

describe('a tab that views the app as a person', () => {
  it('keeps the id from /as/<id> in its own storage and shows /', () => {
    history.replaceState(null, '', '/as/va_abc')
    expect(startViewAs()).toBe('va_abc')
    expect(location.pathname).toBe('/')
    expect(sessionStorage.getItem('petalbook.viewAs')).toBe('va_abc')
    expect(document.documentElement.dataset.viewAs).toBe('')
    // A reload of the tab keeps it; another tab (its own empty storage) has none.
    expect(startViewAs()).toBe('va_abc')
    sessionStorage.clear()
    expect(startViewAs()).toBeNull()
    expect(document.documentElement.dataset.viewAs).toBeUndefined()
  })

  it('sends the header on every request of the tab, and none once it is left', async () => {
    const { amy, view } = await scene()
    // Before: this tab is the administrator's own.
    await api.spaces()
    expect(m!.requests.at(-1)!.viewAs).toBeNull()

    history.replaceState(null, '', `/as/${view.id}`)
    startViewAs()
    const session = await request<SessionInfo>('/session')
    expect(m!.requests.at(-1)!.viewAs).toBe(view.id)
    expect(session.user?.username).toBe('amy')
    expect(session.user?.email).toBe('')
    expect(session.user?.permissions).toEqual([])
    expect(session.viewAs?.id).toBe(view.id)
    expect(session.viewAs?.hidden).toBeGreaterThanOrEqual(1)
    // The person's organisation's space, read only; never their personal one.
    const { spaces } = await api.spaces()
    expect(spaces.map((s: SpaceInfo) => s.id)).toEqual([amy.space])
    expect(spaces[0].permissions).toEqual(['read'])
    // Every write and everything of the person's own is refused.
    for (const call of [
      () => request('/spaces', { method: 'POST', body: { name: 'x' } }),
      () => request('/notifications'),
      () => request('/account'),
    ])
      await expect(call()).rejects.toMatchObject({ code: 'view_as_read_only' })

    // Leaving: the server is told without the header, and the tab leaves.
    let left = false
    const before = m!.requests.length
    await exitViewAs(amy.id, () => (left = true))
    expect(left).toBe(true)
    expect(viewAsId()).toBeNull()
    const end = m!.requests.slice(before).find((r) => r.method === 'DELETE')!
    expect(end.url).toBe(`/api/admin/users/${amy.id}/view-as`)
    expect(end.viewAs).toBeNull()
    // The view is over: its id is refused.
    history.replaceState(null, '', `/as/${view.id}`)
    startViewAs()
    await expect(api.spaces()).rejects.toBeInstanceOf(ApiError)
    await expect(api.spaces()).rejects.toMatchObject({ code: 'view_as_ended' })
  })

  it('shows a banner with the person, read only, the time left and a way out', async () => {
    const { amy, view } = await scene()
    history.replaceState(null, '', `/as/${view.id}`)
    startViewAs()
    await m!.unmount()
    let left = false
    m = await mount(
      <ViewAsBanner
        view={{ ...view, viewer: { name: 'admin' }, hidden: 1 }}
        user={amy.id}
        name="名 amy"
        leave={() => (left = true)}
      />,
    )
    const banner = document.querySelector('[data-slot="view-as-banner"]')!
    expect(banner.getAttribute('aria-label')).toBe(t.viewAs.region)
    expect(banner.textContent).toContain(t.viewAs.viewing('名 amy'))
    expect(banner.textContent).toContain(t.viewAs.readOnly)
    expect(banner.textContent).toContain(t.viewAs.left(60))
    const exit = banner.querySelector<HTMLButtonElement>(`button[aria-label="${t.viewAs.exitLabel}"]`)!
    expect(exit).not.toBeNull()
    await click(exit)
    await settle(() => left, 'left')
    expect(viewAsId()).toBeNull()
  })
})
