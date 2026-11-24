// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { markSignedIn } from '@/api/client'
import { mockFetch } from '@/api/mock/fetch'
import { createMockServer, MOCK_ADMIN, type MockServer } from '@/api/mock/server'
import { t } from '@/i18n'
import { oa } from '@/i18n/oauth'
import { keys } from '@/api/hooks'
import { tg } from '@/i18n/manage'
import {
  adminRequests,
  click,
  installDomShims,
  makePerson,
  PERSON_PASSWORD,
  settle,
  shareSpace,
  startView,
} from '@/testing/harness'

// The real route table, in the real app shell: where the addresses go, who gets
// the administration and what a space that was switched off shows.

beforeAll(installDomShims)

let host: HTMLElement | undefined
let root: Root | undefined
const realFetch = globalThis.fetch

beforeEach(() => {
  localStorage.clear()
})
afterEach(async () => {
  if (root) await act(async () => root!.unmount())
  host?.remove()
  document.body.innerHTML = ''
  globalThis.fetch = realFetch
  markSignedIn()
  root = host = undefined
  vi.resetModules()
})

// Starts the app at `path`, as a person signed in already (or in single-user mode). The router is made
// when the module loads, so each start loads it afresh, with the address already in place.
async function boot(
  path: string | (() => string),
  options: { mode: 'single' | 'accounts'; as?: string; oauth?: boolean; scene?: (server: MockServer) => void },
) {
  const server = createMockServer({ now: () => Date.now(), mode: options.mode, oauth: options.oauth })
  globalThis.fetch = mockFetch(server)
  options.scene?.(server)
  if (options.mode === 'accounts') {
    const login = await globalThis.fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: options.as ?? MOCK_ADMIN.username,
        password: options.as ? PERSON_PASSWORD : MOCK_ADMIN.password,
      }),
    })
    expect(login.status).toBe(200)
  }
  window.history.pushState({}, '', typeof path === 'function' ? path() : path)
  const { default: App } = await import('@/App')
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
  await act(async () => root!.render(<App />))
  return server
}
const text = () => document.body.textContent ?? ''
const where = () => window.location.pathname

describe('the addresses of users and invites', () => {
  it('/users and /settings/users go to the administration', async () => {
    await boot('/users', { mode: 'accounts' })
    await settle(() => where() === '/admin/users' && text().includes(t.users.createInvite), '/users')
    await act(async () => root!.unmount())
    root = undefined
    host!.remove()
    vi.resetModules()
    await boot('/settings/users', { mode: 'accounts' })
    await settle(() => where() === '/admin/users' && text().includes(t.users.createInvite), '/settings/users')
  })

  it('a member who follows an old link to them gets the not-found page, and a single-user server too', async () => {
    await boot('/users', { mode: 'accounts', as: 'dora', scene: (s) => void makePerson(s, 'dora') })
    await settle(() => text().includes(t.notFound), 'a member')
    expect(where()).toBe('/admin/users')
    expect(text()).not.toContain(t.users.createInvite)
    await act(async () => root!.unmount())
    root = undefined
    host!.remove()
    vi.resetModules()
    await boot('/admin/spaces', { mode: 'single' })
    await settle(() => text().includes(t.notFound), 'single-user')
  })
})

describe('the page an MCP client sends a person to', () => {
  it('is /oauth/consent: the page alone, with no app around it', async () => {
    let req = ''
    await boot(() => `/oauth/consent?req=${req}`, {
      mode: 'accounts',
      oauth: true,
      scene: (server) => void (req = server.startAuthorization(MOCK_ADMIN.username, { clientName: 'Notes helper' })),
    })
    await settle(() => text().includes(oa.consent.titleFor('Notes helper')), 'the consent page')
    expect(where()).toBe('/oauth/consent')
    expect(document.querySelector('[data-sidebar]')).toBeNull()
  })

  it('/oauth/authorize, which the server answers, is asked of the server again with a page load', async () => {
    const replace = vi.fn()
    const loc = window.location
    // jsdom does not navigate; what is asked of it is what is checked.
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...loc, pathname: '/oauth/authorize', search: '?client_id=x', replace },
    })
    try {
      const { default: App } = await import('@/App')
      globalThis.fetch = mockFetch(createMockServer({ now: () => Date.now(), mode: 'accounts' }))
      host = document.createElement('div')
      document.body.appendChild(host)
      root = createRoot(host)
      window.history.pushState({}, '', '/oauth/authorize?client_id=x')
      await act(async () => root!.render(<App />))
      await settle(() => replace.mock.calls.length > 0, 'the page load')
      expect(replace).toHaveBeenCalledWith('/oauth/authorize?client_id=x')
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: loc })
    }
  })
})

