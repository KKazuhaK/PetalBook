// Shared pieces for the rendering tests: a mock server behind `fetch`, the
// providers the app wraps its pages in, and a router whose location can be read.

import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import {
  createMemoryRouter,
  MemoryRouter,
  Outlet,
  RouterProvider,
  useLocation,
  type InitialEntry,
  type RouteObject,
} from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { api, markSignedIn } from '@/api/client'
import { mockFetch } from '@/api/mock/fetch'
import { createMockServer, MOCK_ADMIN, type MockOptions, type MockServer } from '@/api/mock/server'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import { fixtureFetch } from '@/api/fixtures/fetch'
import { createFixtureServer, type FixtureServer } from '@/api/fixtures/server'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ThemeProvider } from '@/providers/theme'

// What a mounted page was given: the mock server, or (with `fixtures`) a fixture server (src/api/fixtures).
export interface MountedBase {
  host: HTMLElement
  root: Root
  qc: QueryClient
  requests: {
    method: string
    url: string
    body?: string
    signal?: AbortSignal | null
    scope?: string | null
    viewAs?: string | null
  }[]
  unmount: () => Promise<void>
}
export interface Mounted extends MountedBase {
  server: MockServer
}
export interface FxMounted extends MountedBase {
  fx: FixtureServer
}

// jsdom has no media queries, resize observers or scrolling; the components ask for them.
export function installDomShims() {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  window.matchMedia ??= ((query: string) => ({
    matches: false,
    media: query,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
    onchange: null,
  })) as typeof window.matchMedia
  window.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Element.prototype.scrollIntoView ??= () => {}
}

// Shows where the router is, for assertions.
export function LocationProbe() {
  const l = useLocation()
  return (
    <div data-testid="location" data-state={JSON.stringify(l.state ?? null)}>
      {l.pathname + l.search}
    </div>
  )
}
export const locationText = () => document.querySelector('[data-testid="location"]')?.textContent ?? ''
export const locationState = () =>
  JSON.parse(document.querySelector('[data-testid="location"]')?.getAttribute('data-state') ?? 'null')

interface MountOptions<S> {
  // Where to start; a function is given the server, for a scene made first (see `shareSpace`).
  entries?: InitialEntry[] | ((server: S) => InitialEntry[])
  index?: number
  signIn?: boolean
  delay?: (method: string, url: string) => number
  // A data router with these routes, for what needs one (`useBlocker`); `ui` is then not used.
  routes?: RouteObject[]
}
type MockMount = MountOptions<MockServer> & { server?: MockOptions; fixtures?: undefined }
type FixtureMount = MountOptions<FixtureServer> & { fixtures: FixtureOptions | true; server?: undefined }

export async function mount(ui: ReactNode, options: FixtureMount): Promise<FxMounted>
export async function mount(ui: ReactNode, options?: MockMount): Promise<Mounted>
export async function mount(ui: ReactNode, options: MockMount | FixtureMount = {}): Promise<Mounted | FxMounted> {
  const now = () => Date.now()
  const fx = options.fixtures
    ? createFixtureServer({ now, ...(options.fixtures === true ? {} : options.fixtures) })
    : null
  const server = fx ? null : createMockServer({ now, ...options.server })
  const scene = (options.entries ?? ['/']) as InitialEntry[] | ((s: MockServer & FixtureServer) => InitialEntry[])
  const requests: MountedBase['requests'] = []
  const real = globalThis.fetch
  const mocked = fx ? fixtureFetch(fx) : mockFetch(server!)
  // Every request is noted when it is made; some can be held back, so one can
  // be seen to be cancelled while it is on its way.
  globalThis.fetch = async (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    const method = init?.method ?? 'GET'
    requests.push({
      method,
      url,
      body: typeof init?.body === 'string' ? init.body : undefined,
      signal: init?.signal,
      scope: new Headers(init?.headers).get('petalbook-scope'),
      viewAs: new Headers(init?.headers).get('petalbook-view-as'),
    })
    const ms = options.delay?.(method, url) ?? 0
    if (ms > 0) await new Promise((r) => setTimeout(r, ms))
    return mocked(input, init)
  }
  markSignedIn()
  if (options.signIn) await api.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const entries = typeof scene === 'function' ? scene((fx ?? server) as MockServer & FixtureServer) : scene
  const router = options.routes
    ? createMemoryRouter(
        [
          {
            element: (
              <>
                <Outlet />
                <LocationProbe />
              </>
            ),
            children: options.routes,
          },
        ],
        { initialEntries: entries, initialIndex: options.index },
      )
    : null
  await act(async () =>
    root.render(
      <QueryClientProvider client={qc}>
        <ThemeProvider>
          <TooltipProvider>
            {router ? (
              <RouterProvider router={router} />
            ) : (
              <MemoryRouter initialEntries={entries} initialIndex={options.index}>
                {ui}
                <LocationProbe />
              </MemoryRouter>
            )}
            <Toaster />
          </TooltipProvider>
        </ThemeProvider>
      </QueryClientProvider>,
    ),
  )
  return {
    host,
    root,
    ...(fx ? { fx } : { server: server! }),
    qc,
    requests,
    unmount: async () => {
      await act(async () => root.unmount())
      host.remove()
      document.body.innerHTML = ''
      globalThis.fetch = real
      markSignedIn()
    },
  }
}

