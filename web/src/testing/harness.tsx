// Shared pieces for the rendering tests: a mock server behind `fetch`, the
// providers the app wraps its pages in, and a router whose location can be read.

import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter, useLocation, type InitialEntry } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { api, markSignedIn } from '@/api/client'
import { mockFetch } from '@/api/mock/fetch'
import { createMockServer, MOCK_ADMIN, type MockOptions, type MockServer } from '@/api/mock/server'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ThemeProvider } from '@/providers/theme'

export interface Mounted {
  host: HTMLElement
  root: Root
  server: MockServer
  qc: QueryClient
  requests: { method: string; url: string; body?: string; signal?: AbortSignal | null }[]
  unmount: () => Promise<void>
}

// jsdom has no media queries, resize observers or scrolling; the components ask for them.
export function installDomShims() {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false, onchange: null })) as typeof window.matchMedia
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
  return <div data-testid="location" data-state={JSON.stringify(l.state ?? null)}>{l.pathname + l.search}</div>
}
export const locationText = () => document.querySelector('[data-testid="location"]')?.textContent ?? ''
export const locationState = () => JSON.parse(document.querySelector('[data-testid="location"]')?.getAttribute('data-state') ?? 'null')

export async function mount(
  ui: ReactNode,
  options: { entries?: InitialEntry[]; index?: number; server?: MockOptions; signIn?: boolean; delay?: (method: string, url: string) => number } = {},
): Promise<Mounted> {
  const server = createMockServer({ now: () => Date.now(), ...options.server })
  const requests: Mounted['requests'] = []
  const real = globalThis.fetch
  const mocked = mockFetch(server)
  // Every request is noted when it is made; some can be held back, so one can
  // be seen to be cancelled while it is on its way.
  globalThis.fetch = async (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    const method = init?.method ?? 'GET'
    requests.push({ method, url, body: typeof init?.body === 'string' ? init.body : undefined, signal: init?.signal })
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
  await act(async () =>
    root.render(
      <QueryClientProvider client={qc}>
        <ThemeProvider>
          <TooltipProvider>
            <MemoryRouter initialEntries={options.entries ?? ['/']} initialIndex={options.index}>
              {ui}
              <LocationProbe />
            </MemoryRouter>
            <Toaster />
          </TooltipProvider>
        </ThemeProvider>
      </QueryClientProvider>,
    ),
  )
  return {
    host,
    root,
    server,
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

// Waits (with the clock moving) until `check` holds.
export const settle = async (check: () => boolean, what: string, tries = 100) => {
  for (let i = 0; i < tries; i++) {
    if (check()) return
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20))
    })
  }
  throw new Error(`never happened: ${what}`)
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
export const type = (input: HTMLInputElement | HTMLSelectElement, value: string) =>
  act(async () => {
    const proto = input instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(proto, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }))
  })

export const press = (el: Element | Document, key: string, init: KeyboardEventInit = {}) =>
  act(async () => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }))
  })

export const byLabel = (label: string) => document.querySelector<HTMLElement>(`[aria-label="${label}"]`)
export const byText = (selector: string, text: string) => [...document.querySelectorAll<HTMLElement>(selector)].find((e) => e.textContent?.trim() === text) ?? null