describe('the way into the administration', () => {
  const openUserMenu = async () => {
    await settle(() => document.querySelector(`[aria-label^="${t.userMenu.label}"]`) !== null, 'the user menu')
    await click(document.querySelector(`[aria-label^="${t.userMenu.label}"]`))
    await settle(() => document.querySelectorAll('[role="menuitem"]').length > 0, 'the menu')
    return [...document.querySelectorAll('[role="menuitem"]')].map((e) => e.textContent?.trim())
  }

  it('an administrator finds it in the user menu', async () => {
    await boot('/s/x/recent', { mode: 'accounts', scene: () => undefined })
    // The first space in use is the personal one: the address above is not a space of theirs.
    await settle(
      () =>
        text().includes(t.spaces.switcher) || document.querySelector(`[aria-label^="${t.userMenu.label}"]`) !== null,
      'the shell',
    )
    expect(await openUserMenu()).toEqual([t.userMenu.settings, t.userMenu.admin, t.userMenu.signOut])
    await click(
      [...document.querySelectorAll('[role="menuitem"]')].find((e) => e.textContent?.trim() === t.userMenu.admin),
    )
    await settle(() => where() === '/admin/users', 'the administration')
  })

  it('a member does not have the item', async () => {
    await boot('/s/x/recent', { mode: 'accounts', as: 'dora', scene: (s) => void makePerson(s, 'dora') })
    expect(await openUserMenu()).toEqual([t.userMenu.settings, t.userMenu.signOut])
  })

  it('a single-user server has no user menu at all, and no link to the administration anywhere', async () => {
    await boot('/s/default/recent', { mode: 'single' })
    await settle(() => document.querySelector(`[aria-label="${t.nav.settings}"]`) !== null, 'the shell')
    expect(document.querySelector(`[aria-label^="${t.userMenu.label}"]`)).toBeNull()
    expect(document.querySelector('a[href^="/admin"]')).toBeNull()
  })
})

describe('a space that an administrator switched off', () => {
  it("is in the member's list, marked, and opening it says why instead of failing", async () => {
    let space = ''
    await boot(() => `/s/${space}/recent`, {
      mode: 'accounts',
      as: 'erin',
      scene: (server) => {
        const owner = makePerson(server, 'erin')
        space = (owner.request('POST', '/api/spaces', { name: '停用的空间' }).body as { id: string }).id
        adminRequests(server)('POST', `/api/admin/spaces/${space}/disable`)
      },
    })
    await settle(() => text().includes(t.spaces.disabledTitle), 'the notice')
    expect(text()).toContain(t.spaces.disabledHelp)
    // It is asked for nothing: the server would refuse every request.
    expect(text()).not.toContain(t.editor.spaceMissing)
    // The switcher marks it.
    await click(document.querySelector(`[aria-label^="${t.spaces.switcher}"]`))
    await settle(() => document.querySelectorAll('[role="menuitemradio"]').length > 0, 'the switcher')
    const entry = [...document.querySelectorAll('[role="menuitemradio"]')].find((e) =>
      e.textContent?.includes('停用的空间'),
    )!
    expect(entry.textContent).toContain(t.spaces.disabledMark)
  })

  it('choosing a space in the switcher opens its home page', async () => {
    let other = ''
    await boot(() => '/', {
      mode: 'accounts',
      scene: (server) => {
        other = (adminRequests(server)('POST', '/api/spaces', { name: '另一个空间' }).body as { id: string }).id
      },
    })
    await settle(() => document.querySelector(`[aria-label^="${t.spaces.switcher}"]`) !== null, 'the switcher')
    await click(document.querySelector(`[aria-label^="${t.spaces.switcher}"]`))
    await settle(() => document.querySelectorAll('[role="menuitemradio"]').length > 1, 'the switcher')
    await click(
      [...document.querySelectorAll('[role="menuitemradio"]')].find((e) => e.textContent?.includes('另一个空间')),
    )
    await settle(
      () => window.location.pathname === `/s/${other}/` || window.location.pathname === `/s/${other}`,
      'its home page',
    )
  })

  it('an administrator is told where to switch it on again', async () => {
    let space = ''
    await boot(() => `/s/${space}/recent`, {
      mode: 'accounts',
      scene: (server) => {
        space = (adminRequests(server)('POST', '/api/spaces', { name: '关着的' }).body as { id: string }).id
        adminRequests(server)('POST', `/api/admin/spaces/${space}/disable`)
      },
    })
    await settle(() => text().includes(t.spaces.disabledAdminHelp), 'the notice')
    expect(document.querySelector('a[href="/admin/spaces"]')).not.toBeNull()
  })
})

