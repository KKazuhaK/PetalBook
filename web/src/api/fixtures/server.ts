// The fixture server: the answers of the API as data, for the tests of the pages and for `npm run dev:mock`.
//
// It holds typed collections (data/) and a table of routes (routes/) that list, get, create, update and delete in
// them. It has no rules: no permission check, no validation beyond reading JSON, no quota, rate limit, audit or
// clock. The caller is the person the data says is signed in. What the real server refuses is the Go tests' to
// prove (and the contract suite's, which records the shapes in recorded/ that a test here checks the answers
// against). A test that needs a refusal or an exact answer says so:
//
//   fx.fail('POST', '/api/login', 429, { code: 'rate_limited' }, { headers: { 'Retry-After': '60' } })
//   fx.reply('GET', '/api/spaces/default/tree', { folders: [] })
//
// and asserts what the page sent with `fx.requests`. The one place the fixture keeps a protocol is a page's
// revision and draft (routes/drafts.ts), because the editor's save controller is correct only against it.

import type { ErrorCode } from '../types.ts'
import { createData, type FixtureData, type FixtureOptions } from './data/model.ts'
import { isReply, match, Refusal, type Route } from './kit.ts'
import type { Reply } from './kit.ts'
import { authRoutes } from './routes/auth.ts'
import { draftRoutes } from './routes/drafts.ts'
import { fileRoutes } from './routes/files.ts'
import { syncRoutes } from './routes/sync.ts'
import { quietRoutes } from './routes/quiet.ts'
import { viewRoutes } from './routes/views.ts'
import { areaRoutes } from './routes/area.ts'

export interface Sent {
  method: string
  // The path with its query, as the page asked.
  url: string
  body?: string
  // The Content-Type the page sent.
  type?: string | null
  scope?: string | null
  viewAs?: string | null
}

interface Override {
  method: string
  pattern: string
  times: number
  answer: Reply
}

export interface FixtureServer {
  data: FixtureData
  requests: Sent[]
  sent: FixtureData['sent']
  routes: Route[]
  handle(method: string, target: string, body?: string, headers?: Headers | null, raw?: Uint8Array): Reply
  fail(
    method: string,
    path: string,
    status: number,
    error: { code: ErrorCode; message?: string; [extra: string]: unknown },
    opts?: { times?: number; headers?: Record<string, string> },
  ): void
  reply(method: string, path: string, body: unknown, opts?: { status?: number; times?: number }): void
  clear(): void
  call(method: string, path: string, body?: unknown): { status: number; body: any }
  on(space?: string): (method: string, path: string, body?: unknown) => { status: number; body: any }
  endSession(reason: NonNullable<FixtureData['auth']['ended']>): void
}

export function createFixtureServer(options: FixtureOptions = {}): FixtureServer {
  const now = options.now ?? Date.now
  const data = createData({ ...options, now })
  const requests: Sent[] = []
  const overrides: Override[] = []
  const routes: Route[] = [...authRoutes, ...fileRoutes, ...draftRoutes, ...viewRoutes, ...syncRoutes, ...quietRoutes]
  routes.push(...areaRoutes)

  function handle(
    method: string,
    target: string,
    body?: string,
    headers: Headers | null = null,
    raw?: Uint8Array,
  ): Reply {
    method = method.toUpperCase()
    const [path, search = ''] = target.split('?')
    requests.push({
      method,
      url: target,
      ...(body !== undefined ? { body } : {}),
      type: headers?.get('content-type') ?? null,
      scope: headers?.get('petalbook-scope') ?? null,
      viewAs: headers?.get('petalbook-view-as') ?? null,
    })
    const at = overrides.findLastIndex((o) => o.method === method && match(o.pattern, path))
    if (at >= 0) {
      const o = overrides[at]
      if (--o.times <= 0) overrides.splice(at, 1)
      return o.answer
    }
    for (const [m, pattern, handler] of routes) {
      if (m !== method) continue
      const params = match(pattern, path)
      if (!params) continue
      let json: unknown = {}
      // Only a JSON request is parsed; an upload's bytes go through as `raw`.
      const isJson = !headers?.get('content-type') || /json/i.test(headers.get('content-type') ?? '')
      if (body && isJson) {
        try {
          json = JSON.parse(body)
        } catch {
          return fail(400, 'invalid', 'not JSON')
        }
      }
      try {
        const out = handler({
          fx,
          data,
          method,
          path,
          params,
          query: new URLSearchParams(search),
          body: json,
          raw,
          now,
        })
        if (isReply(out)) return out
        return out === undefined ? { status: 204 } : { status: 200, body: out }
      } catch (e) {
        if (e instanceof Refusal) return fail(e.status, e.code, e.message, e.extra)
        throw e
      }
    }
    return fail(404, 'not_found', `the fixture has no route for ${method} ${path}`)
  }

  const fail = (status: number, code: string, message = '', extra: Record<string, unknown> = {}): Reply => ({
    status,
    body: { error: code, message: message || code, ...extra },
  })

  const fx: FixtureServer = {
    data,
    requests,
    sent: data.sent,
    handle,
    // Make the next `times` matching requests (all of them by default) answer a refusal.
    fail(
      method: string,
      path: string,
      status: number,
      error: { code: ErrorCode; message?: string; [extra: string]: unknown },
      opts: { times?: number; headers?: Record<string, string> } = {},
    ) {
      const { code, message, ...extra } = error
      const answer = { ...fail(status, code, message, extra), ...(opts.headers ? { headers: opts.headers } : {}) }
      overrides.push({ method: method.toUpperCase(), pattern: path, times: opts.times ?? Infinity, answer })
    },
    // Make the matching requests answer exactly `body`.
    reply(method: string, path: string, body: unknown, opts: { status?: number; times?: number } = {}) {
      overrides.push({
        method: method.toUpperCase(),
        pattern: path,
        times: opts.times ?? Infinity,
        answer: { status: opts.status ?? 200, body },
      })
    },
    // Drop every `fail` and `reply`.
    clear: () => void (overrides.length = 0),
    // A request made behind the page's back, as another browser would: {status, body}.
    call(method: string, path: string, body?: unknown): { status: number; body: any } {
      const r = handle(method, path, body === undefined ? undefined : JSON.stringify(body))
      return { status: r.status, body: r.body }
    },
    // The same on one space's routes.
    on:
      (space = 'default') =>
      (method: string, path: string, body?: unknown) =>
        fx.call(method, `/api/spaces/${space}${path}`, body),
    // The session ends for a reason the person did not ask for (what the sign-in page then says).
    endSession(reason: NonNullable<FixtureData['auth']['ended']>) {
      data.auth.signedIn = null
      data.auth.ended = reason
    },
    routes,
  }
  return fx
}
