// `fetch` for the browser's side of a test: answers from a fixture server's routes, never from the network.

import type { FixtureServer } from './server.ts'

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

export function fixtureFetch(fx: FixtureServer): typeof fetch {
  return async (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname + input.search : input.url
    const r = fx.handle(
      init?.method ?? 'GET',
      url,
      typeof init?.body === 'string' ? init.body : undefined,
      new Headers(init?.headers),
      await bytesOf(init?.body),
    )
    const headers = new Headers({ 'Content-Type': 'application/json', ...r.headers })
    if (r.bytes) return new Response(r.bytes as BodyInit, { status: r.status, headers })
    return new Response(r.status === 204 || r.status === 303 ? null : JSON.stringify(r.body), {
      status: r.status,
      headers,
    })
  }
}