describe('an administrator opens a space they are not a member of', () => {
  it('from the administration: the space asks why first, then its pages are there, read only, under the banner', async () => {
    let space = ''
    await boot('/admin/spaces', {
      mode: 'accounts',
      scene: (server) => {
        const owner = makePerson(server, 'frank')
        space = (owner.request('POST', '/api/spaces', { name: '别人的空间' }).body as { id: string }).id
        owner.request('POST', `/api/spaces/${space}/pages`, { meta: { title: '别人写的页面' }, body: 'x\n' })
      },
    })
    await settle(() => document.querySelector(`a[href="/s/${space}/"]`) !== null, 'the table')
    await click(document.querySelector(`a[href="/s/${space}/"]`))
    await settle(() => text().includes(tg.view.gateTitle), 'the gate')
    expect(text()).not.toContain('别人写的页面')
    await startView()
    await settle(() => text().includes('别人写的页面'), 'the pages')
    expect(where()).toBe(`/s/${space}/`)
    expect(document.querySelector(`[data-slot="home"]`)).not.toBeNull()
    expect(document.querySelector('[data-slot="admin-view-banner"]')!.textContent).toContain(tg.view.banner)
  })

  it('a member is told it does not exist', async () => {
    let space = ''
    await boot(() => `/s/${space}/recent`, {
      mode: 'accounts',
      as: 'gina',
      scene: (server) => {
        makePerson(server, 'gina')
        space = shareSpace(server, 'viewer', '另一个', 'hal').space
      },
    })
    await settle(() => text().includes(t.editor.spaceMissing), 'not found')
  })
})

describe('an administrator in a space they are not a member of', () => {
  const frankSpace = (server: MockServer) => {
    const owner = makePerson(server, 'frank')
    return (owner.request('POST', '/api/spaces', { name: '别人的空间', inbox: '待整理' }).body as { id: string }).id
  }
  const dialog = () => document.querySelector<HTMLElement>('[data-slot="settings-dialog"]')

  it('has the settings of that space: its name, its real inbox, its members and sync', async () => {
    let space = ''
    await boot(() => `/s/${space}/settings`, { mode: 'accounts', scene: (s) => void (space = frankSpace(s)) })
    // The old address of a space's settings lands on its sync section.
    await settle(
      () => where() === '/settings/sync' && dialog() !== null && dialog()!.textContent!.includes(t.remote.title),
      'the sync section',
    )
    expect(dialog()!.textContent).not.toContain(t.remote.ownerOnly)
    await click([...dialog()!.querySelectorAll('nav a')].find((a) => a.textContent === t.settingsUi.sections.general))
    await settle(
      () =>
        [...dialog()!.querySelectorAll('input')].some((i) => i.value === '别人的空间') &&
        [...dialog()!.querySelectorAll('input')].some((i) => i.value === '待整理'),
      'the name and the inbox',
    )
    expect(dialog()!.textContent).not.toContain(t.editor.spaceMissing)
    await click([...dialog()!.querySelectorAll('nav a')].find((a) => a.textContent === t.settingsUi.sections.members))
    await settle(() => dialog()!.textContent!.includes('frank'), 'the members')
  })

  it('cannot import into it, even in a view: a view only reads', async () => {
    let space = ''
    await boot(() => `/s/${space}/import`, { mode: 'accounts', scene: (s) => void (space = frankSpace(s)) })
    await settle(() => text().includes(tg.view.gateTitle), 'the gate')
    await startView()
    await settle(() => text().includes(t.permissions.readOnlyNote), 'read only')
    expect(text()).not.toContain(t.import.intro)
  })
})