// Waits (with the clock moving) until `check` holds. The budget is time, not a
// number of tries: a try takes as long as the runner needs for 20 ms, so a count
// would give a busy machine less time, not the same. Every wait gets the same five
// seconds; `tries` (20 ms steps) can only ask for more.
export const settle = async (check: () => boolean, what: string, tries = 0) => {
  const until = Date.now() + Math.max(5_000, tries * 20)
  for (;;) {
    if (check()) return
    if (Date.now() >= until) throw new Error(`never happened: ${what}`)
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20))
    })
  }
}

export const click = (el: Element | null | undefined) => {
  if (!el) throw new Error('nothing to click')
  return act(async () => {
    el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    ;(el as HTMLElement).click()
  })
}

// Types into a controlled input the way a person would.
export const type = (input: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, value: string) =>
  act(async () => {
    const proto =
      input instanceof HTMLSelectElement
        ? HTMLSelectElement.prototype
        : input instanceof HTMLTextAreaElement
          ? HTMLTextAreaElement.prototype
          : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }))
  })

export const press = (el: Element | Document, key: string, init: KeyboardEventInit = {}) =>
  act(async () => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }))
  })

// An administrator reads a space they are not a member of only in an audited view: this answers the gate that asks why.
export async function startView(reason = '检查一个举报') {
  await settle(() => document.querySelector('[data-slot="view-form"] textarea') !== null, 'the form that asks why')
  await type(document.querySelector<HTMLTextAreaElement>('[data-slot="view-form"] textarea')!, reason)
  await click(document.querySelector<HTMLElement>('[data-slot="view-form"] button[type="submit"]'))
}

export const byLabel = (label: string) => document.querySelector<HTMLElement>(`[aria-label="${label}"]`)
export const byText = (selector: string, text: string) =>
  [...document.querySelectorAll<HTMLElement>(selector)].find((e) => e.textContent?.trim() === text) ?? null

// --- people and shared spaces, made behind the app's back ----------------------------
//
// The tests drive the mock server directly (the way another person's browser
// would) to set a scene: a second person, a space they own, the signed-in
// administrator a member of it in some role.

export const WRITE_HEADERS = 'application/json'

// One request to the server as the person holding `cookie` (a Set-Cookie value's
// `name=value`, or null for no session).
export function asPerson(server: MockServer, cookie: string | null) {
  return (method: string, path: string, body?: unknown) => {
    const r = server.handle(method, path, body === undefined ? undefined : JSON.stringify(body), WRITE_HEADERS, cookie)
    if (r.status >= 400) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.body)}`)
    return r
  }
}
const cookieOf = (setCookie: string | undefined) => setCookie?.split(';')[0] ?? null

// Writes site settings, one request each (PUT /api/admin/settings/{key}): the policy that once had endpoints of its own.
export function putSettings(
  call: (method: string, path: string, body?: unknown) => unknown,
  values: Record<string, unknown>,
) {
  for (const [key, value] of Object.entries(values)) call('PUT', `/api/admin/settings/${key}`, { value })
}

export const adminRequests = (server: MockServer) => {
  const login = server.handle(
    'POST',
    '/api/login',
    JSON.stringify({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password }),
    WRITE_HEADERS,
    null,
  )
  return asPerson(server, cookieOf(login.headers?.['Set-Cookie']))
}

export const PERSON_PASSWORD = 'a-long-password'

// An account made through an invite. `request` acts as that person.
export function makePerson(server: MockServer, username: string, role: 'member' | 'admin' = 'member') {
  const invite = adminRequests(server)('POST', '/api/invites', { role }).body as { token: string }
  const r = asPerson(server, null)('POST', '/api/invites/accept', {
    token: invite.token,
    username,
    name: `名 ${username}`,
    password: PERSON_PASSWORD,
  })
  const user = (r.body as { user: { id: string } }).user
  return {
    id: user.id,
    username,
    password: PERSON_PASSWORD,
    request: asPerson(server, cookieOf(r.headers?.['Set-Cookie'])),
  }
}

// A space that `owner` made, which the signed-in administrator belongs to in `role`.
export function shareSpace(
  server: MockServer,
  role: 'owner' | 'editor' | 'viewer',
  name = '团队',
  ownerName = 'sharer',
): { space: string; owner: ReturnType<typeof makePerson> } {
  const owner = makePerson(server, ownerName)
  const space = (owner.request('POST', '/api/spaces', { name }).body as { id: string }).id
  owner.request('POST', `/api/spaces/${space}/members`, { username: MOCK_ADMIN.username, role })
  return { space, owner }
}

// Opens a tenant's own page from the Tenants list (the link named after it), and one of its tabs (by the tab's label).
export async function openTenantPage(openLabel: string, tabLabel?: string) {
  await settle(() => byLabel(openLabel) !== null, 'the list')
  await click(byLabel(openLabel))
  if (tabLabel === undefined) return
  const tab = () =>
    [...document.querySelectorAll<HTMLElement>('nav[aria-label] a')].find((a) => a.textContent?.trim() === tabLabel)
  await settle(() => tab() !== undefined, `the ${tabLabel} tab`)
  await click(tab())
}
