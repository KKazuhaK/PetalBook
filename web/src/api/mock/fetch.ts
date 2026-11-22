// Route `fetch("/api/...")` to a mock server, for tests. It stands in for one
// browser: cookies the server sets are kept and sent back, unless `jar` is
// false (a test that carries the session by hand, as it must against a real
// server).

import type { MockServer } from './server.ts'

// A file's bytes from whatever a request body can be (the client sends a Blob).
async function bytesOf(body: BodyInit | null | undefined): Promise<Uint8Array | undefined> {
  if (body === undefined || body === null || typeof body === 'string') return undefined
  // Not `instanceof`: under jsdom the typed arrays of a test and of the library can be different realms.
  if (ArrayBuffer.isView(body)) return new Uint8Array(body.buffer, body.byteOffset, body.byteLength)
  if (Object.prototype.toString.call(body) === '[object ArrayBuffer]') return new Uint8Array(body as ArrayBuffer)
  const blob = body as { arrayBuffer?: () => Promise<ArrayBuffer> }
  if (typeof blob.arrayBuffer === 'function') return new Uint8Array(await blob.arrayBuffer())
  return undefined
}

export function mockFetch(
  server: MockServer,
  hooks: { onRequest?: (method: string, url: string, body?: string, init?: RequestInit) => void; jar?: boolean } = {},
): typeof fetch {
  const cookies = new Map<string, string>()
  return async (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    const method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? init.body : undefined
    const raw = await bytesOf(init?.body)
    hooks.onRequest?.(method, url, body, init)
    const headers = new Headers(init?.headers)
    const cookie = headers.get('cookie') ?? (hooks.jar === false ? null : [...cookies].map(([k, v]) => `${k}=${v}`).join('; '))
    const r = server.handle(method, url, body, headers.get('content-type'), cookie, raw)
    const out = new Headers({ 'Content-Type': 'application/json', ...r.headers })
    if (hooks.jar !== false && r.headers?.['Set-Cookie']) {
      const [pair, ...attrs] = r.headers['Set-Cookie'].split(';').map((x) => x.trim())
      const i = pair.indexOf('=')
      if (attrs.includes('Max-Age=0') || pair.slice(i + 1) === '') cookies.delete(pair.slice(0, i))
      else cookies.set(pair.slice(0, i), pair.slice(i + 1))
    }
    if (r.bytes) return new Response(r.bytes as BodyInit, { status: r.status, headers: out })
    return new Response(r.status === 204 || r.status === 303 ? null : JSON.stringify(r.body), { status: r.status, headers: out })
  }
}