describe('what the app knows of a space it is showing', () => {
  const frankSpace = (server: MockServer) => {
    const owner = makePerson(server, 'frank')
    const space = (owner.request('POST', '/api/spaces', { name: '别人的空间' }).body as { id: string }).id
    owner.request('POST', `/api/spaces/${space}/pages`, { meta: { title: '别人写的页面' }, body: 'x\n' })
    return { owner, space }
  }
  // Asks the server for something in the space, as any request of the app would.
  const ask = async (space: string) => {
    const { spaceApi } = await import('@/api/client')
    await act(
      async () =>
        void (await spaceApi(space)
          .tree()
          .catch(() => undefined)),
    )
  }

  it('an administrator viewing a space is shown the disabled notice when it is switched off meanwhile', async () => {
    let space = ''
    const server = await boot(() => `/s/${space}/recent`, {
      mode: 'accounts',
      scene: (s) => void (space = frankSpace(s).space),
    })
    await startView()
    await settle(() => text().includes('别人写的页面'), 'the pages')
    adminRequests(server)('POST', `/api/admin/spaces/${space}/disable`)
    await ask(space)
    await settle(() => text().includes(t.spaces.disabledTitle), 'the notice')
    // And back again.
    adminRequests(server)('POST', `/api/admin/spaces/${space}/enable`)
    const { queryClient } = await import('@/App')
    await act(async () => void (await queryClient.invalidateQueries({ queryKey: keys.viewedSpace(space) })))
    await settle(() => text().includes('别人写的页面'), 'the pages again')
  })

  it('a space opened from the administration is read as it is now, not as an older search left it', async () => {
    let space = ''
    const server = await boot('/admin/spaces', { mode: 'accounts', scene: (s) => void (space = frankSpace(s).space) })
    await settle(() => document.querySelector(`a[href="/s/${space}/"]`) !== null, 'the table')
    // Switched off by someone else; the table on screen does not know.
    adminRequests(server)('POST', `/api/admin/spaces/${space}/disable`)
    await click(document.querySelector(`a[href="/s/${space}/"]`))
    await settle(() => text().includes(t.spaces.disabledTitle), 'the notice')
  })

  it('a space that is deleted while it is viewed is not found', async () => {
    let space = ''
    const server = await boot(() => `/s/${space}/recent`, {
      mode: 'accounts',
      scene: (s) => void (space = frankSpace(s).space),
    })
    // A short window, so the recheck that a 404 inside it waits for comes soon:
    // waiting the real five seconds made the test depend on a runner's speed.
    // Set on the app's own copy of the module (the modules are loaded anew per test).
    const { notFoundRecheck } = await import('@/api/hooks')
    const window = notFoundRecheck.ms
    notFoundRecheck.ms = 200
    onTestFinished(() => void (notFoundRecheck.ms = window))
    await startView()
    await settle(() => text().includes('别人写的页面'), 'the pages')
    adminRequests(server)('DELETE', `/api/admin/spaces/${space}`)
    await ask(space)
    await settle(() => text().includes(t.editor.spaceMissing), 'not found')
  })

  // A member of a space, who is removed from it (or whose space is deleted) by someone else.
  const removable = () => {
    let space = ''
    let remove = () => {}
    const scene = (server: MockServer) => {
      const ivy = makePerson(server, 'ivy')
      const jon = makePerson(server, 'jon')
      space = (jon.request('POST', '/api/spaces', { name: '共享的' }).body as { id: string }).id
      jon.request('POST', `/api/spaces/${space}/members`, { username: 'ivy', role: 'editor' })
      remove = () => void jon.request('DELETE', `/api/spaces/${space}/members/${ivy.id}`)
    }
    return { scene, space: () => space, remove: () => remove() }
  }

  it('a member removed from a space meanwhile finds it gone once the window is looked at again', async () => {
    const x = removable()
    await boot(() => `/s/${x.space()}/recent`, { mode: 'accounts', as: 'ivy', scene: x.scene })
    await settle(
      () => text().includes(t.nav.quickNote) || document.querySelector(`[aria-label="${t.list.newPage}"]`) !== null,
      'the space',
    )
    x.remove()
    await act(async () => {
      window.dispatchEvent(new Event('visibilitychange'))
    })
    await settle(() => text().includes(t.editor.spaceMissing), 'not found')
  })

  it('a 404 from a space the list holds makes the list be read again, not offering the page text back', async () => {
    const x = removable()
    await boot(() => `/s/${x.space()}/recent`, { mode: 'accounts', as: 'ivy', scene: x.scene })
    await settle(() => document.querySelector(`[aria-label="${t.list.newPage}"]`) !== null, 'the space')
    x.remove()
    // The list was read a moment ago: a 404 is looked into only after a few seconds.
    const now = Date.now()
    const spy = vi.spyOn(Date, 'now').mockReturnValue(now + 10_000)
    try {
      await ask(x.space())
    } finally {
      spy.mockRestore()
    }
    await settle(() => text().includes(t.editor.spaceMissing), 'not found')
  })
})
