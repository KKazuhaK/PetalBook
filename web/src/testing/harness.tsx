// Shared pieces for the rendering tests: a fixture server behind `fetch`, the
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
import { MOCK_ADMIN } from '@/api/fixtures/data/people'
import type { FixtureOptions } from '@/api/fixtures/data/model'
import { fixtureFetch } from '@/api/fixtures/fetch'
import { createFixtureServer, type FixtureServer } from '@/api/fixtures/server'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ThemeProvider } from '@/providers/theme'

// What a mounted page was given: the fixture server (src/api/fixtures).
export interface Mounted {
  fx: FixtureServer
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
export type FxMounted = Mounted

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

interface MountOptions {
  // Where to start; a function is given the server, for a scene made first.
  entries?: InitialEntry[] | ((server: FixtureServer) => InitialEntry[])
  index?: number
  signIn?: boolean
  delay?: (method: string, url: string) => number
  // A data router with these routes, for what needs one (`useBlocker`); `ui` is then not used.
  routes?: RouteObject[]
  // What the fixture server plays; `true` for the defaults.
  fixtures: FixtureOptions | true
}

export async function mount(ui: ReactNode, options: MountOptions): Promise<Mounted> {
  const now = () => Date.now()
  const fx = createFixtureServer({ now, ...(options.fixtures === true ? {} : options.fixtures) })
  const scene = options.entries ?? ['/']
  const requests: Mounted['requests'] = []
  const real = globalThis.fetch
  const mocked = fixtureFetch(fx)
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
  const entries = typeof scene === 'function' ? scene(fx) : scene
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
    fx,
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
