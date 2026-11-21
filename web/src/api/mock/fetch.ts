// Route `fetch("/api/...")` to a mock server, for tests.

import type { MockServer } from './server.ts'

export function mockFetch(server: MockServer, hooks: { onRequest?: (method: string, url: string, body?: string, init?: RequestInit) => void } = {}): typeof fetch {
  return async (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    const method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? init.body : undefined
    hooks.onRequest?.(method, url, body, init)
    const headers = new Headers(init?.headers)
    const r = server.handle(method, url, body, headers.get('content-type'))
    return new Response(r.status === 204 ? null : JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
  }
}
