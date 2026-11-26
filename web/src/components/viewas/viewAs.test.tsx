// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { api, ApiError, request } from '@/api/client'
import { manageApi } from '@/api/manage'
import { t } from '@/i18n'
import { forgetViewAs, startViewAs, viewAsId } from '@/lib/viewAs'
import { click, installDomShims, mount, settle, type FxMounted } from '@/testing/harness'
import { addPerson, addSpace } from '@/testing/adminScene'
import { ViewAsBanner, exitViewAs } from './Banner'

// Viewing the app as a person (docs/api.md, "Viewing as a person"): the tab opened at /as/<id> keeps the id in its own
// sessionStorage and sends it on every request; the banner says so and leads out. What the server answers a view (the
// person's session, their read-only spaces, the refusals) is its own Go tests': here the refusals are declared.

beforeAll(installDomShims)
beforeEach(() => {
  sessionStorage.clear()
  forgetViewAs()
})

let m: FxMounted | undefined
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
    fixtures: {
      mode: 'accounts',
      empty: true,
      data: (d) => {
        const person = addPerson(d, 'amy')
        amy = { id: person.id, space: addSpace(d, '团队', { owner: person }).info.id }
      },
    },
    entries: ['/'],
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
    await request('/session')
    expect(m!.requests.at(-1)!.viewAs).toBe(view.id)
    await api.spaces()
    expect(m!.requests.at(-1)!.viewAs).toBe(view.id)
    // Every write and everything of the person's own is refused by the server: the client says so by the code.
    m!.fx.fail('POST', '/api/spaces', 403, { code: 'view_as_read_only' })
    m!.fx.fail('GET', '/api/notifications', 403, { code: 'view_as_read_only' })
    m!.fx.fail('GET', '/api/account', 403, { code: 'view_as_read_only' })
    for (const call of [
      () => request('/spaces', { method: 'POST', body: { name: 'x' } }),
      () => request('/notifications'),
      () => request('/account'),
    ])
      await expect(call()).rejects.toMatchObject({ code: 'view_as_read_only' })
    // The three refused requests were made with the header, so the server knew whose view they were.
    const refused = m!.requests.filter((r) =>
      [
        ['POST', '/api/spaces'],
        ['GET', '/api/notifications'],
        ['GET', '/api/account'],
      ].some(([method, url]) => r.method === method && r.url === url),
    )
    expect(refused).toHaveLength(3)
    expect(refused.map((r) => r.viewAs)).toEqual([view.id, view.id, view.id])

    // Leaving: the server is told without the header, and the tab leaves.
    let left = false
    const before = m!.requests.length
    await exitViewAs(amy.id, () => (left = true))
    expect(left).toBe(true)
    expect(viewAsId()).toBeNull()
    const end = m!.requests.slice(before).find((r) => r.method === 'DELETE')!
    expect(end.url).toBe(`/api/admin/users/${amy.id}/view-as`)
    expect(end.viewAs).toBeNull()
    // The view is over: the server refuses its id.
    m!.fx.fail('GET', '/api/spaces', 403, { code: 'view_as_ended' })
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
      { fixtures: true },
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
