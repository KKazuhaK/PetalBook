// The API contract (docs/api.md), asserted through the real client and real
// `fetch`. The same suite runs against either implementation:
//
//   npm run test:contract
//       the in-memory mock, twice: as a single-user server and in accounts mode
//   PETALBOOK_API=http://127.0.0.1:18080 npm run test:contract
//       a real server started on an EMPTY space (`serve --space DIR`): single-user mode
//   PETALBOOK_API=http://127.0.0.1:18080 PETALBOOK_CONTRACT_USER=admin PETALBOOK_CONTRACT_PASSWORD=... npm run test:contract
//       a real accounts-mode server (`serve --data DIR`) whose administrator already exists:
//       the suite signs in, carries the session cookie by hand (Node's fetch has no cookie
//       jar) and works in the first space of `GET /api/spaces`. It creates its own members
//       through invites, under names made unique per run, and disables them at the end.
//
// In single-user mode the page, folder, tag and draft tests run against space
// `default`, and the account endpoints are asserted to answer 404. Sign-in
// through OpenID Connect is exercised against the mock only: a real server in
// this setup has no provider. Tests that need a server's clock (the server's
// own draft commits) or its rate limiter are marked "mock only" in their names:
// a real server would have to be waited on, or locked out, to run them.
//
// A real accounts server throttles failed sign-ins and bad invite tokens per
// client address (ten in a window, then a wait until the window ends). The suite
// makes more failures than that, so it raises the cap and the per-minute limit
// for the run and puts them back afterwards. It waits out a short 429 and fails
// at once on a long one, which means a cap was reached.
//
// History is written by Git commits, which a real server makes a few seconds
// after the file is written and may join (two quick saves can be one commit),
// so the tests against a real server wait for the newest version to show up and
// never count versions exactly; the mock, which commits on the spot, is held to
// exact counts in mock.test.ts. The remote repository tests need a remote: on
// a real server only what needs none is asserted (a local space, the key,
// addresses and hosts that are refused); the four outcomes of connecting, the
// states and the 503s are asserted on the mock. Set PETALBOOK_CONTRACT_HOSTS_OPEN=1
// when the server was started with `--remote-host '*'` (no host is then refused).
//
// It creates all its own data (under names made unique per run) and never
// assumes anything else is in the space. Where the mock and the server
// differ, the mock is wrong.

import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { ApiError, api, fileUrl, markSignedIn, spaceApi, type SpaceApi } from './client'
import { adminApi } from './admin'
import { mockFetch } from './mock/fetch'
import { sha256Hex } from './mock/sha256'
import { createMockServer, MOCK_ADMIN, type MockServer } from './mock/server'
import type {
  AdminSpace,
  CatalogBatch,
  CatalogEntry,
  ChangeItem,
  ListParams,
  Member,
  Page,
  PageSummary,
  Problem,
  ProblemKind,
  SpaceInfo,
  User,
} from './types'

declare const process: { env: Record<string, string | undefined> }
const REAL = process.env.PETALBOOK_API?.replace(/\/+$/, '')
const CONTRACT_USER = process.env.PETALBOOK_CONTRACT_USER
const CONTRACT_PASSWORD = process.env.PETALBOOK_CONTRACT_PASSWORD
const HOSTS_OPEN = process.env.PETALBOOK_CONTRACT_HOSTS_OPEN === '1'
const realFetch = globalThis.fetch

interface Target {
  name: string
  mode: 'single' | 'accounts'
}
const targets: Target[] = REAL
  ? [
      {
        name: CONTRACT_USER && CONTRACT_PASSWORD ? 'real server, accounts mode' : 'real server, single-user mode',
        mode: CONTRACT_USER && CONTRACT_PASSWORD ? 'accounts' : 'single',
      },
    ]
  : [
      { name: 'mock, single-user mode', mode: 'single' },
      { name: 'mock, accounts mode', mode: 'accounts' },
    ]

const JSON_HEADERS = { 'Content-Type': 'application/json' }

// A browser: its own cookies, carried by hand, so the same code works against
// the mock and against a real server over HTTP.
interface Browser {
  fetch: typeof fetch
  cookies: Map<string, string>
}
function newBrowser(base: typeof fetch): Browser {
  const cookies = new Map<string, string>()
  const f: typeof fetch = async (input, init) => {
    const headers = new Headers(init?.headers)
    if (cookies.size) headers.set('Cookie', [...cookies].map(([k, v]) => `${k}=${v}`).join('; '))
    const res = await base(input, { ...init, headers })
    for (const line of res.headers.getSetCookie()) {
      const [pair, ...attrs] = line.split(';').map((x) => x.trim())
      const i = pair.indexOf('=')
      const name = pair.slice(0, i)
      if (pair.slice(i + 1) === '' || attrs.some((a) => /^max-age=0$/i.test(a))) cookies.delete(name)
      else cookies.set(name, pair.slice(i + 1))
    }
    return res
  }
  return { fetch: f, cookies }
}

const failure = async (p: Promise<unknown>): Promise<ApiError> => {
  try {
    await p
  } catch (e) {
    expect(e).toBeInstanceOf(ApiError)
    return e as ApiError
  }
  throw new Error('expected the request to fail')
}

for (const target of targets) {
  describe(target.name, () => suite(target))
}

function suite(target: Target) {
  const accounts = target.mode === 'accounts'
  // Everything this run creates lives under this folder / carries this suffix.
  const run = Math.random().toString(36).slice(2, 8)
  const root = `c${run}`

  let server: MockServer | undefined
  let clock = Date.now()
  let base: typeof fetch
  let admin: Browser
  let spaceId = 'default'
  let S: SpaceApi
  let SP_PAGES = ''
  const created: { users: string[] } = { users: [] }
  // A real accounts server's sign-in limits as they were before the run.
  let savedLimits: { perMinute: number; maxFailures: number } | undefined

  const browser = () => newBrowser(base)
  // Run `fn` as another browser (the client reads `globalThis.fetch`).
  const as = async <T>(b: Browser, fn: () => Promise<T>): Promise<T> => {
    const prev = globalThis.fetch
    globalThis.fetch = b.fetch
    try {
      return await fn()
    } finally {
      globalThis.fetch = prev
    }
  }
  // A plain request: status, parsed body and headers, whatever the answer. A
  // real server throttles the endpoints anyone can reach (invites, setup,
  // sign-in) per client address, so a 429 there is waited out, as a polite
  // client would, unless the test is about the 429 itself.
  const call = async (
    b: Browser,
    method: string,
    path: string,
    body?: unknown,
    headers: Record<string, string> = JSON_HEADERS,
    opts: { expect429?: boolean } = {},
  ): Promise<{ status: number; json: any; headers: Headers; res: Response }> => {
    const res = await b.fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
      redirect: 'manual',
    })
    if (res.status === 429 && !opts.expect429 && REAL) {
      const wait = Number(res.headers.get('Retry-After')) || 1
      // A wait longer than a test may take is a cap the run has used up, not a
      // per-minute pause: say so at once instead of retrying until the timeout.
      if (wait > 20)
        throw new Error(`${method} ${path}: 429 with Retry-After ${wait}s; the run has reached a failure cap`)
      await new Promise((r) => setTimeout(r, wait * 1000 + 100))
      return call(b, method, path, body, headers, opts)
    }
    const text = await res.text()
    let json: any = null
    try {
      json = text ? JSON.parse(text) : null
    } catch {
      // not JSON
    }
    return { status: res.status, json, headers: res.headers, res }
  }

  beforeAll(async () => {
    if (REAL) base = (input, init) => realFetch(REAL + String(input), init)
    else {
      server = createMockServer({ empty: true, mode: target.mode, now: () => clock, oidc: null, oauth: true })
      base = mockFetch(server, { jar: false })
    }
    admin = browser()
    globalThis.fetch = admin.fetch
    if (accounts) {
      const user = REAL ? CONTRACT_USER! : MOCK_ADMIN.username
      const password = REAL ? CONTRACT_PASSWORD! : MOCK_ADMIN.password
      await api.login({ username: user, password })
      if (REAL) {
        // The suite signs in far more often than ten times a minute from one
        // address, and makes more failures from it (wrong passwords, bad
        // invite, email and reset tokens) than the per-address cap allows in
        // its window. Once the cap is reached every public endpoint, invite
        // accept included, answers 429 for the rest of the window, so both
        // limits are raised for the run and put back afterwards.
        const got = await fetch('/api/admin/security')
        if (got.status !== 200)
          throw new Error(`could not read the security settings: ${got.status} ${await got.text()}`)
        const { perMinute, maxFailures } = (
          (await got.json()) as { limits: { perMinute: number; maxFailures: number } }
        ).limits
        savedLimits = { perMinute, maxFailures }
        const r = await fetch('/api/admin/security', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ limits: { perMinute: 1000, maxFailures: 1000 } }),
        })
        if (r.status !== 200)
          throw new Error(`could not raise the sign-in limits for the run: ${r.status} ${await r.text()}`)
      }
      spaceId = (await api.spaces()).spaces[0].id
    }
    S = spaceApi(spaceId)
    SP_PAGES = `/api/spaces/${spaceId}/pages`
  })
  afterAll(async () => {
    globalThis.fetch = admin.fetch
    for (const id of created.users) await adminApi.updateUser(id, { disabled: true }).catch(() => undefined)
    if (REAL && accounts && savedLimits) {
      await fetch('/api/admin/security', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ limits: savedLimits }),
      }).catch(() => undefined)
    }
    globalThis.fetch = realFetch
    markSignedIn()
  })

  const mk = (folder: string, meta: Record<string, unknown> = {}, body = '') => S.createPage({ folder, meta, body })
  const walk = async (params: Parameters<SpaceApi['listPages']>[0], limit = 3) => {
    const items: PageSummary[] = []
    let cursor = ''
    for (let guard = 0; guard < 200; guard++) {
      const r = await S.listPages({ ...params, limit, cursor: cursor || undefined })
      items.push(...r.items)
      cursor = r.next
      if (!cursor) return items
    }
    throw new Error('cursor never ended')
  }
  const ids = (items: PageSummary[]) => items.map((i) => i.id)

  describe('meta and session', () => {
    it('meta reports a version and the spaces the caller can open, with role and personal', async () => {
      const m = await api.meta()
      expect(typeof m.version).toBe('string')
      const mine = m.spaces.find((s) => s.id === spaceId)
      expect(mine).toBeDefined()
      expect(typeof mine!.name).toBe('string')
      expect(typeof mine!.role).toBe('string')
      expect(typeof mine!.personal).toBe('boolean')
    })

    it('every space has an inbox: a folder path that is not empty, the same in meta and in the list of spaces', async () => {
      const m = await api.meta()
      const { spaces } = await api.spaces()
      expect(spaces.length).toBeGreaterThanOrEqual(1)
      for (const s of [...m.spaces, ...spaces]) {
        expect(typeof s.inbox, s.id).toBe('string')
        expect(s.inbox.length, s.id).toBeGreaterThan(0)
      }
      expect(m.spaces.find((s) => s.id === spaceId)!.inbox).toBe(spaces.find((s) => s.id === spaceId)!.inbox)
    })

    it('session says the mode and who is signed in, and is never cached', async () => {
      const r = await call(admin, 'GET', '/api/session')
      expect(r.status).toBe(200)
      expect(r.json.mode).toBe(target.mode)
      expect(r.json.setup).toBe(false)
      // A session, not an API token: the field is there and null.
      expect(r.json.token).toBeNull()
      expect(r.headers.get('Cache-Control')).toMatch(/no-store/)
      expect(r.json.user).toMatchObject({ role: 'admin', disabled: false })
      expect(typeof r.json.user.id).toBe('string')
      expect(typeof r.json.user.username).toBe('string')
      expect(typeof r.json.user.name).toBe('string')
      if (accounts) {
        expect(r.json.methods.password).toBe(true)
        expect(r.json.methods).toHaveProperty('oidc')
      } else {
        expect(r.json.user).toMatchObject({ id: 'local', username: 'local' })
        expect(r.json.methods).toBeUndefined()
      }
    })

    it("every space carries the caller's role and permissions, and whether it is disabled; meta and the list say the same", async () => {
      const m = await api.meta()
      const { spaces } = await api.spaces()
      const mine = spaces.find((s) => s.id === spaceId)!
      // The first space is the caller's own: they own it and may do everything in it.
      expect(mine.role).toBe('owner')
      expect(mine.permissions).toEqual(['read', 'write', 'members', 'manage'])
      expect(mine.disabled).toBe(false)
      expect(m.spaces.find((s) => s.id === spaceId)).toEqual(mine)
      for (const s of spaces) {
        expect(['owner', 'editor', 'viewer', ''], s.id).toContain(s.role)
        expect(Array.isArray(s.permissions), s.id).toBe(true)
        expect(typeof s.disabled, s.id).toBe('boolean')
      }
    })

    it("spaces lists the caller's spaces, the personal one first", async () => {
      const { spaces } = await api.spaces()
      expect(spaces.length).toBeGreaterThanOrEqual(1)
      expect(spaces.map((s) => s.id)).toContain(spaceId)
      if (accounts) expect(spaces[0].personal).toBe(true)
      else expect(spaces).toEqual([expect.objectContaining({ id: 'default', role: 'owner' })])
    })
  })

  describe.skipIf(accounts)('single-user server only (skipped on an accounts server): no accounts', () => {
    it('the sign-in, account, user, invite and space-management endpoints answer 404', async () => {
      const probes: [string, string, unknown?][] = [
        ['POST', '/api/login', { username: 'a', password: 'b' }],
        ['POST', '/api/setup', { token: 'x', username: 'ab', password: 'password1' }],
        ['POST', '/api/auth/oidc/start', { intent: 'login' }],
        ['GET', '/api/auth/providers'],
        ['GET', '/api/captcha?ctx=login'],
        ['POST', '/api/auth/passkey/begin', {}],
        ['POST', '/api/auth/oidc/company/start', { intent: 'login' }],
        ['GET', '/api/account/passkeys'],
        ['GET', '/api/admin/security'],
        ['GET', '/api/admin/auth/providers'],
        ['GET', '/api/admin/audit'],
        ['GET', '/api/account'],
        ['GET', '/api/users'],
        ['GET', '/api/invites'],
        ['GET', '/api/account/tokens'],
        ['POST', '/api/account/tokens', { name: 'x', spaces: [spaceId] }],
        ['DELETE', '/api/account/tokens/NOSUCHTOKN'],
        ['GET', '/api/admin/access'],
        ['GET', '/api/admin/tokens'],
        ['GET', '/api/account/grants'],
        ['DELETE', '/api/account/grants/NOSUCHGRANT'],
        ['GET', '/api/oauth/consent?req=x'],
        ['POST', '/api/oauth/consent', { req: 'x', approve: false }],
        ['GET', '/api/admin/grants'],
        ['POST', '/api/invites/check', { token: 'x' }],
        ['POST', '/api/invites/accept', { token: 'x', username: 'ab', password: 'password1' }],
        ['POST', '/api/spaces', { name: 'Other' }],
        ['PATCH', `/api/spaces/${spaceId}`, { name: 'Other' }],
        // Members and the administration are a layer on top that a single-user server does not have.
        ['GET', `/api/spaces/${spaceId}/members`],
        ['POST', `/api/spaces/${spaceId}/members`, { username: 'ab', role: 'viewer' }],
        ['PATCH', `/api/spaces/${spaceId}/members/local`, { role: 'viewer' }],
        ['DELETE', `/api/spaces/${spaceId}/members/local`],
        ['GET', '/api/admin/spaces'],
        ['GET', '/api/admin/status'],
        ['GET', `/api/admin/spaces/${spaceId}`],
        ['POST', `/api/admin/spaces/${spaceId}/size`],
        ['POST', `/api/admin/spaces/${spaceId}/transfer`, { userId: 'local' }],
        ['POST', `/api/admin/spaces/${spaceId}/disable`],
        ['POST', `/api/admin/spaces/${spaceId}/enable`],
        ['DELETE', `/api/admin/spaces/${spaceId}`],
      ]
      for (const [method, path, body] of probes) {
        const r = await call(admin, method, path, body ?? (method === 'GET' ? undefined : {}))
        expect([r.status, r.json?.error], `${method} ${path}`).toEqual([404, 'not_found'])
      }
    })

    it('the remote repository endpoints answer 404', async () => {
      for (const [method, path] of [
        ['GET', `/api/spaces/${spaceId}/remote`],
        ['POST', `/api/spaces/${spaceId}/remote/key`],
        ['POST', `/api/spaces/${spaceId}/remote/sync`],
      ] as const) {
        const r = await call(admin, method, path, method === 'GET' ? undefined : {})
        expect([r.status, r.json?.error], `${method} ${path}`).toEqual([404, 'not_found'])
      }
    })

    it('there is only the one space, and others are 404', async () => {
      const r = await call(admin, 'GET', `/api/spaces/${'01ZZZZZZZZ'}/pages`)
      expect([r.status, r.json?.error]).toEqual([404, 'not_found'])
    })
  })

  describe.skipIf(!accounts)(
    'accounts server only (skipped on a single-user server, where these answer 404): signing in',
    () => {
      const uniq = (p: string) => `${p}${run}`.slice(0, 30)
      // A member made through an invite, signed in in its own browser.
      const newMember = async (name: string, password = 'a-long-password') => {
        const invite = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
        const b = browser()
        const r = await call(b, 'POST', '/api/invites/accept', {
          token: invite.token,
          username: name,
          name: `名 ${name}`,
          password,
        })
        expect(r.status).toBe(201)
        created.users.push(r.json.user.id)
        return { browser: b, user: r.json.user as User, password }
      }

      it('without a session everything but meta, session and the sign-in endpoints is 401 unauthenticated', async () => {
        const b = browser()
        for (const [method, path] of [
          ['GET', '/api/spaces'],
          ['GET', `${SP_PAGES}`],
          ['GET', `${SP_PAGES}/01ZZZZZZZZ`],
          ['GET', `/api/spaces/${spaceId}/tree`],
          ['GET', '/api/account'],
          ['GET', '/api/users'],
          ['GET', '/api/invites'],
        ]) {
          const r = await call(b, method, path)
          expect([r.status, r.json?.error], `${method} ${path}`).toEqual([401, 'unauthenticated'])
        }
        const meta = await call(b, 'GET', '/api/meta')
        expect(meta.status).toBe(200)
        expect(meta.json.spaces).toEqual([])
        const session = await call(b, 'GET', '/api/session')
        expect(session.status).toBe(200)
        expect(session.json.user).toBeNull()
        expect(session.json.setup).toBe(false)
      })

      it('login: wrong credentials are 401 with the same message whether or not the user exists', async () => {
        const m = await newMember(uniq('lg'))
        const b = browser()
        const unknown = await call(b, 'POST', '/api/login', { username: uniq('nobody'), password: 'whatever-it-is' })
        const wrong = await call(b, 'POST', '/api/login', { username: m.user.username, password: 'not-the-password' })
        expect([unknown.status, unknown.json.error]).toEqual([401, 'unauthenticated'])
        expect([wrong.status, wrong.json.error]).toEqual([401, 'unauthenticated'])
        expect(wrong.json.message).toBe(unknown.json.message)
        expect(wrong.headers.get('Set-Cookie')).toBeNull()
      })

      it('login with the right password: 200 with the user and a session cookie (HttpOnly, SameSite=Lax); upper case in the username is folded', async () => {
        const m = await newMember(uniq('up'))
        const b = browser()
        const r = await call(b, 'POST', '/api/login', { username: m.user.username.toUpperCase(), password: m.password })
        expect(r.status).toBe(200)
        expect(r.json.user).toMatchObject({ id: m.user.id, username: m.user.username, role: 'member', disabled: false })
        const cookie = r.res.headers.getSetCookie().join('\n')
        expect(cookie).toMatch(/HttpOnly/i)
        expect(cookie).toMatch(/SameSite=Lax/i)
        expect((await call(b, 'GET', '/api/session')).json.user.id).toBe(m.user.id)
      })

      it('logout is 204 and ends the session; with no session it is 204 as well', async () => {
        const m = await newMember(uniq('lo'))
        expect((await call(m.browser, 'POST', '/api/logout')).status).toBe(204)
        expect((await call(m.browser, 'GET', '/api/session')).json.user).toBeNull()
        expect((await call(m.browser, 'GET', '/api/spaces')).status).toBe(401)
        expect((await call(browser(), 'POST', '/api/logout')).status).toBe(204)
      })

      it('first-run setup is closed once there is a user: 409 exists, whatever the token', async () => {
        const r = await call(browser(), 'POST', '/api/setup', {
          token: 'not-the-token',
          username: uniq('late'),
          password: 'a-long-password',
        })
        expect([r.status, r.json.error]).toEqual([409, 'exists'])
      })

      it('every request that changes something must say it is JSON, here too', async () => {
        const r = await call(admin, 'POST', '/api/logout', '', { 'Content-Type': 'text/plain' })
        expect([400, 415]).toContain(r.status)
      })

      describe('account', () => {
        it('GET shows the user, whether there is a password, and the linked sign-ins', async () => {
          const m = await newMember(uniq('ac'))
          const r = await call(m.browser, 'GET', '/api/account')
          expect(r.status).toBe(200)
          expect(r.json.user.id).toBe(m.user.id)
          expect(r.json.hasPassword).toBe(true)
          expect(r.json.identities).toEqual([])
        })

        it('PATCH changes the name and the email, each optional', async () => {
          const m = await newMember(uniq('pa'))
          const a = await call(m.browser, 'PATCH', '/api/account', { name: '新名字' })
          expect(a.status).toBe(200)
          expect(a.json.user).toMatchObject({ name: '新名字', email: '' })
          const b = await call(m.browser, 'PATCH', '/api/account', { email: 'x@example.com' })
          expect(b.json.user).toMatchObject({ name: '新名字', email: 'x@example.com' })
          expect((await call(m.browser, 'GET', '/api/account')).json.user.email).toBe('x@example.com')
        })

        it('changing the password: wrong current is 403 forbidden, a short new one 400, the right one 204 and ends the other sessions', async () => {
          const m = await newMember(uniq('pw'))
          const other = browser()
          expect(
            (await call(other, 'POST', '/api/login', { username: m.user.username, password: m.password })).status,
          ).toBe(200)
          const wrong = await call(m.browser, 'POST', '/api/account/password', {
            current: 'not-the-one',
            new: 'another-password',
          })
          expect([wrong.status, wrong.json.error]).toEqual([403, 'forbidden'])
          const short = await call(m.browser, 'POST', '/api/account/password', { current: m.password, new: 'short' })
          expect([short.status, short.json.error]).toEqual([400, 'invalid'])
          const ok = await call(m.browser, 'POST', '/api/account/password', {
            current: m.password,
            new: 'another-password',
          })
          expect(ok.status).toBe(204)
          expect((await call(m.browser, 'GET', '/api/account')).status).toBe(200)
          expect((await call(other, 'GET', '/api/account')).status).toBe(401)
          const fresh = browser()
          expect(
            (await call(fresh, 'POST', '/api/login', { username: m.user.username, password: m.password })).status,
          ).toBe(401)
          expect(
            (await call(fresh, 'POST', '/api/login', { username: m.user.username, password: 'another-password' }))
              .status,
          ).toBe(200)
        })
      })

      describe('OAuth consent and grants', () => {
        // A real server needs an MCP client to start a request, and --public-url: the mock plays both.
        const start = (username: string, o?: Parameters<MockServer['startAuthorization']>[1]) =>
          server!.startAuthorization(username, o)

        it.skipIf(!!REAL)(
          'mock only: a person approves a request with the spaces they pick, and the grant is theirs to see and to revoke',
          async () => {
            const m = await newMember(uniq('oa1'))
            const mine = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0]
            const req = start(m.user.username, { clientName: 'Notes helper' })
            const info = await call(m.browser, 'GET', `/api/oauth/consent?req=${req}`)
            expect(info.status).toBe(200)
            expect(info.json).toMatchObject({
              client: { name: 'Notes helper', id: 'pbc_mock' },
              redirectHost: 'localhost:7777',
              punycode: false,
              scopes: ['read', 'write'],
              user: { id: m.user.id },
            })
            expect(info.json.spaces.find((s: { id: string }) => s.id === mine.id)).toMatchObject({ personal: true })
            const done = await call(m.browser, 'POST', '/api/oauth/consent', {
              req,
              approve: true,
              spaces: [mine.id],
              access: 'write',
            })
            expect(done.status).toBe(200)
            const back = new URL(done.json.redirect)
            expect([back.origin + back.pathname, back.searchParams.get('state')]).toEqual([
              'http://localhost:7777/callback',
              'st4te',
            ])
            expect(back.searchParams.get('code')).toBeTruthy()
            expect(back.searchParams.get('iss')).toBeTruthy()
            // Decided once.
            expect(
              (await call(m.browser, 'POST', '/api/oauth/consent', { req, approve: true, spaces: [mine.id] })).json
                .error,
            ).toBe('not_found')
            const list = (await call(m.browser, 'GET', '/api/account/grants')).json.grants
            expect(list).toHaveLength(1)
            expect(list[0]).toMatchObject({
              client: { name: 'Notes helper', id: 'pbc_mock' },
              access: 'write',
              spaces: [{ id: mine.id, name: mine.name }],
            })
            expect(typeof list[0].created).toBe('string')
            const other = await newMember(uniq('oa2'))
            expect((await call(other.browser, 'GET', '/api/account/grants')).json.grants).toEqual([])
            expect((await call(other.browser, 'DELETE', `/api/account/grants/${list[0].id}`)).json.error).toBe(
              'not_found',
            )
            expect((await call(m.browser, 'DELETE', `/api/account/grants/${list[0].id}`)).status).toBe(204)
            expect((await call(m.browser, 'GET', '/api/account/grants')).json.grants).toEqual([])
          },
        )

        it.skipIf(!!REAL)(
          "mock only: a request is the one person's, and a bad answer is 400 and keeps it",
          async () => {
            const m = await newMember(uniq('oa3'))
            const other = await newMember(uniq('oa4'))
            const mine = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0].id
            const theirs = (await call(other.browser, 'GET', '/api/spaces')).json.spaces[0].id
            const req = start(m.user.username, { scopes: ['read'] })
            expect((await call(other.browser, 'GET', `/api/oauth/consent?req=${req}`)).json.error).toBe('not_found')
            expect(
              (await call(other.browser, 'POST', '/api/oauth/consent', { req, approve: true, spaces: [theirs] })).json
                .error,
            ).toBe('not_found')
            for (const bad of [
              { spaces: [] },
              { spaces: [theirs] },
              { spaces: [mine], access: 'write' },
              { spaces: [mine], access: 'root' },
            ]) {
              const r = await call(m.browser, 'POST', '/api/oauth/consent', { req, approve: true, ...bad })
              expect([r.status, r.json.error], JSON.stringify(bad)).toEqual([400, 'invalid'])
            }
            expect((await call(m.browser, 'GET', `/api/oauth/consent?req=${req}`)).status).toBe(200)
            const denied = await call(m.browser, 'POST', '/api/oauth/consent', { req, approve: false })
            expect(denied.status).toBe(200)
            expect(new URL(denied.json.redirect).searchParams.get('error')).toBe('access_denied')
            expect((await call(m.browser, 'GET', '/api/account/grants')).json.grants).toEqual([])
          },
        )

        it.skipIf(!!REAL)(
          "mock only: an administrator lists everyone's grants a page at a time and revokes one; others are refused",
          async () => {
            const m = await newMember(uniq('oa5'))
            const mine = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0].id
            for (let i = 0; i < 3; i++) {
              const req = start(m.user.username, { clientName: `app ${i}` })
              await call(m.browser, 'POST', '/api/oauth/consent', { req, approve: true, spaces: [mine] })
              clock += 1000
            }
            const first = await call(admin, 'GET', `/api/admin/grants?user=${m.user.id}&limit=2`)
            expect(first.status).toBe(200)
            expect(first.json.items).toHaveLength(2)
            expect(first.json.items[0].owner).toMatchObject({ id: m.user.id, username: m.user.username })
            expect(first.json.next).not.toBe('')
            const rest = await call(
              admin,
              'GET',
              `/api/admin/grants?user=${m.user.id}&limit=2&cursor=${first.json.next}`,
            )
            expect(rest.json.items).toHaveLength(1)
            expect(rest.json.next).toBe('')
            expect((await call(m.browser, 'GET', '/api/admin/grants')).json.error).toBe('forbidden')
            expect((await call(admin, 'GET', '/api/admin/grants?limit=0')).status).toBe(400)
            expect((await call(admin, 'DELETE', `/api/admin/grants/${first.json.items[0].id}`)).status).toBe(204)
            expect((await call(admin, 'DELETE', `/api/admin/grants/${first.json.items[0].id}`)).status).toBe(404)
            expect((await call(m.browser, 'GET', '/api/account/grants')).json.grants).toHaveLength(2)
          },
        )

        it.skipIf(!!REAL)(
          'mock only: with MCP off the consent endpoints are 404, the grants are kept and listed, and oauth reads false',
          async () => {
            const m = await newMember(uniq('oa6'))
            const mine = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0].id
            await call(m.browser, 'POST', '/api/oauth/consent', {
              req: start(m.user.username),
              approve: true,
              spaces: [mine],
            })
            const before = (await call(admin, 'GET', '/api/admin/access')).json
            expect(before).toMatchObject({ mcp: true, oauth: true })
            try {
              await call(admin, 'PUT', '/api/admin/access', { mcp: false })
              expect((await call(admin, 'GET', '/api/admin/access')).json).toMatchObject({ mcp: false, oauth: false })
              expect((await call(m.browser, 'GET', '/api/oauth/consent?req=x')).json.error).toBe('not_found')
              expect((await call(m.browser, 'POST', '/api/oauth/consent', { req: 'x', approve: false })).status).toBe(
                404,
              )
              expect((await call(m.browser, 'GET', '/api/account/grants')).json.grants).toHaveLength(1)
            } finally {
              await call(admin, 'PUT', '/api/admin/access', { mcp: before.mcp })
            }
          },
        )
      })

      describe('API tokens', () => {
        it('a person makes one, sees it without its secret, and revokes it', async () => {
          const m = await newMember(uniq('apk1'))
          const mine = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0]
          const made = await call(m.browser, 'POST', '/api/account/tokens', {
            name: '  Desktop notes ',
            access: 'write',
            spaces: [mine.id],
          })
          expect(made.status).toBe(201)
          expect(made.json.secret).toMatch(/^pbk_[0-9A-Za-z]{43}$/)
          expect(made.json.token).toMatchObject({
            name: 'Desktop notes',
            access: 'write',
            prefix: made.json.secret.slice(0, 12),
            state: 'active',
            lastUsed: null,
            revoked: null,
          })
          expect(made.json.token.spaces).toEqual([{ id: mine.id, name: mine.name }])
          expect(typeof made.json.token.expires).toBe('string')
          const list = await call(m.browser, 'GET', '/api/account/tokens')
          expect(list.status).toBe(200)
          expect(list.json.tokens.map((t: { id: string }) => t.id)).toEqual([made.json.token.id])
          expect(JSON.stringify(list.json)).not.toContain(made.json.secret)
          expect((await call(m.browser, 'DELETE', `/api/account/tokens/${made.json.token.id}`)).status).toBe(204)
          expect((await call(m.browser, 'DELETE', `/api/account/tokens/${made.json.token.id}`)).status).toBe(204)
          expect((await call(m.browser, 'GET', '/api/account/tokens')).json.tokens[0]).toMatchObject({
            state: 'revoked',
          })
        })

        it('read is the default, 0 days is never, and a bad request is 400 or 404', async () => {
          const m = await newMember(uniq('apk2'))
          const mine = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0].id
          const forever = await call(m.browser, 'POST', '/api/account/tokens', {
            name: 'forever',
            spaces: [mine],
            expiresInDays: 0,
          })
          expect(forever.status).toBe(201)
          expect(forever.json.token).toMatchObject({ access: 'read', expires: null })
          for (const bad of [
            { name: ' ', spaces: [mine] },
            { name: 'n', spaces: [] },
            { name: 'n', spaces: [mine], access: 'admin' },
            { name: 'n', spaces: [mine], expiresInDays: 366 },
          ]) {
            const r = await call(m.browser, 'POST', '/api/account/tokens', bad)
            expect([r.status, r.json.error], JSON.stringify(bad)).toEqual([400, 'invalid'])
          }
          const elsewhere = await call(m.browser, 'POST', '/api/account/tokens', { name: 'n', spaces: ['01JD3K8Q7Z'] })
          expect([elsewhere.status, elsewhere.json.error]).toEqual([404, 'not_found'])
        })

        it("someone else's token is not theirs to revoke or to see", async () => {
          const a = await newMember(uniq('apk3'))
          const b = await newMember(uniq('apk4'))
          const space = (await call(a.browser, 'GET', '/api/spaces')).json.spaces[0].id
          const t = (await call(a.browser, 'POST', '/api/account/tokens', { name: 'mine', spaces: [space] })).json.token
            .id
          expect((await call(b.browser, 'GET', '/api/account/tokens')).json.tokens).toEqual([])
          const r = await call(b.browser, 'DELETE', `/api/account/tokens/${t}`)
          expect([r.status, r.json.error]).toEqual([404, 'not_found'])
        })

        it("an administrator lists everyone's tokens and revokes one; others are refused", async () => {
          const m = await newMember(uniq('apk5'))
          const space = (await call(m.browser, 'GET', '/api/spaces')).json.spaces[0].id
          const t = (await call(m.browser, 'POST', '/api/account/tokens', { name: 'theirs', spaces: [space] })).json
            .token.id
          const all = await call(admin, 'GET', `/api/admin/tokens?user=${m.user.id}`)
          expect(all.status).toBe(200)
          expect(all.json.items.map((i: { id: string }) => i.id)).toEqual([t])
          expect(all.json.items[0].owner).toMatchObject({ id: m.user.id, username: m.user.username })
          expect(all.json.next).toBe('')
          expect((await call(m.browser, 'GET', '/api/admin/tokens')).json.error).toBe('forbidden')
          expect((await call(m.browser, 'GET', '/api/admin/access')).json.error).toBe('forbidden')
          expect((await call(admin, 'DELETE', `/api/admin/tokens/${t}`)).status).toBe(204)
          expect((await call(m.browser, 'GET', '/api/account/tokens')).json.tokens[0].state).toBe('revoked')
          expect((await call(admin, 'DELETE', '/api/admin/tokens/NOSUCHTOKN')).status).toBe(404)
          expect((await call(admin, 'GET', '/api/admin/tokens?limit=0')).status).toBe(400)
        })

        it('the machine access switches are on, may be changed, and are put back', async () => {
          const before = await call(admin, 'GET', '/api/admin/access')
          expect(before.status).toBe(200)
          expect(Object.keys(before.json).sort()).toEqual(['mcp', 'oauth', 'tokens'])
          try {
            const off = await call(admin, 'PUT', '/api/admin/access', { tokens: false })
            expect(off.json).toMatchObject({ tokens: false, mcp: before.json.mcp })
            expect((await call(admin, 'GET', '/api/admin/access')).json.tokens).toBe(false)
          } finally {
            await call(admin, 'PUT', '/api/admin/access', { tokens: before.json.tokens, mcp: before.json.mcp })
          }
          expect((await call(admin, 'GET', '/api/admin/access')).json).toEqual(before.json)
        })
      })

      describe('users', () => {
        it('finds users by part of a username or name, ignoring case, and caps the count; no parameters is everyone', async () => {
          const m = await newMember(uniq('fu'))
          const all = (await as(admin, () => adminApi.users())).users
          const upper = m.user.username.toUpperCase()
          const hit = (await as(admin, () => adminApi.users({ q: upper }))).users
          expect(hit.map((u) => u.id)).toContain(m.user.id)
          for (const u of hit) expect((u.username + '\n' + u.name).toLowerCase()).toContain(m.user.username)
          expect((await as(admin, () => adminApi.users({ q: m.user.name }))).users.map((u) => u.id)).toContain(
            m.user.id,
          )
          expect((await as(admin, () => adminApi.users({ q: 'zzz-no-such-user-zzz' }))).users).toEqual([])
          const one = (await as(admin, () => adminApi.users({ limit: 1 }))).users
          expect(one.map((u) => u.id)).toEqual([all[0].id])
          for (const bad of ['0', '201', 'x']) {
            const r = await call(admin, 'GET', `/api/users?limit=${bad}`)
            expect([r.status, r.json.error], bad).toEqual([400, 'invalid'])
          }
        })

        it('pages the list by cursor in each sort, filters by role and state, and refuses what it does not have', async () => {
          const names = [uniq('pgx'), uniq('pgy'), uniq('pgz')]
          const made: Awaited<ReturnType<typeof newMember>>[] = []
          for (const n of names) made.push(await newMember(n))
          await as(admin, () => adminApi.updateUser(made[1].user.id, { disabled: true }))
          for (const sort of ['created', 'name', 'lastActive'] as const) {
            for (const order of ['asc', 'desc'] as const) {
              const whole = (await as(admin, () => adminApi.users({ sort, order, limit: 200 }))).users.map((u) => u.id)
              const walked: string[] = []
              let cursor: string | undefined
              for (let i = 0; i < 400; i++) {
                const page = await as(admin, () => adminApi.users({ sort, order, limit: 2, cursor }))
                expect(page.users.length).toBeLessThanOrEqual(2)
                walked.push(...page.users.map((u) => u.id))
                if (!page.next) break
                cursor = page.next
              }
              // The page of 200 is the whole list as long as there are fewer than that.
              if (whole.length < 200) expect(walked, `${sort} ${order}`).toEqual(whole)
              expect(new Set(walked).size).toBe(walked.length)
            }
          }
          const disabled = (await as(admin, () => adminApi.users({ state: 'disabled', limit: 200 }))).users
          expect(disabled.map((u) => u.id)).toContain(made[1].user.id)
          expect(disabled.every((u) => u.disabled)).toBe(true)
          const enabled = (await as(admin, () => adminApi.users({ state: 'enabled', limit: 200 }))).users
          expect(enabled.map((u) => u.id)).not.toContain(made[1].user.id)
          const members = (await as(admin, () => adminApi.users({ role: 'member', limit: 200 }))).users
          expect(members.every((u) => u.role === 'member')).toBe(true)
          const admins = (await as(admin, () => adminApi.users({ role: 'admin', limit: 200 }))).users
          expect(admins.every((u) => u.role === 'admin')).toBe(true)
          for (const bad of [
            'sort=age',
            'order=up',
            'role=king',
            'state=gone',
            'inactiveDays=x',
            'activeDays=-1',
            'cursor=junk',
          ]) {
            const r = await call(admin, 'GET', `/api/users?${bad}`)
            expect([r.status, r.json.error], bad).toEqual([400, 'invalid'])
          }
          // A cursor of one sort is not one of another.
          const first = await as(admin, () => adminApi.users({ limit: 1, sort: 'created' }))
          if (first.next) {
            const r = await call(admin, 'GET', `/api/users?sort=name&cursor=${encodeURIComponent(first.next)}`)
            expect([r.status, r.json.error]).toEqual([400, 'invalid'])
          }
        })

        it('has one person with their counts and memberships, and a member may not read either', async () => {
          const m = await newMember(uniq('on'))
          const one = await as(admin, () => adminApi.user(m.user.id))
          expect(one.user).toMatchObject({ id: m.user.id, username: m.user.username, passkeys: 0, sso: 0 })
          const mem = await as(admin, () => adminApi.userMemberships(m.user.id))
          expect(mem.spaces.some((x) => x.personal)).toBe(true)
          expect(Array.isArray(mem.groups)).toBe(true)
          expect((await call(admin, 'GET', '/api/users/NOSUCHUSER')).json.error).toBe('not_found')
          for (const path of [`/api/users/${m.user.id}`, `/api/users/${m.user.id}/memberships`]) {
            const r = await call(m.browser, 'GET', path)
            expect([r.status, r.json.error], path).toEqual([403, 'forbidden'])
          }
        })

        it("lists a person's sessions and connected accounts for a site administrator, and ends them as signed out elsewhere", async () => {
          const m = await newMember(uniq('se'))
          const second = browser()
          expect(
            (await call(second, 'POST', '/api/login', { username: m.user.username, password: m.password })).status,
          ).toBe(200)
          const { sessions } = await as(admin, () => adminApi.userSessions(m.user.id))
          expect(sessions.length).toBeGreaterThanOrEqual(2)
          for (const s of sessions) {
            expect(s.id).toMatch(/^[0-9a-f]{64}$|.+/)
            expect(typeof s.method).toBe('string')
            expect(Date.parse(s.lastUsed)).not.toBeNaN()
          }
          expect(sessions.some((s) => s.method === 'password')).toBe(true)
          // End one: the browser that held it is told it signed out elsewhere; the other still works.
          const gone = sessions.find((s) => s.method === 'password')!
          await as(admin, () => adminApi.endUserSession(m.user.id, gone.id))
          const left = (await as(admin, () => adminApi.userSessions(m.user.id))).sessions
          expect(left.map((s) => s.id)).not.toContain(gone.id)
          const missing = await call(admin, 'DELETE', `/api/users/${m.user.id}/sessions/${gone.id}`)
          expect([missing.status, missing.json.error]).toEqual([404, 'not_found'])
          const answers = await Promise.all([
            call(m.browser, 'GET', '/api/account'),
            call(second, 'GET', '/api/account'),
          ])
          expect(answers.filter((r) => r.status === 401 && r.json.reason === 'signed_out_elsewhere')).toHaveLength(1)
          // All of them.
          const ended = await as(admin, () => adminApi.endUserSessions(m.user.id))
          expect(ended.ended).toBe(left.length)
          expect((await as(admin, () => adminApi.userSessions(m.user.id))).sessions).toEqual([])
          for (const b of [m.browser, second]) {
            const r = await call(b, 'GET', '/api/account')
            expect([r.status, r.json.reason]).toEqual([401, 'signed_out_elsewhere'])
          }
          // No connected account to list, and no such one to disconnect.
          expect((await as(admin, () => adminApi.userIdentities(m.user.id))).identities).toEqual([])
          const none = await call(admin, 'DELETE', `/api/users/${m.user.id}/identities/NOSUCHIDEN`)
          expect([none.status, none.json.error]).toEqual([404, 'not_found'])
          // A member of any kind is refused, and a person who does not exist is none.
          const again = browser()
          await call(again, 'POST', '/api/login', { username: m.user.username, password: m.password })
          for (const [method, path] of [
            ['GET', `/api/users/${m.user.id}/sessions`],
            ['DELETE', `/api/users/${m.user.id}/sessions`],
            ['GET', `/api/users/${m.user.id}/identities`],
          ]) {
            const r = await call(again, method, path)
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([403, 'forbidden'])
          }
          expect((await call(admin, 'GET', '/api/users/NOSUCHUSER/sessions')).json.error).toBe('not_found')
        })

        it('an administrator lists users, ordered by created; a member gets 403 forbidden for users and invites', async () => {
          const m = await newMember(uniq('us'))
          const { users } = await as(admin, () => adminApi.users())
          expect(users.find((u) => u.id === m.user.id)).toMatchObject({
            username: m.user.username,
            role: 'member',
            disabled: false,
          })
          for (let i = 1; i < users.length; i++) expect(users[i - 1].created <= users[i].created).toBe(true)
          for (const [method, path] of [
            ['GET', '/api/users'],
            ['PATCH', `/api/users/${m.user.id}`],
            ['GET', '/api/invites'],
            ['POST', '/api/invites'],
          ]) {
            const r = await call(m.browser, method, path, method === 'GET' ? undefined : {})
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([403, 'forbidden'])
          }
        })

        it('changes a role and disables and enables a user; disabling ends every session and blocks signing in', async () => {
          const m = await newMember(uniq('ro'))
          const promoted = await as(admin, () => adminApi.updateUser(m.user.id, { role: 'admin' }))
          expect(promoted.user.role).toBe('admin')
          expect((await call(m.browser, 'GET', '/api/users')).status).toBe(200)
          await as(admin, () => adminApi.updateUser(m.user.id, { role: 'member' }))
          expect((await call(m.browser, 'GET', '/api/users')).status).toBe(403)

          const off = await as(admin, () => adminApi.updateUser(m.user.id, { disabled: true }))
          expect(off.user.disabled).toBe(true)
          expect((await call(m.browser, 'GET', '/api/account')).status).toBe(401)
          const again = await call(browser(), 'POST', '/api/login', { username: m.user.username, password: m.password })
          expect([again.status, again.json.error]).toEqual([401, 'unauthenticated'])
          await as(admin, () => adminApi.updateUser(m.user.id, { disabled: false }))
          expect(
            (await call(browser(), 'POST', '/api/login', { username: m.user.username, password: m.password })).status,
          ).toBe(200)
        })

        it('an administrator cannot disable themself (409), nor leave the server with no enabled administrator (409)', async () => {
          const me = (await call(admin, 'GET', '/api/session')).json.user as User
          const self = await call(admin, 'PATCH', `/api/users/${me.id}`, { disabled: true })
          expect([self.status, self.json.error]).toEqual([409, 'conflict'])
          const { users } = await as(admin, () => adminApi.users())
          if (users.filter((u) => u.role === 'admin' && !u.disabled).length === 1) {
            const demote = await call(admin, 'PATCH', `/api/users/${me.id}`, { role: 'member' })
            expect([demote.status, demote.json.error]).toEqual([409, 'conflict'])
          }
          expect((await call(admin, 'GET', '/api/session')).json.user).toMatchObject({
            id: me.id,
            role: 'admin',
            disabled: false,
          })
        })

        it('an unknown user is 404', async () => {
          const r = await call(admin, 'PATCH', '/api/users/01ZZZZZZZZ', { disabled: true })
          expect([r.status, r.json.error]).toEqual([404, 'not_found'])
        })
      })

      describe('invites', () => {
        it('create: 201 with the token once; the list shows the open ones without it', async () => {
          const inv = await as(admin, () => adminApi.createInvite({ role: 'member', days: 3 }))
          expect(inv.token.length).toBeGreaterThanOrEqual(16)
          expect(inv).toMatchObject({ role: 'member' })
          expect(Date.parse(inv.expires)).toBeGreaterThan(Date.parse(inv.created))
          const { invites } = await as(admin, () => adminApi.invites())
          const row = invites.find((i) => i.id === inv.id)!
          expect(row).toMatchObject({ role: 'member', expires: inv.expires })
          expect(typeof row.createdBy).toBe('string')
          expect(JSON.stringify(invites)).not.toContain(inv.token)
        })

        it('days outside 1 to 30 and an unknown role are 400', async () => {
          // (`days: 0` is not tried: a server may read it as "not given".)
          for (const body of [{ days: -1 }, { days: 31 }, { days: 1.5 }, { role: 'owner' }]) {
            const r = await call(admin, 'POST', '/api/invites', body)
            expect([r.status, r.json.error], JSON.stringify(body)).toEqual([400, 'invalid'])
          }
        })

        it('check is public: 200 with role and expiry for a good token, 404 for anything else', async () => {
          const inv = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
          const anon = browser()
          const ok = await call(anon, 'POST', '/api/invites/check', { token: inv.token })
          expect(ok.status).toBe(200)
          expect(ok.json).toEqual({ role: 'member', expires: inv.expires, username: '', name: '', email: '' })
          const bad = await call(anon, 'POST', '/api/invites/check', { token: 'not-a-token' })
          expect([bad.status, bad.json.error]).toEqual([404, 'not_found'])
        })

        it('accept is public: 201 with the user and a session, a personal space of their own; the invite works once', async () => {
          const inv = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
          const name = uniq('ac2')
          const b = browser()
          const r = await call(b, 'POST', '/api/invites/accept', {
            token: inv.token,
            username: name.toUpperCase(),
            name: '新人',
            password: 'a-long-password',
          })
          expect(r.status).toBe(201)
          created.users.push(r.json.user.id)
          expect(r.json.user).toMatchObject({ username: name, name: '新人', role: 'member', disabled: false })
          expect(r.res.headers.getSetCookie().length).toBeGreaterThan(0)
          const { spaces } = (await call(b, 'GET', '/api/spaces')).json as { spaces: SpaceInfo[] }
          expect(spaces[0]).toMatchObject({ personal: true, role: 'owner' })
          expect(spaces.map((s) => s.id)).not.toContain(spaceId)
          // Used: gone from the list, and a 404 to check. (Accepting it again is not
          // tried: a real server counts every bad token against the client address.)
          expect((await call(browser(), 'POST', '/api/invites/check', { token: inv.token })).status).toBe(404)
          expect((await as(admin, () => adminApi.invites())).invites.map((i) => i.id)).not.toContain(inv.id)
        })

        it('a taken username is 409 exists and the invite stays usable; a short password is 400', async () => {
          const m = await newMember(uniq('tk'))
          const inv = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
          const taken = await call(browser(), 'POST', '/api/invites/accept', {
            token: inv.token,
            username: m.user.username,
            password: 'a-long-password',
          })
          expect([taken.status, taken.json.error]).toEqual([409, 'exists'])
          const short = await call(browser(), 'POST', '/api/invites/accept', {
            token: inv.token,
            username: uniq('sh'),
            password: 'short',
          })
          expect([short.status, short.json.error]).toEqual([400, 'invalid'])
          expect((await call(browser(), 'POST', '/api/invites/check', { token: inv.token })).status).toBe(200)
        })

        it('revoke is 204 and the token stops working', async () => {
          const inv = await as(admin, () => adminApi.createInvite({}))
          await as(admin, () => adminApi.revokeInvite(inv.id))
          expect((await call(browser(), 'POST', '/api/invites/check', { token: inv.token })).status).toBe(404)
          expect((await as(admin, () => adminApi.invites())).invites.map((i) => i.id)).not.toContain(inv.id)
        })
      })

      describe('spaces', () => {
        it('creates a space (201, the caller owns it), renames it, and lists personal first, then by name', async () => {
          const m = await newMember(uniq('sp'))
          const made = await call(m.browser, 'POST', '/api/spaces', { name: 'Beta' })
          expect(made.status).toBe(201)
          expect(made.json).toMatchObject({ name: 'Beta', role: 'owner', personal: false })
          const renamed = await call(m.browser, 'PATCH', `/api/spaces/${made.json.id}`, { name: 'Gamma' })
          expect(renamed.status).toBe(200)
          expect(renamed.json).toMatchObject({ id: made.json.id, name: 'Gamma' })
          await call(m.browser, 'POST', '/api/spaces', { name: 'Alpha' })
          const { spaces } = (await call(m.browser, 'GET', '/api/spaces')).json as { spaces: SpaceInfo[] }
          expect(spaces[0].personal).toBe(true)
          expect(spaces.slice(1).map((s) => s.name)).toEqual(['Alpha', 'Gamma'])
          expect((await call(m.browser, 'GET', '/api/meta')).json.spaces.map((s: SpaceInfo) => s.id)).toEqual(
            spaces.map((s) => s.id),
          )
          // The new space is a working space.
          const page = await call(m.browser, 'POST', `/api/spaces/${made.json.id}/pages`, {
            meta: { title: '新空间里的页面' },
            body: 'x\n',
          })
          expect(page.status).toBe(201)
        })

        it('a name of 0 or more than 80 characters is 400', async () => {
          for (const name of ['', '   ', '字'.repeat(81)]) {
            const r = await call(admin, 'POST', '/api/spaces', { name })
            expect([r.status, r.json.error], name.length.toString()).toEqual([400, 'invalid'])
          }
          expect((await call(admin, 'POST', '/api/spaces', { name: '字'.repeat(80) })).status).toBe(201)
        })

        it('inbox: Inbox unless given; set on creation, changed with PATCH alone or with the name; a PATCH with neither is 400', async () => {
          const m = await newMember(uniq('ib1'))
          const plain = await call(m.browser, 'POST', '/api/spaces', { name: 'Plain' })
          expect(plain.status).toBe(201)
          expect(plain.json.inbox).toBe('Inbox')
          const own = await call(m.browser, 'POST', '/api/spaces', { name: 'Own', inbox: '收件箱' })
          expect(own.status).toBe(201)
          expect(own.json.inbox).toBe('收件箱')
          const path = `/api/spaces/${own.json.id}`
          const moved = await call(m.browser, 'PATCH', path, { inbox: '随记/待整理' })
          expect(moved.status).toBe(200)
          expect(moved.json).toMatchObject({ id: own.json.id, name: 'Own', inbox: '随记/待整理' })
          const renamed = await call(m.browser, 'PATCH', path, { name: 'Renamed' })
          expect(renamed.json).toMatchObject({ name: 'Renamed', inbox: '随记/待整理' })
          const both = await call(m.browser, 'PATCH', path, { name: 'Both', inbox: 'Notes' })
          expect(both.json).toMatchObject({ name: 'Both', inbox: 'Notes' })
          const none = await call(m.browser, 'PATCH', path, {})
          expect([none.status, none.json.error]).toEqual([400, 'invalid'])
          const { spaces } = (await call(m.browser, 'GET', '/api/spaces')).json as { spaces: SpaceInfo[] }
          expect(spaces.find((s) => s.id === own.json.id)).toMatchObject({ name: 'Both', inbox: 'Notes' })
          expect(spaces.find((s) => s.id === plain.json.id)!.inbox).toBe('Inbox')
        })

        it('an invalid inbox path is 400 on create and on PATCH, and the space is left as it was', async () => {
          const m = await newMember(uniq('ib2'))
          for (const inbox of ['.hidden', 'assets']) {
            const r = await call(m.browser, 'POST', '/api/spaces', { name: 'Bad', inbox })
            expect([r.status, r.json.error], inbox).toEqual([400, 'invalid'])
          }
          const made = await call(m.browser, 'POST', '/api/spaces', { name: 'Good', inbox: 'Keep' })
          for (const inbox of ['.hidden', 'assets']) {
            const r = await call(m.browser, 'PATCH', `/api/spaces/${made.json.id}`, { inbox })
            expect([r.status, r.json.error], inbox).toEqual([400, 'invalid'])
          }
          // A bad inbox does not let the name through either.
          const mixed = await call(m.browser, 'PATCH', `/api/spaces/${made.json.id}`, {
            name: 'Changed',
            inbox: '.hidden',
          })
          expect(mixed.status).toBe(400)
          const { spaces } = (await call(m.browser, 'GET', '/api/spaces')).json as { spaces: SpaceInfo[] }
          expect(spaces.find((s) => s.id === made.json.id)).toMatchObject({ name: 'Good', inbox: 'Keep' })
        })

        it('the inbox is only a name: no folder until a page is made in it, and changing it moves nothing', async () => {
          const m = await newMember(uniq('ib3'))
          const sp = await call(m.browser, 'POST', '/api/spaces', { name: 'Quick', inbox: '收件箱' })
          const base = `/api/spaces/${sp.json.id}`
          expect(
            ((await call(m.browser, 'GET', `${base}/tree`)).json.folders as { path: string }[]).map((f) => f.path),
          ).not.toContain('收件箱')
          // What quick capture sends: the inbox folder and a body, no title.
          const note = await call(m.browser, 'POST', `${base}/pages`, { folder: sp.json.inbox, body: '一句话\n' })
          expect(note.status).toBe(201)
          expect(note.json.folder).toBe('收件箱')
          expect('title' in note.json.meta).toBe(false)
          const tree = (await call(m.browser, 'GET', `${base}/tree`)).json.folders as { path: string; pages: number }[]
          expect(tree.find((f) => f.path === '收件箱')).toEqual({ path: '收件箱', pages: 1 })
          await call(m.browser, 'PATCH', base, { inbox: '别处' })
          const after = await call(m.browser, 'GET', `${base}/pages/${note.json.id}`)
          expect(after.json.folder).toBe('收件箱')
          const listed = (await call(m.browser, 'GET', `${base}/pages?folder=${encodeURIComponent('收件箱')}`)).json
            .items as PageSummary[]
          expect(listed.map((i) => i.id)).toEqual([note.json.id])
        })

        it('a space of someone else is 404 not_found, the same as one that does not exist: pages, folders, drafts, rename', async () => {
          const m = await newMember(uniq('is'))
          const mine = ((await call(m.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[])[0]
          const own = await call(m.browser, 'POST', `/api/spaces/${mine.id}/pages`, {
            meta: { title: '只有我能看' },
            body: '秘密\n',
          })
          expect(own.status).toBe(201)
          const mineSpace = `/api/spaces/${mine.id}`
          const theirs = `/api/spaces/${spaceId}`
          const absent = await call(admin, 'GET', `/api/spaces/01ZZZZZZZZ/pages`)
          expect([absent.status, absent.json.error]).toEqual([404, 'not_found'])
          for (const [b, method, path, body] of [
            [m.browser, 'GET', `${theirs}/pages`, undefined],
            [m.browser, 'POST', `${theirs}/pages`, { body: 'x' }],
            [m.browser, 'PATCH', theirs, { name: '抢走' }],
            [m.browser, 'GET', `${theirs}/members`, undefined],
          ] as [Browser, string, string, unknown][]) {
            const r = await call(b, method, path, body ?? (method === 'GET' ? undefined : {}))
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([404, 'not_found'])
          }
          // An administrator is no member and manages the space without reading it; a view, opened with a reason, lets
          // them read (and nothing else); it is not in their own list.
          for (const path of [`${mineSpace}/pages`, `${mineSpace}/pages/${own.json.id}`, `${mineSpace}/tree`]) {
            expect([(await call(admin, 'GET', path)).status, path].join(' ')).toBe(`403 ${path}`)
          }
          expect((await call(admin, 'POST', `/api/admin/spaces/${mine.id}/view`, {})).status).toBe(400)
          expect((await call(admin, 'GET', `/api/admin/spaces/${mine.id}`)).json.view).toBeNull()
          const view = await call(admin, 'POST', `/api/admin/spaces/${mine.id}/view`, { reason: 'contract test' })
          expect(view.status).toBe(200)
          expect(view.json.space).toBe(mine.id)
          // The server says the view is open: since and until, on the space and in the list.
          const open = (await call(admin, 'GET', `/api/admin/spaces/${mine.id}`)).json.view
          expect(open).toEqual({ since: expect.any(String), until: view.json.until })
          expect(Date.parse(open.until) - Date.parse(open.since)).toBe(3_600_000)
          const listed = (await call(admin, 'GET', `/api/admin/spaces?owner=${m.user.id}`)).json.items as AdminSpace[]
          expect(listed.find((x) => x.id === mine.id)?.view).toEqual(open)
          for (const path of [`${mineSpace}/pages`, `${mineSpace}/pages/${own.json.id}`, `${mineSpace}/tree`]) {
            expect((await call(admin, 'GET', path)).status, path).toBe(200)
          }
          expect((await call(admin, 'POST', `${mineSpace}/pages`, { body: 'x' })).status).toBe(403)
          expect((await call(admin, 'DELETE', `/api/admin/spaces/${mine.id}/view`)).status).toBe(204)
          expect((await call(admin, 'GET', `/api/admin/spaces/${mine.id}`)).json.view).toBeNull()
          expect(((await call(admin, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).map((s) => s.id)).not.toContain(
            mine.id,
          )
          // Nothing of theirs shows in their lists, either way.
          expect((await walk({ q: '只有我能看', recursive: true, folder: '' })).length).toBe(0)
        })
      })

      // A space owned by a member, so the contract user (an administrator) is no member of it.
      const ownedSpace = async (owner: { browser: Browser }, name: string): Promise<SpaceInfo> => {
        const r = await call(owner.browser, 'POST', '/api/spaces', { name })
        expect(r.status).toBe(201)
        return r.json as SpaceInfo
      }
      const addMember = async (owner: { browser: Browser }, space: string, username: string, role: string) => {
        const r = await call(owner.browser, 'POST', `/api/spaces/${space}/members`, { username, role })
        expect(r.status, `${username} as ${role}`).toBe(201)
        return r.json as Member
      }
      const roleOf = async (b: Browser, space: string) =>
        ((await call(b, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find((s) => s.id === space)

      describe('groups', () => {
        const mkGroup = async (name: string, source = 'local') => {
          const r = await call(admin, 'POST', '/api/admin/groups', { name, source })
          expect(r.status, name).toBe(201)
          return r.json as {
            id: string
            name: string
            source: string
            people: number
            spaces: number
            mappedFrom: unknown[]
          }
        }

        it('an administrator makes, renames, lists and deletes groups; names are unique ignoring case; only administrators may', async () => {
          const name = uniq('Group')
          const g = await mkGroup(name)
          expect(g).toMatchObject({ name, source: 'local', people: 0, spaces: 0, mappedFrom: [] })
          const clash = await call(admin, 'POST', '/api/admin/groups', { name: name.toUpperCase(), source: 'local' })
          expect([clash.status, clash.json.error]).toEqual([409, 'exists'])
          const bad = await call(admin, 'POST', '/api/admin/groups', { name: ' ', source: 'local' })
          expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
          const renamed = await call(admin, 'PATCH', `/api/admin/groups/${g.id}`, {
            name: `${name} 2`,
            description: 'd',
          })
          expect(renamed.json).toMatchObject({ name: `${name} 2`, description: 'd' })
          expect((await call(admin, 'PATCH', `/api/admin/groups/${g.id}`, { source: 'synced' })).status).toBe(400)
          const list = await call(admin, 'GET', `/api/admin/groups?q=${encodeURIComponent(name.toLowerCase())}`)
          expect((list.json.items as { id: string }[]).map((x) => x.id)).toEqual([g.id])
          expect(list.json.next).toBe('')
          const m = await newMember(uniq('gm'))
          expect((await call(m.browser, 'GET', '/api/admin/groups')).status).toBe(403)
          expect((await call(admin, 'GET', '/api/admin/groups/01ZZZZZZZZ')).json.error).toBe('not_found')
          expect((await call(admin, 'DELETE', `/api/admin/groups/${g.id}`)).status).toBe(204)
          expect((await call(admin, 'GET', `/api/admin/groups/${g.id}`)).status).toBe(404)
        })

        it('people: local groups only, all or nothing; synced groups refuse; anyone signed in finds a group by name and sees theirs in the account', async () => {
          const local = await mkGroup(uniq('Local'))
          const synced = await mkGroup(uniq('Synced'), 'synced')
          const m = await newMember(uniq('gp'))
          const refused = await call(admin, 'POST', `/api/admin/groups/${synced.id}/members`, { userIds: [m.user.id] })
          expect([refused.status, refused.json.error]).toEqual([409, 'group_synced'])
          const missing = await call(admin, 'POST', `/api/admin/groups/${local.id}/members`, {
            userIds: [m.user.id, '01ZZZZZZZZ'],
          })
          expect([missing.status, missing.json.error, missing.json.missing]).toEqual([
            404,
            'user_not_found',
            ['01ZZZZZZZZ'],
          ])
          expect((await call(admin, 'GET', `/api/admin/groups/${local.id}`)).json.people).toBe(0)
          const added = await call(admin, 'POST', `/api/admin/groups/${local.id}/members`, {
            userIds: [m.user.id, m.user.id],
          })
          expect(added.json).toEqual({ added: 1, already: 0 })
          const people = (await call(admin, 'GET', `/api/admin/groups/${local.id}/members`)).json.items as {
            userId: string
            from: unknown[]
          }[]
          expect(people.map((p) => [p.userId, p.from])).toEqual([[m.user.id, []]])
          expect(
            ((await call(m.browser, 'GET', '/api/account')).json.groups as { id: string }[]).map((x) => x.id),
          ).toEqual([local.id])
          const found = await call(m.browser, 'GET', `/api/groups?q=${encodeURIComponent(local.name.slice(1, 8))}`)
          expect(found.json.groups).toEqual([{ id: local.id, name: local.name, source: 'local' }])
          expect((await call(m.browser, 'GET', '/api/groups')).status).toBe(400)
          expect((await call(admin, 'DELETE', `/api/admin/groups/${local.id}/members/${m.user.id}`)).status).toBe(204)
          expect((await call(admin, 'DELETE', `/api/admin/groups/${local.id}/members/${m.user.id}`)).status).toBe(404)
          await call(admin, 'DELETE', `/api/admin/groups/${local.id}`)
          await call(admin, 'DELETE', `/api/admin/groups/${synced.id}`)
        })

        it('a group in a space gives its people the role; the union with their own; leaving through a group is refused; removal ends it', async () => {
          const owner = await newMember(uniq('go'))
          const person = await newMember(uniq('gq'))
          const sp = await ownedSpace(owner, 'Shared')
          const g = await mkGroup(uniq('Team'))
          await call(admin, 'POST', `/api/admin/groups/${g.id}/members`, { userIds: [person.user.id] })
          const base = `/api/spaces/${sp.id}`
          expect((await call(person.browser, 'GET', `${base}/members`)).status).toBe(404)
          const refusals: [unknown, number, string][] = [
            [{ groupId: '01ZZZZZZZZ', role: 'viewer' }, 404, 'group_not_found'],
            [{ groupId: g.id, role: 'boss' }, 400, 'invalid'],
          ]
          for (const [body, status, code] of refusals) {
            const r = await call(owner.browser, 'POST', `${base}/groups`, body)
            expect([r.status, r.json.error], JSON.stringify(body)).toEqual([status, code])
          }
          const mine = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find(
            (s) => s.personal,
          )!
          expect(
            (await call(owner.browser, 'POST', `/api/spaces/${mine.id}/groups`, { groupId: g.id, role: 'viewer' })).json
              .error,
          ).toBe('personal_space')
          const added = await call(owner.browser, 'POST', `${base}/groups`, { groupId: g.id, role: 'editor' })
          expect(added.status).toBe(201)
          expect(added.json).toEqual({
            groupId: g.id,
            name: g.name,
            source: 'local',
            role: 'editor',
            people: 1,
            yours: false,
          })
          expect(
            (await call(owner.browser, 'POST', `${base}/groups`, { groupId: g.id, role: 'viewer' })).json.error,
          ).toBe('exists')
          expect(await roleOf(person.browser, sp.id)).toMatchObject({
            role: 'editor',
            directRole: '',
            viaGroups: true,
            permissions: ['read', 'write'],
          })
          // Members and groups of the space, and everyone who can reach it.
          const listed = (await call(person.browser, 'GET', `${base}/members`)).json as {
            members: Member[]
            groups: { groupId: string; yours: boolean }[]
          }
          expect(listed.members.map((x) => x.userId)).toEqual([owner.user.id])
          expect(listed.groups.map((x) => [x.groupId, x.yours])).toEqual([[g.id, true]])
          const eff = (await call(person.browser, 'GET', `${base}/members/effective`)).json.items as {
            userId: string
            role: string
            directRole: string
          }[]
          expect(eff.map((x) => [x.userId, x.role, x.directRole]).sort()).toEqual(
            [
              [owner.user.id, 'owner', 'owner'],
              [person.user.id, 'editor', ''],
            ].sort(),
          )
          // Cannot leave: not a member of one's own.
          const leave = await call(person.browser, 'DELETE', `${base}/members/${person.user.id}`)
          expect([leave.status, leave.json.error]).toEqual([409, 'member_via_group'])
          // Direct viewer and editor through the group: the union, then the direct membership goes and the group's role stays.
          await addMember(owner, sp.id, person.user.username, 'viewer')
          expect(await roleOf(person.browser, sp.id)).toMatchObject({
            role: 'editor',
            directRole: 'viewer',
            viaGroups: true,
          })
          expect((await call(person.browser, 'DELETE', `${base}/members/${person.user.id}`)).status).toBe(204)
          expect(await roleOf(person.browser, sp.id)).toMatchObject({ role: 'editor', directRole: '' })
          // The role of the group changes; the group leaves the space.
          expect((await call(owner.browser, 'PATCH', `${base}/groups/${g.id}`, { role: 'viewer' })).json.role).toBe(
            'viewer',
          )
          expect(await roleOf(person.browser, sp.id)).toMatchObject({ role: 'viewer', permissions: ['read'] })
          expect((await call(owner.browser, 'PATCH', `${base}/groups/01ZZZZZZZZ`, { role: 'viewer' })).status).toBe(404)
          // Deleting a group that is in a space needs force.
          const inUse = await call(admin, 'DELETE', `/api/admin/groups/${g.id}`)
          expect([inUse.status, inUse.json.error, inUse.json.spaces]).toEqual([409, 'group_in_use', 1])
          expect((await call(owner.browser, 'DELETE', `${base}/groups/${g.id}`)).status).toBe(204)
          expect((await call(person.browser, 'GET', `${base}/members`)).status).toBe(404)
          expect((await call(admin, 'DELETE', `/api/admin/groups/${g.id}`)).status).toBe(204)
        })
      })

      describe('tenants and layered settings (3.3)', () => {
        const slugOf = (p: string) =>
          `${p}${run}`
            .toLowerCase()
            .replace(/[^a-z0-9]/g, '')
            .slice(0, 24)

        it('the user object says whether a person is a site administrator and which tenant they are in', async () => {
          const me = (await call(admin, 'GET', '/api/session')).json.user as User
          expect(me.siteAdmin).toBe(true)
          expect(me.tenant?.slug).toBe('default')
          const m = await newMember(uniq('tm'))
          expect(m.user.siteAdmin).toBe(false)
          expect(m.user.tenant?.id).toBe('0000000000')
          // Only site administrators reach the server's own settings and the tenants.
          for (const path of ['/api/admin/tenancy', '/api/admin/tenants', '/api/admin/settings'])
            expect((await call(m.browser, 'GET', path)).status, path).toBe(403)
          expect((await call(m.browser, 'GET', '/api/settings')).status).toBe(200)
        })

        it('mail is off until an administrator enables it: registration and reset answer as switched off, nothing can open registration', async () => {
          const mail = (await call(admin, 'GET', '/api/admin/mail')).json
          // A server this suite runs against has not been given SMTP.
          expect(mail.enabled).toBe(false)
          expect(mail).toMatchObject({ security: 'starttls', hasPassword: false })
          const anon = browser()
          expect((await call(anon, 'GET', '/api/register')).json).toEqual({ open: false })
          const pr = (await call(anon, 'GET', '/api/auth/providers')).json
          expect([pr.register, pr.forgot]).toEqual([false, false])
          expect((await call(anon, 'POST', '/api/password/forgot', { login: 'nobody' })).json.error).toBe(
            'password_reset_off',
          )
          expect(
            (
              await call(anon, 'POST', '/api/register', {
                username: 'x1',
                name: 'x',
                email: 'x@example.test',
                password: 'a-long-password',
              })
            ).json.error,
          ).toBe('registration_closed')
          expect((await call(anon, 'POST', '/api/email/confirm', { token: 'nope' })).json.error).toBe('not_found')
          expect((await call(admin, 'PUT', '/api/admin/settings/registration.open', { value: true })).json.error).toBe(
            'mail_not_configured',
          )
          expect(
            (await call(admin, 'PUT', '/api/admin/mail', { security: 'none', host: 'mail.example.test' })).json.error,
          ).toBe('mail_insecure')
          const m = await newMember(uniq('ml'))
          for (const [method, path] of [
            ['GET', '/api/admin/mail'],
            ['PUT', '/api/admin/mail'],
            ['POST', '/api/admin/mail/test'],
          ] as const)
            expect((await call(m.browser, method, path, method === 'GET' ? undefined : {})).status, path).toBe(403)
        })

        it('the switch gates making tenants; a tenant is made, listed, renamed, disabled and deleted; the default one is kept', async () => {
          const was = ((await call(admin, 'GET', '/api/admin/tenancy')).json as { enabled: boolean }).enabled
          try {
            expect((await call(admin, 'PUT', '/api/admin/tenancy', { enabled: false })).status).toBe(200)
            const slug = slugOf('tn')
            expect((await call(admin, 'POST', '/api/admin/tenants', { slug, name: 'Acme' })).json.error).toBe(
              'tenants_off',
            )
            expect((await call(admin, 'PUT', '/api/admin/tenancy', { enabled: true })).json.enabled).toBe(true)
            const bad = await call(admin, 'POST', '/api/admin/tenants', { slug: 'A', name: 'x' })
            expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
            const made = await call(admin, 'POST', '/api/admin/tenants', { slug, name: ' Acme ' })
            expect(made.status).toBe(201)
            const t = made.json as { id: string; slug: string; name: string; default: boolean; disabled: boolean }
            expect([t.slug, t.name, t.default, t.disabled]).toEqual([slug, 'Acme', false, false])
            expect((await call(admin, 'POST', '/api/admin/tenants', { slug, name: 'Again' })).json.error).toBe('exists')
            const list = (await call(admin, 'GET', '/api/admin/tenants?limit=200')).json as {
              items: { id: string; default: boolean }[]
            }
            expect(list.items[0].default).toBe(true)
            expect(list.items.some((x) => x.id === t.id)).toBe(true)
            const renamed = await call(admin, 'PATCH', `/api/admin/tenants/${t.id}`, {
              name: 'Acme Two',
              disabled: true,
            })
            expect([renamed.json.name, renamed.json.disabled]).toEqual(['Acme Two', true])
            expect((await call(admin, 'PATCH', `/api/admin/tenants/${t.id}`, { mood: 1 })).status).toBe(400)
            expect((await call(admin, 'GET', '/api/admin/tenants/01ZZZZZZZZ')).json.error).toBe('not_found')
            for (const body of [{ disabled: true }])
              expect((await call(admin, 'PATCH', '/api/admin/tenants/0000000000', body)).json.error).toBe(
                'default_tenant',
              )
            expect((await call(admin, 'DELETE', '/api/admin/tenants/0000000000')).json.error).toBe('default_tenant')
            expect((await call(admin, 'DELETE', `/api/admin/tenants/${t.id}`)).status).toBe(204)
            expect((await call(admin, 'GET', `/api/admin/tenants/${t.id}`)).status).toBe(404)
          } finally {
            await call(admin, 'PUT', '/api/admin/tenancy', { enabled: was })
          }
        })

        it('settings by layer: values are checked, a lock refuses the tenant, a restriction caps it, and what a lock hides is kept', async () => {
          const was = ((await call(admin, 'GET', '/api/admin/tenancy')).json as { enabled: boolean }).enabled
          await call(admin, 'PUT', '/api/admin/tenancy', { enabled: true })
          const t = (await call(admin, 'POST', '/api/admin/tenants', { slug: slugOf('ts'), name: 'Settings' }))
            .json as { id: string }
          const site = (key: string, body: unknown) => call(admin, 'PUT', `/api/admin/settings/${key}`, body)
          const mine = (key: string, body: unknown) =>
            call(admin, 'PUT', `/api/admin/tenants/${t.id}/settings/${key}`, body)
          const view = async () =>
            (
              (await call(admin, 'GET', `/api/admin/tenants/${t.id}/settings`)).json.settings as {
                key: string
                effective: unknown
                tenant: { value: unknown; inEffect: boolean }
                site: { lower: { mode: string } }
              }[]
            ).find((x) => x.key === 'registration.perHour')!
          try {
            expect((await site('registration.perHour', { value: 0 })).json.error).toBe('invalid')
            expect((await site('no.such.setting', { value: 1 })).status).toBe(404)
            // Registration cannot be opened while mail is not enabled; a tenant cannot touch what the site locks.
            expect((await site('registration.open', { value: true })).json.error).toBe('mail_not_configured')
            expect((await mine('registration.open', { value: true })).json.error).toBe('setting_locked')
            // The site's rule for perHour: at most 50. A tenant above it is refused, below it is in effect.
            expect((await site('registration.perHour', { lower: { mode: 'restrict', max: 50 } })).status).toBe(200)
            expect((await mine('registration.perHour', { value: 80 })).json.error).toBe('setting_restricted')
            expect((await mine('registration.perHour', { value: 30 })).status).toBe(200)
            expect((await view()).effective).toBe(30)
            // Tightened to 10: the tenant's 30 is kept, not in effect, and the answer counts the tenants.
            const tight = await site('registration.perHour', { lower: { mode: 'restrict', max: 10 } })
            expect(tight.json.affected).toBeGreaterThanOrEqual(1)
            const v = await view()
            expect([v.tenant.value, v.tenant.inEffect]).toEqual([30, false])
            // Loosened again: it counts again.
            await site('registration.perHour', { lower: { mode: 'restrict', max: 50 } })
            expect((await view()).tenant.inEffect).toBe(true)
            expect((await mine('registration.perHour', { value: null })).status).toBe(200)
            expect((await mine('registration.perHour', { lower: { mode: 'restrict', max: 60 } })).json.error).toBe(
              'setting_restricted',
            )
          } finally {
            await site('registration.perHour', { lower: null })
            await call(admin, 'DELETE', `/api/admin/tenants/${t.id}`)
            await call(admin, 'PUT', '/api/admin/tenancy', { enabled: was })
          }
        })

        it('where attachments go: separate by default, a choice, a site lock for one tenant the tenant cannot lift, and the exemption switch', async () => {
          const was = ((await call(admin, 'GET', '/api/admin/tenancy')).json as { enabled: boolean }).enabled
          await call(admin, 'PUT', '/api/admin/tenancy', { enabled: true })
          const t = (await call(admin, 'POST', '/api/admin/tenants', { slug: slugOf('as'), name: 'Assets' })).json as {
            id: string
          }
          type Row = {
            key: string
            choices?: string[]
            effective: unknown
            tenant: { value: unknown; inEffect: boolean; siteLocked: boolean }
            site: { value: unknown; lower: { mode: string } }
          }
          const view = async () =>
            ((await call(admin, 'GET', `/api/admin/tenants/${t.id}/settings`)).json.settings as Row[]).find(
              (x) => x.key === 'storage.assets',
            )!
          const mine = (body: unknown) => call(admin, 'PUT', `/api/admin/tenants/${t.id}/settings/storage.assets`, body)
          try {
            const first = await view()
            expect([first.effective, first.choices, first.tenant.siteLocked]).toEqual([
              'repository',
              ['space', 'repository'],
              false,
            ])
            expect((await mine({ value: 'nowhere' })).json.error).toBe('invalid')
            expect((await mine({ value: 'space' })).status).toBe(200)
            expect((await view()).effective).toBe('space')
            // A lock needs a value, and the site's own layer has no lock for a tenant.
            expect((await mine({ value: null, siteLock: true })).json.error).toBe('invalid')
            expect(
              (await call(admin, 'PUT', '/api/admin/settings/storage.assets', { siteLock: true })).json.error,
            ).toBe('invalid')
            // Locked for this tenant: its value beats the site's default.
            expect((await mine({ value: 'repository', siteLock: true })).status).toBe(200)
            expect((await call(admin, 'PUT', '/api/admin/settings/storage.assets', { value: 'space' })).status).toBe(
              200,
            )
            const locked = await view()
            expect([locked.effective, locked.tenant.siteLocked, locked.tenant.inEffect]).toEqual([
              'repository',
              true,
              true,
            ])
            // The site's lock over everyone beats the one for the tenant, which is kept.
            await call(admin, 'PUT', '/api/admin/settings/storage.assets', { lower: { mode: 'lock' } })
            const over = await view()
            expect([over.effective, over.tenant.value, over.tenant.inEffect]).toEqual(['space', 'repository', false])
            // What a person of the default tenant (the administrator here) sees when they make a space: the
            // effective default, what they may ask for, and whether a lock leaves no choice.
            const asked = (
              (await call(admin, 'GET', '/api/settings')).json.settings as {
                key: string
                effective: unknown
                allowed?: string[]
                locked?: boolean
              }[]
            ).find((x) => x.key === 'storage.assets')!
            expect([asked.effective, asked.allowed, asked.locked]).toEqual(['space', ['space'], true])
            expect(
              ((await call(admin, 'GET', '/api/admin/settings')).json.settings as Row[]).find(
                (x) => x.key === 'storage.assets.ownStorageExempt',
              )!.site.value,
            ).toBe(true)
            expect(
              (await call(admin, 'PUT', '/api/admin/settings/storage.assets.ownStorageExempt', { value: false }))
                .status,
            ).toBe(200)
          } finally {
            await call(admin, 'PUT', '/api/admin/settings/storage.assets.ownStorageExempt', { value: true })
            await call(admin, 'PUT', '/api/admin/settings/storage.assets', { value: 'repository', lower: null })
            await call(admin, 'DELETE', `/api/admin/tenants/${t.id}`)
            await call(admin, 'PUT', '/api/admin/tenancy', { enabled: was })
          }
        })

        it("the daily text's switches are the layered settings daily.own and daily.api; GET /api/settings says who keeps them off", async () => {
          const m = await newMember(uniq('ts'))
          const read = async (key: string) =>
            (
              (await call(admin, 'GET', '/api/admin/settings')).json.settings as {
                key: string
                site: { value: unknown }
              }[]
            ).find((x) => x.key === key)!.site.value
          const was = await read('daily.own')
          try {
            expect((await call(admin, 'PUT', '/api/admin/daily', { allow: { own: false } })).status).toBe(200)
            expect(await read('daily.own')).toBe(false)
            const mineAll = (await call(m.browser, 'GET', '/api/settings')).json.settings as {
              key: string
              effective: unknown
              space?: { canChange: boolean; inEffect: boolean; source?: string }
            }[]
            const own = mineAll.find((x) => x.key === 'daily.own')!
            expect([own.effective, own.space?.canChange, own.space?.inEffect, own.space?.source]).toEqual([
              false,
              false,
              false,
              'site',
            ])
            expect((await call(admin, 'PUT', '/api/admin/settings/daily.own', { value: true })).status).toBe(200)
            const back = (await call(admin, 'GET', '/api/admin/daily')).json as { allow: { own: boolean } }
            expect(back.allow.own).toBe(true)
          } finally {
            await call(admin, 'PUT', '/api/admin/settings/daily.own', { value: was })
          }
        })
      })

      describe('quotas and usage (3.3)', () => {
        const slugOf = (p: string) =>
          `${p}${run}`
            .toLowerCase()
            .replace(/[^a-z0-9]/g, '')
            .slice(0, 24)
        type Row = { used: number; limit: number | null }

        it("GET /api/account/usage is the caller's own spaces, bytes and pages against their limits", async () => {
          const m = await newMember(uniq('qa'))
          const r = await call(m.browser, 'GET', '/api/account/usage')
          expect(r.status).toBe(200)
          const u = r.json as unknown as {
            spaces: Row
            bytes: Row
            pages: Row
            measuredAt: string | null
            notices: unknown[]
          }
          // The personal space is one; nothing is measured on the request path, and nothing is limited unless an administrator said so.
          expect(u.spaces.used).toBe(1)
          expect(typeof u.spaces.limit === 'number' || u.spaces.limit === null).toBe(true)
          expect(u.bytes.used).toBeGreaterThanOrEqual(0)
          expect(u.pages.used).toBeGreaterThanOrEqual(0)
          expect(u.notices).toEqual([])
          expect(typeof u.measuredAt === 'string' || u.measuredAt === null).toBe(true)
          // Signed in only.
          expect((await call(browser(), 'GET', '/api/account/usage')).status).toBe(401)
        })

        it('GET /api/admin/usage lists people with their numbers, limits and own limits; it is checked, and for administrators only', async () => {
          const m = await newMember(uniq('qb'))
          const list = await call(admin, 'GET', '/api/admin/usage?limit=200')
          expect(list.status).toBe(200)
          const items = list.json.items as {
            user: { id: string; username: string; name: string }
            tenant: string
            spaces: number
            bytes: number
            pages: number
            limits: Record<string, number | null>
            own: Record<string, number | null>
            over: string[]
            measuredAt: string | null
          }[]
          expect(items.length).toBeGreaterThan(0)
          for (let i = 1; i < items.length; i++) expect(items[i].bytes).toBeLessThanOrEqual(items[i - 1].bytes)
          const mine = items.find((x) => x.user.id === m.user.id)
          // Found on the first page unless the server has more than 200 people with more bytes than this one.
          if (mine) {
            expect(mine.spaces).toBe(1)
            expect(Object.keys(mine.limits).sort()).toEqual(['bytes', 'pages', 'spaces'])
            expect(mine.own).toEqual({ spaces: null, bytes: null, pages: null })
            expect(mine.over).toEqual([])
          }
          expect(typeof list.json.next).toBe('string')
          expect((await call(admin, 'GET', '/api/admin/usage?limit=0')).status).toBe(400)
          expect((await call(admin, 'GET', '/api/admin/usage?over=yes')).status).toBe(400)
          expect((await call(admin, 'GET', '/api/admin/usage?tenant=01ZZZZZZZZ')).json.error).toBe('not_found')
          expect((await call(m.browser, 'GET', '/api/admin/usage')).status).toBe(403)
        })

        it("quota on PATCH /api/users/{id} sets a person's own limits; the number of spaces is the one refused, with what ran out", async () => {
          const m = await newMember(uniq('qc'))
          const path = `/api/users/${m.user.id}`
          expect((await call(m.browser, 'PATCH', path, { quota: { spaces: 5 } })).status).toBe(403)
          for (const bad of [
            'x',
            { disk: 1 },
            { spaces: -1 },
            { spaces: 10001 },
            { bytes: 0 },
            { pages: 1.5 },
            { bytes: '1' },
          ]) {
            const r = await call(admin, 'PATCH', path, { quota: bad })
            expect([r.status, r.json.error], JSON.stringify(bad)).toEqual([400, 'invalid'])
          }
          const set = await call(admin, 'PATCH', path, { quota: { spaces: 1 } })
          expect(set.status).toBe(200)
          expect((set.json.user as User).quota).toEqual({ spaces: 1, bytes: null, pages: null })
          try {
            // The personal space is the one they may have.
            const refused = await call(m.browser, 'POST', '/api/spaces', { name: 'One too many' })
            expect([
              refused.status,
              refused.json.error,
              refused.json.quota,
              refused.json.limit,
              refused.json.used,
            ]).toEqual([403, 'quota_exceeded', 'spaces', 1, 1])
            expect((await call(m.browser, 'GET', '/api/account/usage')).json.spaces).toEqual({ used: 1, limit: 1 })
            const raised = await call(admin, 'PATCH', path, { quota: { spaces: 3, pages: 100000 } })
            expect((raised.json.user as User).quota).toEqual({ spaces: 3, bytes: null, pages: 100000 })
            expect((await call(m.browser, 'POST', '/api/spaces', { name: 'Allowed now' })).status).toBe(201)
            // 0 is no limit; null follows the tenant and the site again.
            expect((await call(admin, 'PATCH', path, { quota: { spaces: 0 } })).status).toBe(200)
            expect((await call(m.browser, 'GET', '/api/account/usage')).json.spaces).toMatchObject({
              used: 2,
              limit: null,
            })
          } finally {
            const cleared = await call(admin, 'PATCH', path, { quota: { spaces: null, bytes: null, pages: null } })
            expect((cleared.json.user as User).quota).toEqual({ spaces: null, bytes: null, pages: null })
          }
          const listed = ((await call(admin, 'GET', `/api/users?q=${m.user.username}`)).json.users as User[]).find(
            (x) => x.id === m.user.id,
          )
          expect(listed?.quota).toEqual({ spaces: null, bytes: null, pages: null })
        })

        it('the quotas are layered settings with a person layer, checked like the others; a tenant says what it uses', async () => {
          const settings = (await call(admin, 'GET', '/api/admin/settings')).json.settings as {
            key: string
            kind: string
            layers: string[]
            site: { value: unknown }
          }[]
          for (const key of ['quota.spacesPerPerson', 'quota.bytesPerPerson', 'quota.pagesPerPerson']) {
            const d = settings.find((x) => x.key === key)
            expect([d?.kind, d?.layers]).toEqual(['limit', ['site', 'tenant', 'person']])
          }
          expect((await call(admin, 'PUT', '/api/admin/settings/quota.bytesPerPerson', { value: 0 })).json.error).toBe(
            'invalid',
          )
          expect(
            (await call(admin, 'PUT', '/api/admin/settings/quota.spacesPerPerson', { value: 10001 })).json.error,
          ).toBe('invalid')
          // A person has no use for the layer: it is not among what GET /api/settings offers.
          const m = await newMember(uniq('qd'))
          expect(
            ((await call(m.browser, 'GET', '/api/settings')).json.settings as { key: string }[]).some((x) =>
              x.key.startsWith('quota.'),
            ),
          ).toBe(false)
          const tenant = (await call(admin, 'GET', '/api/admin/tenants/0000000000')).json as unknown as {
            usage: {
              people: number
              spaces: number
              bytes: number
              pages: number
              measuredAt: string | null
              over: string[]
            }
          }
          expect(tenant.usage.people).toBeGreaterThanOrEqual(1)
          expect(tenant.usage.spaces).toBeGreaterThanOrEqual(1)
          expect(Array.isArray(tenant.usage.over)).toBe(true)
          expect('measuredAt' in tenant.usage).toBe(true)
        })

        it("a new tenant's first administrator's invite fills in the form that accepts it; nothing is mailed", async () => {
          const was = ((await call(admin, 'GET', '/api/admin/tenancy')).json as { enabled: boolean }).enabled
          await call(admin, 'PUT', '/api/admin/tenancy', { enabled: true })
          const username = uniq('qa')
          const made = await call(admin, 'POST', '/api/admin/tenants', {
            slug: slugOf('qi'),
            name: 'Invite',
            admin: { username, name: 'First Admin', email: 'first@example.test' },
          })
          expect(made.status).toBe(201)
          const tenantId = made.json.id as string
          const inv = made.json.invite as {
            id: string
            token: string
            role: string
            username: string
            name: string
            email: string
          }
          try {
            expect([inv.role, inv.username, inv.name, inv.email]).toEqual([
              'admin',
              username,
              'First Admin',
              'first@example.test',
            ])
            const check = await call(browser(), 'POST', '/api/invites/check', { token: inv.token })
            expect(check.status).toBe(200)
            expect([check.json.role, check.json.username, check.json.name, check.json.email]).toEqual([
              'admin',
              username,
              'First Admin',
              'first@example.test',
            ])
            // An invite without a fill has nothing in them.
            const plain = (await call(admin, 'POST', '/api/invites', { role: 'member' })).json as {
              id: string
              token: string
            }
            const empty = await call(browser(), 'POST', '/api/invites/check', { token: plain.token })
            expect([empty.json.username ?? '', empty.json.name ?? '', empty.json.email ?? '']).toEqual(['', '', ''])
            await call(admin, 'DELETE', `/api/invites/${plain.id}`)
            const bad = await call(admin, 'POST', '/api/admin/tenants', {
              slug: slugOf('qj'),
              name: 'Bad',
              admin: { username, email: 'not-an-address' },
            })
            expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
          } finally {
            await call(admin, 'DELETE', `/api/invites/${inv.id}`)
            await call(admin, 'DELETE', `/api/admin/tenants/${tenantId}`)
            await call(admin, 'PUT', '/api/admin/tenancy', { enabled: was })
          }
        })
      })

      describe('members and permissions', () => {
        it("adds a member by username with a role; lists owners first with `you`; the role and its permissions show in the member's own list", async () => {
          const owner = await newMember(uniq('mo'))
          const guest = await newMember(uniq('mg'))
          const sp = await ownedSpace(owner, 'Shared')
          expect(sp).toMatchObject({
            role: 'owner',
            permissions: ['read', 'write', 'members', 'manage'],
            disabled: false,
          })
          // Not a member yet: not even found.
          expect((await call(guest.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
          const added = await addMember(owner, sp.id, guest.user.username.toUpperCase(), 'viewer')
          expect(added).toEqual({
            userId: guest.user.id,
            username: guest.user.username,
            name: guest.user.name,
            role: 'viewer',
            you: false,
          })
          const list = (await call(owner.browser, 'GET', `/api/spaces/${sp.id}/members`)).json.members as Member[]
          expect(list.map((m) => [m.userId, m.role, m.you])).toEqual([
            [owner.user.id, 'owner', true],
            [guest.user.id, 'viewer', false],
          ])
          expect(await roleOf(guest.browser, sp.id)).toMatchObject({
            role: 'viewer',
            permissions: ['read'],
            disabled: false,
          })
          // The role changes at the member's very next request.
          const changed = await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/${guest.user.id}`, {
            role: 'editor',
          })
          expect(changed.status).toBe(200)
          expect(changed.json).toMatchObject({ userId: guest.user.id, role: 'editor', you: false })
          expect(await roleOf(guest.browser, sp.id)).toMatchObject({ role: 'editor', permissions: ['read', 'write'] })
          // Removed: the space is gone for them, at once.
          expect((await call(owner.browser, 'DELETE', `/api/spaces/${sp.id}/members/${guest.user.id}`)).status).toBe(
            204,
          )
          expect((await call(guest.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
          expect(await roleOf(guest.browser, sp.id)).toBeUndefined()
        })

        it('refuses what it should: an unknown user 404 user_not_found, a member already 409 exists, a role that is none 400 invalid, a personal space 409 personal_space', async () => {
          const owner = await newMember(uniq('me'))
          const guest = await newMember(uniq('mf'))
          const sp = await ownedSpace(owner, 'Rules')
          const add = (username: string, role: string, space = sp.id) =>
            call(owner.browser, 'POST', `/api/spaces/${space}/members`, { username, role })
          const unknown = await add(uniq('nobody'), 'viewer')
          expect([unknown.status, unknown.json.error]).toEqual([404, 'user_not_found'])
          await addMember(owner, sp.id, guest.user.username, 'viewer')
          const twice = await add(guest.user.username, 'editor')
          expect([twice.status, twice.json.error]).toEqual([409, 'exists'])
          const own = await add(owner.user.username, 'editor')
          expect([own.status, own.json.error]).toEqual([409, 'exists'])
          const bad = await add(guest.user.username, 'boss', (await ownedSpace(owner, 'Rules 2')).id)
          expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
          const personal = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find(
            (s) => s.personal,
          )!
          const shared = await add(guest.user.username, 'viewer', personal.id)
          expect([shared.status, shared.json.error]).toEqual([409, 'personal_space'])
          const notMember = await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/01ZZZZZZZZ`, {
            role: 'viewer',
          })
          expect([notMember.status, notMember.json.error]).toEqual([404, 'not_found'])
          const badRole = await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/${guest.user.id}`, {
            role: 'boss',
          })
          expect([badRole.status, badRole.json.error]).toEqual([400, 'invalid'])
        })

        it('a space keeps an owner: the only owner can neither be demoted nor removed (409 last_owner), until there is another', async () => {
          const owner = await newMember(uniq('lx'))
          const second = await newMember(uniq('lw'))
          const sp = await ownedSpace(owner, 'Owners')
          const self = `/api/spaces/${sp.id}/members/${owner.user.id}`
          const demote = await call(owner.browser, 'PATCH', self, { role: 'editor' })
          expect([demote.status, demote.json.error]).toEqual([409, 'last_owner'])
          const leave = await call(owner.browser, 'DELETE', self)
          expect([leave.status, leave.json.error]).toEqual([409, 'last_owner'])
          expect((await roleOf(owner.browser, sp.id))!.role).toBe('owner')
          await addMember(owner, sp.id, second.user.username, 'owner')
          expect((await call(owner.browser, 'PATCH', self, { role: 'editor' })).status).toBe(200)
          // The new owner is the last one now.
          const last = await call(second.browser, 'DELETE', `/api/spaces/${sp.id}/members/${second.user.id}`)
          expect([last.status, last.json.error]).toEqual([409, 'last_owner'])
        })

        it("any member may leave a space, a viewer too; someone else's removal needs the members permission", async () => {
          const owner = await newMember(uniq('lv'))
          const viewer = await newMember(uniq('vw'))
          const editor = await newMember(uniq('ed'))
          const sp = await ownedSpace(owner, 'Leaving')
          await addMember(owner, sp.id, viewer.user.username, 'viewer')
          await addMember(owner, sp.id, editor.user.username, 'editor')
          const other = await call(editor.browser, 'DELETE', `/api/spaces/${sp.id}/members/${viewer.user.id}`)
          expect([other.status, other.json.error]).toEqual([403, 'no_permission'])
          expect((await call(viewer.browser, 'DELETE', `/api/spaces/${sp.id}/members/${viewer.user.id}`)).status).toBe(
            204,
          )
          expect((await call(viewer.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
          expect((await call(editor.browser, 'DELETE', `/api/spaces/${sp.id}/members/${editor.user.id}`)).status).toBe(
            204,
          )
          const left = (await call(owner.browser, 'GET', `/api/spaces/${sp.id}/members`)).json.members as Member[]
          expect(left.map((m) => m.userId)).toEqual([owner.user.id])
        })

        it('a viewer reads but is refused every write with 403 no_permission; an editor writes but cannot manage or add members', async () => {
          const owner = await newMember(uniq('wo'))
          const viewer = await newMember(uniq('wv'))
          const editor = await newMember(uniq('we'))
          const sp = await ownedSpace(owner, 'Writes')
          await addMember(owner, sp.id, viewer.user.username, 'viewer')
          await addMember(owner, sp.id, editor.user.username, 'editor')
          const base = `/api/spaces/${sp.id}`
          const page = await call(owner.browser, 'POST', `${base}/pages`, { meta: { title: '读得到' }, body: '正文\n' })
          expect(page.status).toBe(201)
          for (const path of [
            `${base}/pages`,
            `${base}/pages/${page.json.id}`,
            `${base}/tree`,
            `${base}/tags`,
            `${base}/members`,
            `${base}/views?folder=`,
            `${base}/pages/groups?folder=&key=status`,
            `${base}/templates`,
          ]) {
            expect((await call(viewer.browser, 'GET', path)).status, `viewer GET ${path}`).toBe(200)
          }
          const writes: [string, string, unknown][] = [
            ['POST', `${base}/pages`, { body: 'x' }],
            ['PUT', `${base}/pages/${page.json.id}`, { baseRev: page.json.rev, meta: page.json.meta, body: '改\n' }],
            [
              'PUT',
              `${base}/pages/${page.json.id}/draft`,
              { baseRev: page.json.rev, meta: page.json.meta, body: '改\n', prev: '' },
            ],
            ['POST', `${base}/pages/${page.json.id}/move`, { folder: 'x' }],
            ['DELETE', `${base}/pages/${page.json.id}`, undefined],
            ['POST', `${base}/pages/batch`, { op: 'delete', ids: [page.json.id] }],
            ['POST', `${base}/folders`, { path: 'x' }],
            ['PUT', `${base}/tags/blue`, { color: 'red' }],
            ['PATCH', `${base}/pages/${page.json.id}/props`, { baseRev: page.json.rev, set: { status: 'x' } }],
            ['PUT', `${base}/types/task`, { title: '任务', fields: [] }],
            ['DELETE', `${base}/types/task`, undefined],
            ['PUT', `${base}/folders/type`, { folder: '', type: '' }],
            ['PUT', `${base}/views`, { folder: '', baseRev: '0'.repeat(40), views: [] }],
            ['PUT', `${base}/templates/日记`, { baseRev: '', meta: {}, body: '' }],
            ['DELETE', `${base}/templates/日记?baseRev=x`, undefined],
            ['PUT', `${base}/templates/defaults`, { quickNote: '' }],
            ['PATCH', base, { name: '改名' }],
            ['POST', `${base}/reindex`, undefined],
            ['POST', `${base}/members`, { username: owner.user.username, role: 'viewer' }],
          ]
          for (const [method, path, body] of writes) {
            const r = await call(viewer.browser, method, path, body ?? {})
            expect([r.status, r.json?.error], `viewer ${method} ${path}`).toEqual([403, 'no_permission'])
          }
          // The page is as it was.
          const still = await call(owner.browser, 'GET', `${base}/pages/${page.json.id}`)
          expect(still.json).toMatchObject({ rev: page.json.rev, body: '正文\n' })

          const made = await call(editor.browser, 'POST', `${base}/pages`, {
            meta: { title: '编辑者写的' },
            body: 'x\n',
          })
          expect(made.status).toBe(201)
          expect((await call(editor.browser, 'PUT', `${base}/tags/blue`, { color: 'red' })).status).toBe(200)
          // An editor writes properties of a page, and reads the types; making one needs `manage`.
          const patched = await call(editor.browser, 'PATCH', `${base}/pages/${page.json.id}/props`, {
            baseRev: page.json.rev,
            set: { status: 'x' },
          })
          expect([patched.status, patched.json.meta.status]).toEqual([200, 'x'])
          expect((await call(editor.browser, 'GET', `${base}/types`)).status).toBe(200)
          // Templates are made by those who manage; whoever writes pages makes pages from them.
          const tpl = await call(owner.browser, 'PUT', `${base}/templates/日记`, {
            baseRev: '',
            meta: { title: '日记 {{date}}' },
            body: '今天\n',
          })
          expect(tpl.status).toBe(200)
          expect((await call(viewer.browser, 'GET', `${base}/templates/日记`)).json.rev).toBe(tpl.json.rev)
          const fromTemplate = await call(editor.browser, 'POST', `${base}/pages`, { template: '日记', tz: 0 })
          expect([fromTemplate.status, fromTemplate.json.body]).toEqual([201, '今天\n'])
          // Views are a shared arrangement of pages: whoever may write pages may save them.
          const noViews = await call(editor.browser, 'GET', `${base}/views?folder=`)
          const savedViews = await call(editor.browser, 'PUT', `${base}/views`, {
            folder: '',
            baseRev: noViews.json.rev,
            views: [{ name: '全部', layout: 'cards' }],
          })
          expect([savedViews.status, savedViews.json.views[0].name]).toEqual([200, '全部'])
          expect((await call(viewer.browser, 'GET', `${base}/views?folder=`)).json).toEqual(savedViews.json)
          for (const [method, path, body] of [
            ['PUT', `${base}/types/task`, { title: '任务', fields: [] }],
            ['DELETE', `${base}/types/task`, {}],
            ['PUT', `${base}/folders/type`, { folder: '', type: '' }],
            ['PUT', `${base}/templates/日记`, { baseRev: '', meta: {}, body: '' }],
            ['DELETE', `${base}/templates/日记?baseRev=x`, {}],
            ['PUT', `${base}/templates/defaults`, { quickNote: '' }],
            ['PATCH', base, { name: '改名' }],
            ['POST', `${base}/reindex`, {}],
            ['POST', `${base}/members`, { username: viewer.user.username, role: 'editor' }],
            ['PATCH', `${base}/members/${viewer.user.id}`, { role: 'editor' }],
          ] as [string, string, unknown][]) {
            const r = await call(editor.browser, method, path, body)
            expect([r.status, r.json?.error], `editor ${method} ${path}`).toEqual([403, 'no_permission'])
          }
        })
      })

      describe('notifications (3.10, part 4)', () => {
        const notes = async (b: Browser, query = '') => (await call(b, 'GET', `/api/notifications${query}`)).json
        const count = async (b: Browser) => (await call(b, 'GET', '/api/notifications/unread')).json.count as number

        it('adding a member tells them, once, with the facts a sentence is made of; nobody else is told', async () => {
          const owner = await newMember(uniq('no'))
          const guest = await newMember(uniq('ng'))
          const other = await newMember(uniq('nx'))
          const sp = await ownedSpace(owner, 'Notified')
          expect(await count(guest.browser)).toBe(0)
          await addMember(owner, sp.id, guest.user.username, 'editor')
          expect(await count(guest.browser)).toBe(1)
          const l = await notes(guest.browser)
          expect(l.unread).toBe(1)
          expect(l.next).toBe('')
          expect(l.items).toHaveLength(1)
          expect(l.items[0]).toMatchObject({
            kind: 'invite',
            ref: sp.id,
            payload: { space: 'Notified', role: 'editor', by: owner.user.name },
            read: false,
          })
          expect(typeof l.items[0].id).toBe('string')
          expect(l.items[0].at).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/)
          // Not the person who added them, not someone who is no member.
          expect(await count(owner.browser)).toBe(0)
          expect(await count(other.browser)).toBe(0)
          // A change of role is no invitation.
          await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/${guest.user.id}`, { role: 'viewer' })
          expect(await count(guest.browser)).toBe(1)
        })

        it('marks one as read for the person only, all of them, and answers the count that is left', async () => {
          const owner = await newMember(uniq('mo2'))
          const guest = await newMember(uniq('mg2'))
          const a = await ownedSpace(owner, 'First')
          const b = await ownedSpace(owner, 'Second')
          await addMember(owner, a.id, guest.user.username, 'viewer')
          await addMember(owner, b.id, guest.user.username, 'viewer')
          const l = await notes(guest.browser)
          expect(l.items.map((i: { ref: string }) => i.ref)).toEqual([b.id, a.id]) // newest first
          // Another person cannot read it for them, with its id or without.
          const stolen = await call(owner.browser, 'POST', '/api/notifications/read', { ids: [l.items[0].id] })
          expect([stolen.status, stolen.json.unread]).toEqual([200, 0])
          expect(await count(guest.browser)).toBe(2)
          const one = await call(guest.browser, 'POST', '/api/notifications/read', {
            ids: [l.items[0].id, 'no-such-id'],
          })
          expect([one.status, one.json.unread]).toEqual([200, 1])
          const after = await notes(guest.browser)
          expect(after.items.map((i: { read: boolean }) => i.read)).toEqual([true, false])
          expect((await notes(guest.browser, '?unread=1')).items).toHaveLength(1)
          const all = await call(guest.browser, 'POST', '/api/notifications/read-all', {})
          expect([all.status, all.json.unread]).toEqual([200, 0])
          expect(await count(guest.browser)).toBe(0)
        })

        it('is paged by a cursor, newest first, and filtered by kind', async () => {
          const owner = await newMember(uniq('mo3'))
          const guest = await newMember(uniq('mg3'))
          const spaces: SpaceInfo[] = []
          for (let i = 0; i < 3; i++) spaces.push(await ownedSpace(owner, `Paged ${i}`))
          for (const sp of spaces) await addMember(owner, sp.id, guest.user.username, 'viewer')
          const first = await notes(guest.browser, '?limit=2')
          expect(first.items).toHaveLength(2)
          expect(first.next).not.toBe('')
          const second = await notes(guest.browser, `?limit=2&cursor=${encodeURIComponent(first.next)}`)
          expect(second.items).toHaveLength(1)
          expect(second.next).toBe('')
          expect([...first.items, ...second.items].map((i: { ref: string }) => i.ref)).toEqual(
            [...spaces].reverse().map((s) => s.id),
          )
          expect((await notes(guest.browser, '?kind=invite')).items).toHaveLength(3)
          expect((await notes(guest.browser, '?kind=sync')).items).toEqual([])
          expect((await notes(guest.browser, '?kind=mention')).items).toEqual([])
        })

        it('refuses what it should, and a stranger has nothing', async () => {
          const m = await newMember(uniq('nr'))
          for (const q of ['?kind=bogus', '?limit=0', '?limit=101', '?limit=x']) {
            const r = await call(m.browser, 'GET', `/api/notifications${q}`)
            expect([q, r.status, r.json.error]).toEqual([q, 400, 'invalid'])
          }
          for (const body of [{}, { ids: [] }]) {
            const r = await call(m.browser, 'POST', '/api/notifications/read', body)
            expect([r.status, r.json.error]).toEqual([400, 'invalid'])
          }
          expect((await call(m.browser, 'POST', '/api/notifications/read-all', { kind: 'bogus' })).status).toBe(400)
          for (const [method, path] of [
            ['GET', '/api/notifications'],
            ['GET', '/api/notifications/unread'],
            ['POST', '/api/notifications/read-all'],
            ['GET', '/api/prefs'],
          ] as const) {
            const r = await call(browser(), method, path, method === 'GET' ? undefined : {})
            expect([path, r.status, r.json.error]).toEqual([path, 401, 'unauthenticated'])
          }
        })

        it('keeps each person’s preferences apart', async () => {
          const a = await newMember(uniq('pfa'))
          const b = await newMember(uniq('pfb'))
          await call(a.browser, 'PATCH', '/api/prefs', { set: { 'pb.table.users': { pageSize: 100 }, tour: 'done' } })
          const mine = (await call(a.browser, 'GET', '/api/prefs')).json
          expect(mine.personal).toBe(true)
          expect(mine.prefs).toEqual({ 'pb.table.users': { pageSize: 100 }, tour: 'done' })
          expect((await call(b.browser, 'GET', '/api/prefs')).json.prefs).toEqual({})
          // A write that would take the file past 100 keys is refused, and nothing of it is kept.
          const many = Object.fromEntries(Array.from({ length: 99 }, (_, i) => [`k.${i}`, i]))
          const full = await call(a.browser, 'PATCH', '/api/prefs', { set: many })
          expect([full.status, full.json.error]).toEqual([400, 'prefs_full'])
          expect(Object.keys((await call(a.browser, 'GET', '/api/prefs')).json.prefs)).toHaveLength(2)
          // A personal space that is switched off holds nothing: the answer says so.
          const own = (await call(b.browser, 'GET', '/api/spaces')).json.spaces.find(
            (x: { personal: boolean }) => x.personal,
          )
          await as(admin, () => adminApi.adminDisableSpace(own.id))
          try {
            const r = await call(b.browser, 'PATCH', '/api/prefs', { set: { k: 1 } })
            expect([r.status, r.json.personal]).toEqual([200, false])
            expect((await call(b.browser, 'GET', '/api/prefs')).json).toEqual({ personal: false, prefs: {} })
          } finally {
            await as(admin, () => adminApi.adminEnableSpace(own.id))
          }
        })
      })

      describe('administration', () => {
        const adminPaths: [string, string, unknown?][] = [
          ['GET', '/api/admin/spaces'],
          ['GET', '/api/admin/status'],
          ['GET', '/api/admin/security'],
          ['PUT', '/api/admin/security', {}],
          ['GET', '/api/admin/auth/providers'],
          ['POST', '/api/admin/auth/providers', {}],
          ['GET', '/api/admin/audit'],
          ['GET', '/api/admin/spaces/01ZZZZZZZZ'],
          ['POST', '/api/admin/spaces/01ZZZZZZZZ/size'],
          ['POST', '/api/admin/spaces/01ZZZZZZZZ/transfer', { userId: 'x' }],
          ['POST', '/api/admin/spaces/01ZZZZZZZZ/disable'],
          ['POST', '/api/admin/spaces/01ZZZZZZZZ/enable'],
          ['DELETE', '/api/admin/spaces/01ZZZZZZZZ'],
        ]
        const listAll = async (query = '', limit = 3) => {
          const items: AdminSpace[] = []
          let cursor = ''
          for (let guard = 0; guard < 500; guard++) {
            const r = await as(admin, () =>
              adminApi.adminSpaces({ q: query || undefined, cursor: cursor || undefined, limit }),
            )
            items.push(...r.items)
            cursor = r.next
            if (!cursor) return items
          }
          throw new Error('cursor never ended')
        }

        it('is for administrators only: a member gets 403 forbidden from every endpoint, and nothing changes', async () => {
          const m = await newMember(uniq('na'))
          const own = await ownedSpace(m, 'Mine')
          for (const [method, path, body] of adminPaths) {
            const r = await call(m.browser, method, path, body ?? (method === 'GET' ? undefined : {}))
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([403, 'forbidden'])
          }
          const real = await call(m.browser, 'POST', `/api/admin/spaces/${own.id}/disable`, {})
          expect(real.status).toBe(403)
          expect((await roleOf(m.browser, own.id))!.disabled).toBe(false)
        })

        it('an unknown space is 404 not_found', async () => {
          for (const [method, path, body] of adminPaths.filter(([, p]) => p.includes('01ZZZZZZZZ'))) {
            const r = await call(admin, method, path, body ?? (method === 'GET' ? undefined : {}))
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([404, 'not_found'])
          }
        })

        it('lists every space in a page at a time, with its owner, members, size, sync and whether it is disabled', async () => {
          const owner = await newMember(uniq('ls1'))
          const guest = await newMember(uniq('ls2'))
          const name = `Listed ${run}`
          const sp = await ownedSpace(owner, name)
          await addMember(owner, sp.id, guest.user.username, 'viewer')
          const found = await listAll(name.toLowerCase())
          expect(found.map((s) => s.id)).toEqual([sp.id])
          expect(found[0]).toMatchObject({
            id: sp.id,
            name,
            personal: false,
            disabled: false,
            members: 2,
            sync: 'local',
            owner: { id: owner.user.id, username: owner.user.username, name: owner.user.name },
          })
          expect(typeof found[0].created).toBe('string')
          // Not measured yet, or measured: both are shaped alike.
          expect(found[0].sizeBytes === null ? found[0].sizeAt : typeof found[0].sizeAt).toEqual(
            found[0].sizeBytes === null ? null : 'string',
          )
          const first = await as(admin, () => adminApi.adminSpaces({ limit: 1 }))
          expect(first.items.length).toBe(1)
          expect(typeof first.next).toBe('string')
          // Walking the pages meets each space once, the personal ones included.
          const all = await listAll('', 2)
          expect(new Set(all.map((s) => s.id)).size).toBe(all.length)
          expect(all.map((s) => s.id)).toContain(sp.id)
          expect(all.some((s) => s.personal)).toBe(true)
          expect((await listAll('no-such-space-' + run)).length).toBe(0)
          const bad = await call(admin, 'GET', '/api/admin/spaces?limit=0')
          expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
        })

        it('finds one space by its id: the same object as its item of the list, 404 for an unknown id', async () => {
          const owner = await newMember(uniq('g1'))
          const guest = await newMember(uniq('g2'))
          const name = `Single ${run}`
          const sp = await ownedSpace(owner, name)
          await addMember(owner, sp.id, guest.user.username, 'editor')
          const listed = (await listAll(name.toLowerCase())).find((s) => s.id === sp.id)
          expect(listed).toBeTruthy()
          expect(await as(admin, () => api.adminSpace(sp.id))).toEqual(listed)
          const missing = await call(admin, 'GET', '/api/admin/spaces/01ZZZZZZZZ')
          expect([missing.status, missing.json.error]).toEqual([404, 'not_found'])
        })

        it('measures a space on request and stores the size', async () => {
          const owner = await newMember(uniq('sz'))
          const sp = await ownedSpace(owner, `Sized ${run}`)
          await call(owner.browser, 'POST', `/api/spaces/${sp.id}/pages`, {
            meta: { title: '占地方' },
            body: '一些字\n',
          })
          const size = await as(admin, () => adminApi.adminResizeSpace(sp.id))
          expect(size.sizeBytes).toBeGreaterThan(0)
          expect(typeof size.sizeAt).toBe('string')
          const row = (await listAll(`sized ${run}`)).find((s) => s.id === sp.id)!
          expect(row.sizeBytes).toBe(size.sizeBytes)
          expect(row.sizeAt).toBe(size.sizeAt)
        })

        it('a member may leave a space that is switched off, though nothing else in it opens', async () => {
          const owner = await newMember(uniq('dfo'))
          const guest = await newMember(uniq('dfg'))
          const sp = await ownedSpace(owner, 'Left switched off')
          await addMember(owner, sp.id, guest.user.username, 'editor')
          expect((await call(admin, 'POST', `/api/admin/spaces/${sp.id}/disable`, {})).status).toBe(204)
          const members = await call(guest.browser, 'GET', `/api/spaces/${sp.id}/members`)
          expect([members.status, members.json.error]).toEqual([403, 'space_disabled'])
          expect((await call(guest.browser, 'DELETE', `/api/spaces/${sp.id}/members/${guest.user.id}`)).status).toBe(
            204,
          )
          expect(await roleOf(guest.browser, sp.id)).toBeUndefined()
          expect((await call(admin, 'POST', `/api/admin/spaces/${sp.id}/enable`, {})).status).toBe(204)
          const left = (await call(owner.browser, 'GET', `/api/spaces/${sp.id}/members`)).json.members as Member[]
          expect(left.map((m) => m.userId)).toEqual([owner.user.id])
        })

        it('the owner who must stay is one who can sign in: with the other owner disabled, the only enabled owner can neither be demoted nor leave', async () => {
          const owner = await newMember(uniq('efo'))
          const second = await newMember(uniq('efs'))
          const sp = await ownedSpace(owner, 'Enabled owners')
          await addMember(owner, sp.id, second.user.username, 'owner')
          const self = `/api/spaces/${sp.id}/members/${owner.user.id}`
          await as(admin, () => adminApi.updateUser(second.user.id, { disabled: true }))
          const demote = await call(owner.browser, 'PATCH', self, { role: 'editor' })
          expect([demote.status, demote.json.error]).toEqual([409, 'last_owner'])
          const leave = await call(owner.browser, 'DELETE', self)
          expect([leave.status, leave.json.error]).toEqual([409, 'last_owner'])
          // Enabled again, the other owner counts: the first may step down.
          await as(admin, () => adminApi.updateUser(second.user.id, { disabled: false }))
          expect((await call(owner.browser, 'PATCH', self, { role: 'editor' })).status).toBe(200)
        })

        it('transfers ownership: the user becomes the owner and the owners before become editors; a personal space, an unknown or a disabled user is refused', async () => {
          const owner = await newMember(uniq('to'))
          const heir = await newMember(uniq('th'))
          const gone = await newMember(uniq('tg'))
          const sp = await ownedSpace(owner, 'Handed over')
          const transfer = (userId: string, space = sp.id) =>
            call(admin, 'POST', `/api/admin/spaces/${space}/transfer`, { userId })
          const personal = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find(
            (s) => s.personal,
          )!
          const p = await transfer(heir.user.id, personal.id)
          expect([p.status, p.json.error]).toEqual([409, 'personal_space'])
          const unknown = await transfer('01ZZZZZZZZ')
          expect([unknown.status, unknown.json.error]).toEqual([404, 'user_not_found'])
          await as(admin, () => adminApi.updateUser(gone.user.id, { disabled: true }))
          const off = await transfer(gone.user.id)
          expect([off.status, off.json.error]).toEqual([409, 'user_disabled'])
          expect((await transfer(heir.user.id)).status).toBe(204)
          const list = (await call(heir.browser, 'GET', `/api/spaces/${sp.id}/members`)).json.members as Member[]
          expect(list.map((m) => [m.userId, m.role])).toEqual([
            [heir.user.id, 'owner'],
            [owner.user.id, 'editor'],
          ])
          expect(await roleOf(owner.browser, sp.id)).toMatchObject({ role: 'editor', permissions: ['read', 'write'] })
          expect((await listAll(sp.name.toLowerCase())).find((s) => s.id === sp.id)!.owner!.id).toBe(heir.user.id)
        })

        it("disables a space: it stays in its members' lists, marked, and nothing opens it for anyone, an administrator included; enabling undoes it", async () => {
          const owner = await newMember(uniq('do'))
          const sp = await ownedSpace(owner, 'Switched off')
          const page = await call(owner.browser, 'POST', `/api/spaces/${sp.id}/pages`, {
            meta: { title: '还在' },
            body: 'x\n',
          })
          expect(page.status).toBe(201)
          expect((await call(admin, 'POST', `/api/admin/spaces/${sp.id}/disable`, {})).status).toBe(204)
          expect(await roleOf(owner.browser, sp.id)).toMatchObject({ role: 'owner', disabled: true })
          expect(
            (await call(owner.browser, 'GET', '/api/meta')).json.spaces.find((s: SpaceInfo) => s.id === sp.id).disabled,
          ).toBe(true)
          for (const [b, method, path] of [
            [owner.browser, 'GET', `/api/spaces/${sp.id}/pages`],
            [owner.browser, 'GET', `/api/spaces/${sp.id}/tree`],
            [owner.browser, 'POST', `/api/spaces/${sp.id}/pages`],
            [admin, 'GET', `/api/spaces/${sp.id}/pages`],
          ] as [Browser, string, string][]) {
            const r = await call(b, method, path, method === 'GET' ? undefined : { body: 'x' })
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([403, 'space_disabled'])
          }
          // The administration itself still reaches it.
          expect((await listAll(sp.name.toLowerCase())).find((s) => s.id === sp.id)!.disabled).toBe(true)
          expect((await call(admin, 'POST', `/api/admin/spaces/${sp.id}/size`, {})).status).toBe(200)
          expect((await call(admin, 'POST', `/api/admin/spaces/${sp.id}/enable`, {})).status).toBe(204)
          expect((await roleOf(owner.browser, sp.id))!.disabled).toBe(false)
          const again = await call(owner.browser, 'GET', `/api/spaces/${sp.id}/pages/${page.json.id}`)
          expect(again.status).toBe(200)
          expect(again.json.meta.title).toBe('还在')
        })

        it('deletes a space (the files go to the trash, not away); a personal space cannot be deleted', async () => {
          const owner = await newMember(uniq('de'))
          const guest = await newMember(uniq('dg'))
          const sp = await ownedSpace(owner, 'To be deleted')
          await addMember(owner, sp.id, guest.user.username, 'viewer')
          const personal = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find(
            (s) => s.personal,
          )!
          const refused = await call(admin, 'DELETE', `/api/admin/spaces/${personal.id}`)
          expect([refused.status, refused.json.error]).toEqual([409, 'personal_space'])
          const done = await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`)
          expect(done.status).toBe(200)
          // Every deletion goes through the recycle bin: the answer is the item.
          expect(done.json).toMatchObject({
            kind: 'space',
            space: { id: sp.id, name: 'To be deleted' },
            restorable: true,
          })
          expect(Date.parse(done.json.purgeAt)).toBeGreaterThan(Date.parse(done.json.deletedAt))
          expect(
            ((await call(admin, 'GET', '/api/admin/recycle?limit=200')).json.items as { id: string }[]).some(
              (i) => i.id === done.json.id,
            ),
          ).toBe(true)
          for (const b of [owner.browser, guest.browser, admin])
            expect((await call(b, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
          expect(await roleOf(guest.browser, sp.id)).toBeUndefined()
          expect((await listAll('to be deleted')).some((s) => s.id === sp.id)).toBe(false)
          expect((await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`)).status).toBe(404)
        })

        it("reports the server's status", async () => {
          const st = await as(admin, () => adminApi.adminStatus())
          expect(typeof st.version).toBe('string')
          expect(Number.isNaN(Date.parse(st.startedAt))).toBe(false)
          expect(st.uptimeSeconds).toBeGreaterThanOrEqual(0)
          expect(['sqlite', 'postgres']).toContain(st.database)
          expect(st.spaces).toBeGreaterThanOrEqual(1)
          expect(st.users).toBeGreaterThanOrEqual(1)
          expect(st.openSpaces).toBeGreaterThanOrEqual(0)
          expect(typeof st.goVersion).toBe('string')
          if (st.diskFreeBytes !== undefined) expect(st.diskFreeBytes).toBeGreaterThanOrEqual(0)
        })
      })

      // The administrators' re-check and one-click App creation are site-only, on a real server and in the mock alike;
      // without an App there is nothing to check (what they do with one is below, against the mock's GitHub).
      describe('the App re-check and creation endpoints', () => {
        it('are for site administrators, and a re-check needs an App', async () => {
          const m = await newMember(uniq('ghc'))
          for (const [path, body] of [
            ['/api/admin/connections/github/check', {}],
            ['/api/admin/connections/github/manifest', {}],
          ] as const) {
            const r = await call(m.browser, 'POST', path, body)
            expect([r.status, r.json?.error], path).toEqual([403, 'forbidden'])
          }
          const none = await call(admin, 'POST', '/api/admin/connections/github/check', {})
          expect([none.status, none.json?.error]).toEqual([409, 'provider_not_configured'])
        })
      })

      // GitHub (docs/api.md, "GitHub"). There is no GitHub behind a real server in this setup: the mock plays it.
      describe.skipIf(!!REAL)('GitHub (mock only)', () => {
        const KEY =
          '-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\n-----END RSA PRIVATE KEY-----\n'
        const CLIENT = 'Iv23liMockClientId'
        const putApp = (b: Browser, body: unknown) => call(b, 'PUT', '/api/admin/connections/github', body)
        const mockHook = (body: unknown) => call(admin, 'POST', '/api/_mock/github', body)
        const codeOf = (r: { json: any }) => r.json?.error
        // The GitHub provider's entry in GET /api/connections.
        const ghProvider = async (b: Browser) =>
          ((await call(b, 'GET', '/api/connections')).json.providers as { id: string }[]).find(
            (p) => p.id === 'github',
          ) as any

        // Plays the browser through connecting a GitHub account.
        const connectAccount = async (b: Browser, ret = '/settings/connections') => {
          const start = await call(b, 'POST', '/api/connections/github/authorization/start', { return: ret })
          expect(start.status).toBe(200)
          const cb = await call(b, 'GET', start.json.url, undefined, {})
          return cb
        }

        it('starts with no App: everyone is told so, and every other endpoint answers 409 provider_not_configured', async () => {
          const m = await newMember(uniq('gh0'))
          const info = { json: await ghProvider(m.browser) }
          expect(info.json).toMatchObject({
            id: 'github',
            configured: false,
            app: null,
            userAuthorization: false,
            connection: null,
            leftovers: [],
          })
          const off = await call(admin, 'GET', '/api/admin/connections/github')
          expect(off.json).toMatchObject({ configured: false })
          expect(off.json.callbackUrl).toMatch(/\/api\/connections\/github\/authorization\/callback$/)
          for (const [method, path, body] of [
            ['POST', '/api/connections/github/authorization/start', {}],
            ['GET', '/api/connections/github/owners'],
            ['GET', '/api/connections/github/repos?mine=true'],
            ['GET', '/api/admin/connections/github/installations'],
            ['PUT', '/api/admin/connections/github/installations/5001', { tenants: [] }],
          ] as const) {
            const r = await call(
              method.startsWith('P') && path.startsWith('/api/admin')
                ? admin
                : path.startsWith('/api/admin')
                  ? admin
                  : m.browser,
              method,
              path,
              body,
            )
            expect([r.status, codeOf(r)], `${method} ${path}`).toEqual([409, 'provider_not_configured'])
          }
          const sp = await call(m.browser, 'POST', '/api/spaces', {
            name: 'Gh',
            storage: { sync: { provider: 'github', owner: 'kazuhahub', name: 'x' } },
          })
          expect([sp.status, codeOf(sp)]).toEqual([409, 'provider_not_configured'])
        })

        it('is for administrators to set up: a member gets 403, a refused check saves nothing, and no secret is ever returned', async () => {
          const m = await newMember(uniq('gh1'))
          for (const [method, path, body] of [
            ['GET', '/api/admin/connections/github'],
            ['PUT', '/api/admin/connections/github', {}],
            ['DELETE', '/api/admin/connections/github'],
            ['GET', '/api/admin/connections/github/installations'],
          ] as const) {
            const r = await call(m.browser, method, path, body)
            expect([r.status, codeOf(r)], `${method} ${path}`).toEqual([403, 'forbidden'])
          }
          expect(codeOf(await putApp(admin, { appId: '1234567' }))).toBe('invalid')
          expect(codeOf(await putApp(admin, { appId: '1234567', privateKey: 'nonsense' }))).toBe('key_invalid')
          expect(
            codeOf(await putApp(admin, { appId: '1234567', privateKey: KEY, clientId: CLIENT, clientSecret: 'wrong' })),
          ).toBe('client_rejected')
          expect(((await call(admin, 'GET', '/api/admin/connections/github')).json as any).configured).toBe(false)
          const saved = await putApp(admin, {
            appId: '1234567',
            privateKey: KEY,
            clientId: CLIENT,
            clientSecret: 'a-secret',
          })
          expect(saved.status).toBe(200)
          expect(saved.json).toMatchObject({
            configured: true,
            appId: '1234567',
            clientId: CLIENT,
            slug: 'petalbook-notes',
            privateKey: { set: true },
            clientSecret: { set: true },
            userAuthorization: true,
            missing: [],
            spaces: 0,
          })
          expect(saved.json.privateKey.fingerprint).toMatch(/^SHA256:/)
          expect(JSON.stringify(saved.json)).not.toContain('a-secret')
          expect(JSON.stringify(saved.json)).not.toContain('PRIVATE KEY')
          // Saving again with the key and the secret left out keeps them.
          const again = await putApp(admin, { appId: '1234567' })
          expect(again.json).toMatchObject({ privateKey: { set: true }, clientSecret: { set: true } })
          const seen = { json: await ghProvider(m.browser) }
          expect(seen.json).toMatchObject({
            app: {
              name: 'PetalBook Notes',
              slug: 'petalbook-notes',
              htmlUrl: expect.any(String),
              installUrl: expect.stringContaining('/installations/new'),
            },
            userAuthorization: true,
            connection: null,
          })
        })

        it('connects an account only from the browser that started it, once, and disconnects', async () => {
          const m = await newMember(uniq('gh2'))
          const other = await newMember(uniq('gh3'))
          const start = await call(m.browser, 'POST', '/api/connections/github/authorization/start', {
            return: '/settings/connections',
          })
          expect(start.json.url).toContain('/api/connections/github/authorization/callback')
          // Another browser, or no session: expired, nothing is connected.
          const stray = await call(other.browser, 'GET', start.json.url, undefined, {})
          expect([stray.status, stray.headers.get('Location')]).toEqual([
            303,
            '/settings?provider=github&connection_error=expired',
          ])
          const ok = await call(m.browser, 'GET', start.json.url, undefined, {})
          expect([ok.status, ok.headers.get('Location')]).toEqual([303, '/settings/connections'])
          const replay = await call(m.browser, 'GET', start.json.url, undefined, {})
          expect(replay.headers.get('Location')).toBe('/settings?provider=github&connection_error=expired')
          const info = { json: await ghProvider(m.browser) }
          expect(info.json.connection).toMatchObject({ state: 'ok', spaces: 0 })
          expect(typeof info.json.connection.login).toBe('string')
          expect((await call(m.browser, 'DELETE', '/api/connections/github/authorization')).status).toBe(204)
          expect((await ghProvider(m.browser)).connection).toBeNull()
          expect((await call(m.browser, 'DELETE', '/api/connections/github/authorization')).status).toBe(204)
        })

        it('lists the accounts to create in and the repositories to connect, a page at a time', async () => {
          const m = await newMember(uniq('gh4'))
          expect(codeOf(await call(m.browser, 'GET', '/api/connections/github/repos?mine=true'))).toBe('not_connected')
          await connectAccount(m.browser)
          const owners = await call(m.browser, 'GET', '/api/connections/github/owners')
          expect(owners.status).toBe(200)
          expect(owners.json.items[0]).toMatchObject({ type: 'User', via: 'user', installationId: null })
          const org = owners.json.items.find((o: any) => o.login === 'kazuhahub')
          expect(org).toMatchObject({ type: 'Organization', via: 'user', installationId: expect.any(Number) })
          expect(typeof owners.json.next).toBe('string')
          const repos = await call(m.browser, 'GET', `/api/connections/github/repos?installation=${org.installationId}`)
          expect(repos.status).toBe(200)
          expect(repos.json.items[0]).toMatchObject({
            id: expect.any(Number),
            fullName: expect.stringContaining('kazuhahub/'),
            private: expect.any(Boolean),
            defaultBranch: 'main',
            canPush: true,
          })
          expect(codeOf(await call(m.browser, 'GET', '/api/connections/github/repos'))).toBe('invalid')
          expect(
            codeOf(
              await call(
                m.browser,
                'GET',
                `/api/connections/github/repos?installation=${org.installationId}&mine=true`,
              ),
            ),
          ).toBe('invalid')
          expect((await call(m.browser, 'GET', '/api/connections/github/repos?installation=99')).status).toBe(404)
          expect(codeOf(await call(m.browser, 'GET', '/api/connections/github/owners?cursor=zzz'))).toBe('invalid')
        })

        it("shares an organisation's installation with a tenant, and a member without a GitHub account there can then use it", async () => {
          const list = await call(admin, 'GET', '/api/admin/connections/github/installations')
          expect(list.status).toBe(200)
          const item = list.json.items.find((i: any) => i.account.login === 'petal-team')
          expect(item).toMatchObject({
            account: { type: 'Organization' },
            repositorySelection: 'selected',
            suspended: false,
            tenants: [],
            spaces: 0,
          })
          expect(codeOf(await call(admin, 'PUT', `/api/admin/connections/github/installations/${item.id}`, {}))).toBe(
            'invalid',
          )
          expect(
            codeOf(
              await call(admin, 'PUT', `/api/admin/connections/github/installations/${item.id}`, { shared: true }),
            ),
          ).toBe('invalid')
          expect(
            codeOf(
              await call(admin, 'PUT', `/api/admin/connections/github/installations/${item.id}`, {
                tenants: ['01ZZZZZZZZ'],
              }),
            ),
          ).toBe('invalid')
          expect(
            (await call(admin, 'PUT', '/api/admin/connections/github/installations/424242', { tenants: [] })).status,
          ).toBe(404)
          const put = await call(admin, 'PUT', `/api/admin/connections/github/installations/${item.id}`, {
            tenants: ['0000000000'],
          })
          expect(put.json).toMatchObject({ id: item.id, tenants: ['0000000000'] })
          const m = await newMember(uniq('gh5'))
          const owners = await call(m.browser, 'GET', '/api/connections/github/owners')
          expect(owners.json.items).toEqual([
            { login: 'petal-team', type: 'Organization', via: 'app', installationId: item.id, needsInstall: false },
          ])
          const made = await call(m.browser, 'POST', '/api/spaces', {
            name: `Shared ${run}`,
            storage: {
              sync: {
                provider: 'github',
                owner: 'petal-team',
                name: `shared-${run}`,
                private: false,
                description: 'd',
              },
            },
          })
          expect(made.status).toBe(201)
          const remote = await call(m.browser, 'GET', `/api/spaces/${made.json.id}/remote`)
          expect(remote.json).toMatchObject({
            state: 'ok',
            errorCode: '',
            credential: {
              type: 'connection',
              provider: 'github',
              via: 'app',
              installationId: item.id,
              account: 'petal-team',
            },
          })
          // The App cannot be removed while a space syncs through it.
          expect(codeOf(await call(admin, 'DELETE', '/api/admin/connections/github'))).toBe('provider_in_use')
          await call(m.browser, 'DELETE', `/api/spaces/${made.json.id}/remote`)
          expect(
            (await call(admin, 'PUT', `/api/admin/connections/github/installations/${item.id}`, { tenants: [] })).json
              .tenants,
          ).toEqual([])
        })

        it('creates a space with a new repository, or connects it to one that is there, and says why it could not', async () => {
          const m = await newMember(uniq('gh6'))
          await connectAccount(m.browser)
          const body = (extra: Record<string, unknown>) => ({
            name: `Repo ${run}`,
            storage: { sync: { provider: 'github', owner: 'kazuhahub', name: `notes-${run}`, ...extra } },
          })
          const made = await call(m.browser, 'POST', '/api/spaces', body({ private: true, description: 'Notes' }))
          expect(made.status).toBe(201)
          expect(made.headers.get('Location')).toBe(`/api/spaces/${made.json.id}`)
          expect(made.json.sync).toBe('ok')
          const remote = await call(m.browser, 'GET', `/api/spaces/${made.json.id}/remote`)
          expect(remote.json).toMatchObject({
            url: `https://github.com/kazuhahub/notes-${run}.git`,
            branch: 'main',
            state: 'ok',
            errorCode: '',
            credential: {
              type: 'connection',
              provider: 'github',
              via: 'app',
              account: 'kazuhahub',
              repoId: expect.any(Number),
            },
          })
          expect(JSON.stringify(remote.json)).not.toMatch(/token|secret/i)
          // The same name again: taken; create: false uses it.
          expect(codeOf(await call(m.browser, 'POST', '/api/spaces', body({})))).toBe('name_taken')
          expect((await call(m.browser, 'POST', '/api/spaces', body({ create: false }))).status).toBe(201)
          expect(
            codeOf(
              await call(m.browser, 'POST', '/api/spaces', {
                name: 'x',
                storage: { sync: { provider: 'github', owner: 'nobody-here', name: 'x' } },
              }),
            ),
          ).toBe('owner_not_allowed')
          expect(codeOf(await call(m.browser, 'POST', '/api/spaces', body({ name: 'bad name' })))).toBe('invalid')
          expect((await mockHook({ trap: 'noPermission' })).status).toBe(200)
          const refused = await call(m.browser, 'POST', '/api/spaces', body({ name: `other-${run}` }))
          expect([refused.status, codeOf(refused)]).toEqual([403, 'provider_refused'])
          await mockHook({ trap: 'noPermission', on: false })
          const spaces = (await call(m.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]
          expect(spaces.filter((x) => x.name === `Repo ${run}`).length).toBe(2)
        })

        it('reminds a person of an empty repository a failed space left, until a space connects to it or they dismiss it', async () => {
          const m = await newMember(uniq('gh6b'))
          const other = await newMember(uniq('gh6c'))
          await connectAccount(m.browser)
          expect((await ghProvider(m.browser)).leftovers).toEqual([])
          const body = (name: string, create = true) => ({
            name: 'Kept',
            storage: { sync: { provider: 'github', owner: 'kazuhahub', name, create } },
          })
          await mockHook({ trap: 'pushFails' })
          const failed = await call(m.browser, 'POST', '/api/spaces', body(`left-${run}`))
          expect([failed.status, codeOf(failed)]).toEqual([502, 'push_failed'])
          const second = await call(m.browser, 'POST', '/api/spaces', body(`left2-${run}`))
          await mockHook({ trap: 'pushFails', on: false })
          const list = (await ghProvider(m.browser)).leftovers
          expect(list).toHaveLength(2)
          expect(list[1]).toMatchObject({
            repoId: failed.json.repo.id,
            fullName: `kazuhahub/left-${run}`,
            htmlUrl: failed.json.repo.htmlUrl,
            spaceName: 'Kept',
            created: expect.stringMatching(/Z$/),
          })
          // Newest first, and only the person's own.
          expect(list[0].repoId).toBe(second.json.repo.id)
          expect((await ghProvider(other.browser)).leftovers).toEqual([])
          // A connection that is refused (a branch name that cannot be used) leaves the reminder.
          const sp = await ownedSpace(m, 'Refused')
          const refused = await call(m.browser, 'PUT', `/api/spaces/${sp.id}/remote`, {
            url: `https://github.com/kazuhahub/left2-${run}.git`,
            branch: 'bad branch',
            credential: { type: 'connection', provider: 'github', via: 'user' },
          })
          expect([refused.status, codeOf(refused)]).toEqual([400, 'invalid'])
          expect((await ghProvider(m.browser)).leftovers).toHaveLength(2)
          // A space connecting to the repository (here by create: false) answers its reminder.
          expect((await call(m.browser, 'POST', '/api/spaces', body(`left-${run}`, false))).status).toBe(201)
          expect((await ghProvider(m.browser)).leftovers.map((l: any) => l.repoId)).toEqual([second.json.repo.id])
          // Dismissing is the person's own, and fine when it is gone already.
          expect(
            (await call(other.browser, 'DELETE', `/api/connections/github/leftovers/${second.json.repo.id}`)).status,
          ).toBe(204)
          expect((await ghProvider(m.browser)).leftovers).toHaveLength(1)
          expect(
            (await call(m.browser, 'DELETE', `/api/connections/github/leftovers/${second.json.repo.id}`)).status,
          ).toBe(204)
          expect(
            (await call(m.browser, 'DELETE', `/api/connections/github/leftovers/${second.json.repo.id}`)).status,
          ).toBe(204)
          expect((await ghProvider(m.browser)).leftovers).toEqual([])
          expect((await call(m.browser, 'DELETE', '/api/connections/github/leftovers/nope')).status).toBe(404)
        })

        it("switches a space to the App or to the user's connection with PUT .../remote", async () => {
          const m = await newMember(uniq('gh7'))
          await connectAccount(m.browser)
          const sp = await ownedSpace(m, 'Switch')
          const put = (credential: unknown, url = 'https://github.com/kazuhahub/wiki.git') =>
            call(m.browser, 'PUT', `/api/spaces/${sp.id}/remote`, { url, credential })
          expect(codeOf(await put({ type: 'connection', provider: 'github' }))).toBe('invalid')
          expect(
            codeOf(await put({ type: 'connection', provider: 'github', via: 'app' }, 'https://example.com/a/b.git')),
          ).toBe('invalid')
          const app = await put(
            { type: 'connection', provider: 'github', via: 'app' },
            'git@github.com:kazuhahub/wiki.git',
          )
          expect(app.status).toBe(200)
          expect(app.json).toMatchObject({
            url: 'https://github.com/kazuhahub/wiki.git',
            credential: { type: 'connection', provider: 'github', via: 'app' },
          })
          const mine = { json: await ghProvider(m.browser) }
          expect(mine.json.connection.spaces).toBe(0)
          const user = await put(
            { type: 'connection', provider: 'github', via: 'user' },
            'https://github.com/kazuhahub/notes.git',
          )
          expect(user.json).toMatchObject({
            credential: {
              type: 'connection',
              provider: 'github',
              via: 'user',
              user: { id: m.user.id, username: m.user.username },
              login: expect.any(String),
            },
          })
          expect((await ghProvider(m.browser)).connection.spaces).toBe(1)
          expect([
            (await put({ type: 'connection', provider: 'github', via: 'app' }, 'https://github.com/none/x.git')).status,
          ]).toEqual([409])
          // An expired connection puts the space in error with a code that says why; connecting again and syncing fixes it.
          await mockHook({ expire: true, user: m.user.id })
          const bad = await call(m.browser, 'GET', `/api/spaces/${sp.id}/remote`)
          expect(bad.json).toMatchObject({ state: 'error', errorCode: 'connection_expired' })
          expect((await ghProvider(m.browser)).connection.state).toBe('expired')
          await connectAccount(m.browser)
          const fixed = await call(m.browser, 'POST', `/api/spaces/${sp.id}/remote/sync`)
          expect(fixed.json).toMatchObject({ state: 'ok', errorCode: '' })
        })

        it('connects through the installation page in one flow, and tells the personal account that needs the App', async () => {
          const m = await newMember(uniq('gh10'))
          const start = await call(m.browser, 'POST', '/api/connections/github/authorization/start', {
            return: '/settings/connections',
            install: true,
          })
          expect(start.status).toBe(200)
          expect(start.json.url).toContain('setup_action=install')
          const cb = await call(m.browser, 'GET', start.json.url, undefined, {})
          expect([cb.status, cb.headers.get('Location')]).toEqual([303, '/settings/connections'])
          expect((await ghProvider(m.browser)).connection).toMatchObject({ state: 'ok' })
          // An organisation that has to approve the installation first.
          const asked = await call(m.browser, 'POST', '/api/connections/github/authorization/start', {
            return: '/settings/connections',
            install: true,
          })
          const pending = await call(
            m.browser,
            'GET',
            asked.json.url.replace('setup_action=install', 'setup_action=request'),
            undefined,
            {},
          )
          expect(pending.headers.get('Location')).toBe('/settings?provider=github&connection_error=requested')
          // The own account of someone the App is not installed on: marked, and refused with the way out.
          const other = await newMember(uniq('gh11'))
          await connectAccount(other.browser)
          await mockHook({ trap: 'personalNeedsInstall' })
          try {
            const owners = await call(other.browser, 'GET', '/api/connections/github/owners')
            expect(owners.json.items[0]).toMatchObject({ type: 'User', needsInstall: true })
            expect(owners.json.items.filter((o: any) => o.needsInstall)).toHaveLength(1)
            const login = owners.json.items[0].login
            const refused = await call(other.browser, 'POST', '/api/spaces', {
              name: 'Own',
              storage: { sync: { provider: 'github', owner: login, name: `own-${run}` } },
            })
            expect([refused.status, codeOf(refused)]).toEqual([409, 'app_not_installed'])
            expect(refused.json.installUrl).toContain('/installations/new')
          } finally {
            await mockHook({ trap: 'personalNeedsInstall', on: false })
          }
          expect((await call(m.browser, 'GET', '/api/connections/github/owners')).json.items[0].needsInstall).toBe(
            false,
          )
        })

        it('names the permission an App lacks, and the re-check shows what GitHub says now', async () => {
          const m = await newMember(uniq('gh12'))
          await connectAccount(m.browser)
          await mockHook({ trap: 'missingPermission' })
          try {
            const r = await call(m.browser, 'POST', '/api/spaces', {
              name: 'Perm',
              storage: { sync: { provider: 'github', owner: 'kazuhahub', name: `perm-${run}` } },
            })
            expect([r.status, codeOf(r)]).toEqual([403, 'app_missing_permission'])
            expect(r.json.missing).toEqual(['administration'])
          } finally {
            await mockHook({ trap: 'missingPermission', on: false })
          }
          // GitHub's side changes; the view keeps the last check until it is asked again.
          const before = (await call(admin, 'GET', '/api/admin/connections/github')).json
          expect(before).toMatchObject({ missing: [], gaps: [], anyAccount: true })
          await mockHook({
            side: {
              permissions: { administration: 'write', metadata: 'read' },
              gaps: [{ installationId: 5001, account: 'kazuhahub', missing: ['contents'] }],
              anyAccount: false,
            },
          })
          expect((await call(admin, 'GET', '/api/admin/connections/github')).json).toMatchObject({
            missing: [],
            gaps: [],
            anyAccount: true,
          })
          const checked = await call(admin, 'POST', '/api/admin/connections/github/check', {})
          expect(checked.status).toBe(200)
          expect(checked.json).toMatchObject({
            missing: ['contents'],
            gaps: [{ installationId: 5001, account: 'kazuhahub', missing: ['contents'] }],
            anyAccount: false,
          })
          expect((await call(admin, 'GET', '/api/admin/connections/github')).json.missing).toEqual(['contents'])
          await mockHook({
            side: {
              permissions: { administration: 'write', contents: 'write', metadata: 'read' },
              gaps: [],
              anyAccount: true,
            },
          })
          expect((await call(admin, 'POST', '/api/admin/connections/github/check', {})).json).toMatchObject({
            missing: [],
            gaps: [],
            anyAccount: true,
          })
        })

        it('checks the organisation and name before making the App, and does not replace one that is in use', async () => {
          const m = await newMember(uniq('gh13'))
          await connectAccount(m.browser)
          expect(
            codeOf(await call(admin, 'POST', '/api/admin/connections/github/manifest', { organization: 'a/b' })),
          ).toBe('invalid')
          expect(
            codeOf(await call(admin, 'POST', '/api/admin/connections/github/manifest', { name: 'x'.repeat(35) })),
          ).toBe('invalid')
          const used = await call(admin, 'POST', '/api/admin/connections/github/manifest', {
            organization: 'kazuhahub',
          })
          expect([used.status, codeOf(used)]).toEqual([409, 'provider_in_use'])
        })

        it("puts other remotes' error states under errorCode too", async () => {
          // A member of their own, so the administrator's spaces stay under the default quota of 20 for the rest of the run.
          const m = await newMember(uniq('gh9'))
          const sp = await ownedSpace(m, 'Plain')
          await call(m.browser, 'PUT', `/api/spaces/${sp.id}/remote`, {
            url: 'https://github.com/a/b.git',
            credential: { type: 'token', token: 'x' },
          })
          const r = await call(m.browser, 'GET', `/api/spaces/${sp.id}/remote`)
          expect(r.json.errorCode).toBe('')
        })
      })

      describe("server storage (the site's policy, the pin and the exemption, the requirement of a remote)", () => {
        const settings = () => call(admin, 'GET', '/api/admin/storage')
        // The policy is the site's: whatever a test changes is put back, also on a real server that is used again.
        const reset = () =>
          call(admin, 'PUT', '/api/admin/storage', {
            remotes: { require: null, scope: 'new', graceDays: 30 },
            localSpaces: { maxBytes: null },
            cache: { evictAfterDays: 14, minIdleHours: 24, clonesBytes: null, minFreeBytes: 10 * 1024 ** 3 },
          })
        afterAll(async () => {
          await reset()
        })

        it('has the settings an administrator reads and writes, partly, and refuses a value outside its range', async () => {
          await reset()
          const g = await settings()
          expect(g.status).toBe(200)
          expect(g.json).toMatchObject({
            remotes: { require: null, scope: 'new', graceDays: 30, since: null },
            hosts: { allowed: expect.any(Array), fromFlag: expect.any(Boolean) },
            localSpaces: { maxBytes: null },
            cache: {
              partial: expect.any(Boolean),
              evictAfterDays: 14,
              minIdleHours: 24,
              clonesBytes: null,
              minFreeBytes: 10 * 1024 ** 3,
            },
            git: { version: expect.any(String), partialClone: expect.any(Boolean) },
            affected: { inGrace: expect.any(Number), overdue: expect.any(Number), exempt: expect.any(Number) },
          })
          const put = await call(admin, 'PUT', '/api/admin/storage', {
            cache: { minIdleHours: 48 },
            localSpaces: { maxBytes: 5 * 1024 ** 2 },
          })
          expect(put.status).toBe(200)
          expect(put.json.cache).toMatchObject({ minIdleHours: 48, evictAfterDays: 14 })
          expect(put.json.localSpaces.maxBytes).toBe(5 * 1024 ** 2)
          // The summary is the GET's alone.
          const { summary: _summary, ...again } = (await settings()).json
          void _summary
          expect(again).toEqual(put.json)
          for (const bad of [
            { remotes: { graceDays: 400 } },
            { remotes: { require: 'gitlab' } },
            { remotes: { scope: 'most' } },
            { localSpaces: { maxBytes: 1000 } },
            { cache: { evictAfterDays: 0 } },
            { cache: { minIdleHours: 0 } },
            { cache: { clonesBytes: 1024 ** 2 } },
          ]) {
            const r = await call(admin, 'PUT', '/api/admin/storage', bad)
            expect([r.status, r.json.error], JSON.stringify(bad)).toEqual([400, 'invalid'])
          }
        })

        it('has the attachment cache among the settings', async () => {
          await reset()
          const g = await settings()
          expect(g.json).toMatchObject({ cache: { assetsBytes: expect.any(Number) } })
          const put = await call(admin, 'PUT', '/api/admin/storage', { cache: { assetsBytes: 64 * 1024 ** 2 } })
          expect(put.status).toBe(200)
          expect(put.json).toMatchObject({ cache: { assetsBytes: 64 * 1024 ** 2 } })
          for (const bad of [{ cache: { assetsBytes: 1024 ** 2 } }]) {
            const r = await call(admin, 'PUT', '/api/admin/storage', bad)
            expect([r.status, r.json.error], JSON.stringify(bad)).toEqual([400, 'invalid'])
          }
          await call(admin, 'PUT', '/api/admin/storage', { cache: { assetsBytes: 5 * 1024 ** 3 } })
        })

        it('has an attachment repository only for a space that follows a remote', async () => {
          const sp = await ownedSpace({ browser: admin }, 'Plain')
          const base = `/api/spaces/${sp.id}/remote/assets`
          expect((await call(admin, 'GET', base)).status).toBe(404)
          const put = await call(admin, 'PUT', base, {
            url: 'https://github.com/a/b-assets.git',
            credential: { type: 'token', token: 'x' },
          })
          expect([put.status, put.json.error]).toEqual([409, 'remote_required'])
          expect((await call(admin, 'POST', base + '/move')).status).toBe(404)
          expect((await call(admin, 'DELETE', base)).status).toBe(404)
          // The administrator's spaces count against their quota (3.3), which the later tests need.
          expect((await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`)).status).toBe(200)
        })

        it.skipIf(!!REAL)(
          'mock only: connects one, moves the attachments with progress, and cannot take it away after',
          async () => {
            const sp = await ownedSpace({ browser: admin }, 'Followed')
            await call(admin, 'PUT', `/api/spaces/${sp.id}/remote`, {
              url: 'https://github.com/a/b.git',
              credential: { type: 'token', token: 'x' },
            })
            const base = `/api/spaces/${sp.id}/remote/assets`
            const put = await call(admin, 'PUT', base, {
              url: 'https://github.com/a/b-assets.git',
              credential: { type: 'token', token: 'x' },
            })
            expect(put.status).toBe(200)
            expect(put.json).toMatchObject({ url: 'https://github.com/a/b-assets.git', state: 'ok', move: null })
            const listed = (await call(admin, 'GET', '/api/spaces')).json.spaces.find((x: SpaceInfo) => x.id === sp.id)
            expect(listed.storage.assets).toBe('repository')
            // Nothing in assets/: nothing to move.
            expect((await call(admin, 'POST', base + '/move')).status).toBe(200)
            const page = (await call(admin, 'POST', `/api/spaces/${sp.id}/pages`, { title: 'P' })).json
            await spaceApi(sp.id).uploadAsset(page.id, new Blob(['PNG']), 'a.png')
            const mv = await call(admin, 'POST', base + '/move')
            expect(mv.status).toBe(202)
            expect(mv.json.move).toMatchObject({ state: 'copying', files: 1, filesDone: 0, error: '' })
            expect((await call(admin, 'POST', base + '/move')).status).toBe(409)
            let state = 'copying'
            for (let i = 0; i < 6 && state !== 'done'; i++) state = (await call(admin, 'GET', base)).json.move.state
            expect(state).toBe('done')
            const del = await call(admin, 'DELETE', base)
            expect([del.status, del.json.error]).toEqual([409, 'conflict'])
            expect((await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`)).status).toBe(200)
          },
        )

        it('is for administrators only', async () => {
          const m = await newMember(uniq('stad'))
          for (const [method, path, body] of [
            ['GET', '/api/admin/storage', undefined],
            ['PUT', '/api/admin/storage', {}],
            ['PUT', '/api/admin/spaces/01ZZZZZZZZ/storage', {}],
            ['POST', '/api/admin/spaces/01ZZZZZZZZ/evict', {}],
          ] as [string, string, unknown][]) {
            const r = await call(m.browser, method, path, body)
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([403, 'forbidden'])
          }
        })

        it("a space says what its copy is; a local space is never evicted; pinning and the exemption are an administrator's", async () => {
          const owner = await newMember(uniq('stsp'))
          const sp = await ownedSpace(owner, `Stored ${run}`)
          const item = (await call(admin, 'GET', `/api/admin/spaces/${sp.id}`)).json as AdminSpace
          expect(item.storage).toMatchObject({
            cache: 'local',
            pinned: false,
            exempt: false,
            remote: null,
            assets: 'space',
          })
          const mine = (await call(owner.browser, 'GET', '/api/spaces')).json.spaces.find(
            (s: SpaceInfo) => s.id === sp.id,
          )
          expect(mine.storage).toMatchObject({ cache: 'local', remote: null })
          const evict = await call(admin, 'POST', `/api/admin/spaces/${sp.id}/evict`)
          expect([evict.status, evict.json.error]).toEqual([409, 'conflict'])
          const pinned = await call(admin, 'PUT', `/api/admin/spaces/${sp.id}/storage`, { pin: true, exempt: true })
          expect(pinned.status).toBe(200)
          expect(pinned.json.storage).toMatchObject({ pinned: true, exempt: true })
          expect(
            (await call(admin, 'PUT', `/api/admin/spaces/${sp.id}/storage`, { pin: false, exempt: false })).json
              .storage,
          ).toMatchObject({ pinned: false, exempt: false })
          const filtered = (await call(admin, 'GET', '/api/admin/spaces?cache=evicted&limit=200')).json
            .items as AdminSpace[]
          expect(filtered.every((s) => s.storage.cache === 'evicted')).toBe(true)
          const status = (await call(admin, 'GET', '/api/admin/status')).json
          expect(status.storage).toMatchObject({
            spaces: {
              local: expect.any(Number),
              full: expect.any(Number),
              partial: expect.any(Number),
              evicted: expect.any(Number),
              restoring: expect.any(Number),
            },
            remotes: { inGrace: expect.any(Number), overdue: expect.any(Number) },
          })
        })

        it('requires a remote of new spaces before anything is made, and relaxing it takes the requirement away', async () => {
          const owner = await newMember(uniq('strq'))
          const before = (await call(owner.browser, 'GET', '/api/spaces')).json.spaces.length
          await call(admin, 'PUT', '/api/admin/storage', { remotes: { require: 'any', scope: 'new', graceDays: 30 } })
          const refused = await call(owner.browser, 'POST', '/api/spaces', { name: `Needs a remote ${run}` })
          expect([refused.status, refused.json.error, refused.json.require]).toEqual([409, 'remote_required', 'any'])
          expect((await call(owner.browser, 'GET', '/api/spaces')).json.spaces.length).toBe(before)
          await call(admin, 'PUT', '/api/admin/storage', { remotes: { require: null } })
          expect((await call(owner.browser, 'POST', '/api/spaces', { name: `Free ${run}` })).status).toBe(201)
        })

        it('exempts a user: the spaces they own are not asked for a remote', async () => {
          const owner = await newMember(uniq('stex'))
          const r = await call(admin, 'PATCH', `/api/users/${owner.user.id}`, { remoteExempt: true })
          expect(r.json.user.remoteExempt).toBe(true)
          await call(admin, 'PUT', '/api/admin/storage', { remotes: { require: 'any', scope: 'new', graceDays: 30 } })
          expect((await call(owner.browser, 'POST', '/api/spaces', { name: `Exempt ${run}` })).status).toBe(201)
          await call(admin, 'PUT', '/api/admin/storage', { remotes: { require: null } })
          const back = await call(admin, 'PATCH', `/api/users/${owner.user.id}`, { remoteExempt: false })
          expect(back.json.user.remoteExempt).toBeUndefined()
        })
      })

      // Storage management (3.5): the lists, the previews, the recycle bin, the confirmation and its jobs, exports.
      describe('storage management (3.5)', () => {
        const waitFor = async <T>(read: () => Promise<T>, done: (v: T) => boolean, what: string): Promise<T> => {
          for (let i = 0; i < 120; i++) {
            const v = await read()
            if (done(v)) return v
            await new Promise((r) => setTimeout(r, REAL ? 250 : 5))
          }
          throw new Error(`never happened: ${what}`)
        }
        const LOCATIONS = ['server', 'own', 'tenant', 'site']

        it('summarises what the server holds, from stored values', async () => {
          const r = await call(admin, 'GET', '/api/admin/storage')
          expect(r.status).toBe(200)
          const s = r.json.summary
          expect(s.spaces).toEqual(
            expect.objectContaining({
              local: expect.any(Number),
              full: expect.any(Number),
              partial: expect.any(Number),
              evicted: expect.any(Number),
            }),
          )
          for (const k of ['people', 'dormant', 'heldBytes', 'usageBytes', 'assetCacheBytes'])
            expect(typeof s[k], k).toBe('number')
          expect(s.recycle).toEqual({ items: expect.any(Number), bytes: expect.any(Number) })
          expect(s.exports).toEqual({ items: expect.any(Number), bytes: expect.any(Number) })
          expect(s.remotes).toEqual({ inGrace: expect.any(Number), overdue: expect.any(Number) })
          expect(s.recycleDays).toBeGreaterThanOrEqual(1)
        })

        it('lists spaces with where their bytes live and whether they count, sorted, with a cursor that is for one sort', async () => {
          const owner = await newMember(uniq('sl'))
          for (const name of [`Bb ${run}`, `Aa ${run}`, `Cc ${run}`]) await ownedSpace(owner, name)
          const first = await call(
            admin,
            'GET',
            `/api/admin/spaces?q=${encodeURIComponent(run)}&sort=name&order=desc&limit=2`,
          )
          expect(first.status).toBe(200)
          const names = (first.json.items as AdminSpace[]).map((s) => s.name)
          expect(names).toEqual([...names].sort().reverse())
          for (const s of first.json.items as AdminSpace[]) {
            expect(LOCATIONS).toContain(s.location)
            expect(typeof s.counted).toBe('boolean')
          }
          expect(first.json.next).toBeTruthy()
          const other = await call(
            admin,
            'GET',
            `/api/admin/spaces?q=${encodeURIComponent(run)}&sort=bytes&limit=2&cursor=${encodeURIComponent(first.json.next)}`,
          )
          expect([other.status, other.json.error]).toEqual([400, 'invalid'])
          for (const bad of ['sort=colour', 'order=sideways', 'remote=maybe', 'deadline=soon'])
            expect((await call(admin, 'GET', `/api/admin/spaces?${bad}`)).status, bad).toBe(400)
        })

        it('lists people with what they hold, and one person with their spaces', async () => {
          const owner = await newMember(uniq('pl'))
          await ownedSpace(owner, `Held ${run}`)
          const list = await call(
            admin,
            'GET',
            `/api/admin/storage/people?q=${encodeURIComponent(owner.user.username)}&sort=held`,
          )
          expect(list.status).toBe(200)
          const row = (
            list.json.items as { user: { id: string }; spaces: number; usageBytes: number; heldBytes: number }[]
          ).find((p) => p.user.id === owner.user.id)!
          expect(row.spaces).toBeGreaterThanOrEqual(2)
          expect(typeof row.usageBytes).toBe('number')
          expect(typeof row.heldBytes).toBe('number')
          const one = await call(admin, 'GET', `/api/admin/storage/people/${owner.user.id}`)
          expect(one.status).toBe(200)
          expect((one.json.items as AdminSpace[]).map((s) => s.name)).toContain(`Held ${run}`)
          expect((await call(admin, 'GET', '/api/admin/storage/people?sort=colour')).status).toBe(400)
          expect((await call(admin, 'GET', '/api/admin/storage/people/01ZZZZZZZZ')).status).toBe(404)
        })

        it('says what deleting a space would do, deletes it into the bin, restores it, and keeps its people', async () => {
          const owner = await newMember(uniq('bin'))
          const guest = await newMember(uniq('bing'))
          const sp = await ownedSpace(owner, `Binned ${run}`)
          await addMember(owner, sp.id, guest.user.username, 'viewer')
          const preview = await call(admin, 'GET', `/api/admin/spaces/${sp.id}/deletion`)
          expect(preview.status).toBe(200)
          expect(preview.json).toMatchObject({ space: { id: sp.id }, members: 2, recycleDays: expect.any(Number) })
          const del = await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`, { reason: 'contract test' })
          expect(del.status).toBe(200)
          expect(del.json.reason).toBe('contract test')
          expect(del.json.state).toBe('ready')
          const item = del.json.id as string
          const listedItem = (
            (await call(admin, 'GET', '/api/admin/recycle?limit=200')).json.items as { id: string; state: string }[]
          ).find((i) => i.id === item)
          expect(listedItem?.state).toBe('ready')
          expect((await call(owner.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
          const back = await call(admin, 'POST', `/api/admin/recycle/${item}/restore`, {})
          expect(back.status).toBe(200)
          expect(back.json.restored.space.id).toBe(sp.id)
          expect((await call(owner.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(200)
          expect((await roleOf(guest.browser, sp.id))?.role).toBe('viewer')
        })

        it('deletes a person: an heir is needed for a shared space, the person goes to the bin, and comes back whole', async () => {
          const leaver = await newMember(uniq('dpl'))
          const viewer = await newMember(uniq('dpv'))
          const heir = await newMember(uniq('dph'))
          const shared = await ownedSpace(leaver, `Shared ${run}`)
          await addMember(leaver, shared.id, viewer.user.username, 'viewer')
          const plan = await call(admin, 'GET', `/api/admin/users/${leaver.user.id}/deletion`)
          expect(plan.status).toBe(200)
          expect(plan.json.recycleDays).toBeGreaterThanOrEqual(1)
          expect(plan.json.needsHeir.map((s: { id: string }) => s.id)).toEqual([shared.id])
          expect(plan.json.recycle.some((r: { space: { personal?: boolean } }) => r.space.personal)).toBe(true)
          const refused = await call(admin, 'DELETE', `/api/users/${leaver.user.id}`, {})
          expect([refused.status, refused.json.error]).toEqual([409, 'heir_required'])
          expect(refused.json.spaces.map((s: { id: string }) => s.id)).toEqual([shared.id])
          const done = await call(admin, 'DELETE', `/api/users/${leaver.user.id}`, {
            heir: heir.user.id,
            reason: 'contract test',
          })
          expect(done.status).toBe(200)
          expect(done.json).toMatchObject({ kind: 'user', user: { id: leaver.user.id } })
          const users = (await call(admin, 'GET', `/api/users?q=${leaver.user.username}`)).json.users as User[]
          expect(users.some((u) => u.id === leaver.user.id)).toBe(false)
          expect((await roleOf(heir.browser, shared.id))?.role).toBe('owner')
          const back = await call(admin, 'POST', `/api/admin/recycle/${done.json.id}/restore`, {})
          expect(back.status).toBe(200)
          expect(back.json.restored.user.id).toBe(leaver.user.id)
          expect(back.json.transfers.map((t: { space: string }) => t.space)).toContain(shared.id)
          for (const k of back.json.skipped as { name: string; reason: string }[])
            expect(k).toEqual(expect.objectContaining({ name: expect.any(String), reason: expect.any(String) }))
          expect(
            ((await call(admin, 'GET', `/api/users?q=${leaver.user.username}`)).json.users as User[]).some(
              (u) => u.id === leaver.user.id,
            ),
          ).toBe(true)
        })

        it('confirms a bulk action with a token that is for those ids, once, and refuses it when the plan changed', async () => {
          const owner = await newMember(uniq('bk'))
          const a = await ownedSpace(owner, `Bulk A ${run}`)
          const b = await ownedSpace(owner, `Bulk B ${run}`)
          const outsider = await newMember(uniq('bko'))
          const confirm = await call(admin, 'POST', '/api/admin/confirm', {
            action: 'disable_spaces',
            spaces: [a.id, b.id],
          })
          expect(confirm.status).toBe(200)
          expect(confirm.json.count).toBe(2)
          expect(typeof confirm.json.confirm).toBe('string')
          const started = await call(admin, 'POST', '/api/admin/bulk', { confirm: confirm.json.confirm })
          expect(started.status).toBe(202)
          const job = await waitFor(
            async () => (await call(admin, 'GET', `/api/admin/jobs/${started.json.job.id}`)).json.job,
            (j) => j.state === 'done',
            'the job',
          )
          expect([job.total, job.done, job.failed]).toEqual([2, 2, []])
          expect((await roleOf(owner.browser, a.id))?.disabled).toBe(true)
          // A token is spent by its use.
          expect((await call(admin, 'POST', '/api/admin/bulk', { confirm: confirm.json.confirm })).status).toBe(404)
          // The plan changes (a member is added) between the confirmation and the action.
          const again = await call(admin, 'POST', '/api/admin/confirm', {
            action: 'enable_spaces',
            spaces: [a.id, b.id],
          })
          await call(admin, 'POST', `/api/admin/spaces/${b.id}/enable`, {})
          await addMember(owner, b.id, outsider.user.username, 'viewer')
          const stale = await call(admin, 'POST', '/api/admin/bulk', { confirm: again.json.confirm })
          expect([stale.status, stale.json.error]).toEqual([409, 'confirm_stale'])
          for (const bad of [
            { action: 'nonsense', spaces: [a.id] },
            { action: 'disable_spaces', spaces: [] },
            { action: 'disable_spaces', spaces: ['01ZZZZZZZZ'] },
          ]) {
            expect([400, 404], JSON.stringify(bad)).toContain(
              (await call(admin, 'POST', '/api/admin/confirm', bad)).status,
            )
          }
          await call(admin, 'POST', `/api/admin/spaces/${a.id}/enable`, {})
        })

        it('purges early only with a confirmation made for that item', async () => {
          const owner = await newMember(uniq('pg'))
          const sp = await ownedSpace(owner, `Purge ${run}`)
          const item = (await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`, {})).json.id as string
          expect([400, 404, 409]).toContain((await call(admin, 'DELETE', `/api/admin/recycle/${item}`)).status)
          const other = await call(admin, 'POST', '/api/admin/confirm', { action: 'restore', items: [item] })
          expect([400, 404, 409]).toContain(
            (await call(admin, 'DELETE', `/api/admin/recycle/${item}?confirm=${other.json.confirm}`)).status,
          )
          const mine = await call(admin, 'POST', '/api/admin/confirm', { action: 'purge', items: [item] })
          const purged = await call(admin, 'DELETE', `/api/admin/recycle/${item}?confirm=${mine.json.confirm}`)
          expect(purged.status).toBe(202)
          await waitFor(
            async () => (await call(admin, 'GET', '/api/admin/recycle?limit=200')).json.items as { id: string }[],
            (l) => !l.some((i) => i.id === item),
            'the purge',
          )
        })

        it('exports a space to a zip that is listed, downloaded and deleted', async () => {
          const owner = await newMember(uniq('ex'))
          const sp = await ownedSpace(owner, `Exported ${run}`)
          const made = await call(admin, 'POST', '/api/admin/exports', { spaces: [sp.id] })
          expect(made.status).toBe(202)
          const ready = await waitFor(
            async () => (await call(admin, 'GET', `/api/admin/exports/${made.json.id}`)).json,
            (e) => e.state === 'ready' || e.state === 'failed',
            'the export',
          )
          expect(ready.state).toBe('ready')
          expect(Date.parse(ready.expires)).toBeGreaterThan(Date.now())
          expect(ready.spaces).toEqual([{ space: sp.id, name: `Exported ${run}`, spaceId: sp.id, open: true }])
          expect(
            ((await call(admin, 'GET', '/api/admin/exports')).json.items as { id: string }[]).some(
              (e) => e.id === made.json.id,
            ),
          ).toBe(true)
          const dl = await call(admin, 'GET', `/api/admin/exports/${made.json.id}/download`)
          expect(dl.status).toBe(200)
          expect(dl.headers.get('Content-Disposition')).toMatch(/attachment/)
          expect((await call(admin, 'DELETE', `/api/admin/exports/${made.json.id}`)).status).toBe(204)
          expect((await call(admin, 'GET', `/api/admin/exports/${made.json.id}`)).status).toBe(404)
        })

        it('a member reaches none of it', async () => {
          const m = await newMember(uniq('sm'))
          for (const [method, path] of [
            ['GET', '/api/admin/storage/people'],
            ['GET', '/api/admin/recycle'],
            ['GET', '/api/admin/exports'],
            ['POST', '/api/admin/confirm'],
            ['GET', '/api/admin/storage'],
          ] as const) {
            const r = await call(m.browser, method, path, method === 'GET' ? undefined : {})
            expect([r.status, r.json?.error], `${method} ${path}`).toEqual([403, 'forbidden'])
          }
        })
      })
    },
  )

  describe('notifications and preferences (3.10, part 4)', () => {
    it('the list, the count and the marks answer in both modes, with nothing to say yet', async () => {
      const l = (await call(admin, 'GET', '/api/notifications')).json
      expect(Array.isArray(l.items)).toBe(true)
      expect(typeof l.next).toBe('string')
      expect(typeof l.unread).toBe('number')
      const c = (await call(admin, 'GET', '/api/notifications/unread')).json
      expect(Object.keys(c)).toEqual(['count'])
      expect(c.count).toBeGreaterThanOrEqual(0)
      // Marking all as read leaves nothing unread (the problems of the space are read as they are now).
      const all = await call(admin, 'POST', '/api/notifications/read-all', {})
      expect([all.status, all.json.unread]).toEqual([200, 0])
      expect((await call(admin, 'GET', '/api/notifications/unread')).json.count).toBe(0)
      // An id nobody has changes nothing.
      const none = await call(admin, 'POST', '/api/notifications/read', { ids: ['0000000ZZZ'] })
      expect([none.status, none.json.unread]).toEqual([200, 0])
      expect((await call(admin, 'GET', '/api/notifications?kind=bogus')).status).toBe(400)
    })

    it('the person’s own preferences: read back at once, a null takes a key away, bad names and sizes are refused', async () => {
      const get = async () => (await call(admin, 'GET', '/api/prefs')).json
      const first = await get()
      expect(typeof first.personal).toBe('boolean')
      const key = `contract.${run}`
      const set = await call(admin, 'PATCH', '/api/prefs', { set: { [key]: { widths: { name: 0.3 }, hidden: ['a'] } } })
      expect([set.status, typeof set.json.personal]).toEqual([200, 'boolean'])
      if (first.personal) expect((await get()).prefs[key]).toEqual({ widths: { name: 0.3 }, hidden: ['a'] })
      await call(admin, 'PATCH', '/api/prefs', { set: { [key]: null } })
      expect((await get()).prefs[key]).toBeUndefined()
      for (const body of [{}, { set: {} }, { set: 'x' }, { set: { 'has space': 1 } }, { set: { '../x': 1 } }]) {
        const r = await call(admin, 'PATCH', '/api/prefs', body)
        expect([JSON.stringify(body), r.status, r.json.error]).toEqual([JSON.stringify(body), 400, 'invalid'])
      }
    })
  })

  describe('the daily text and favourites', () => {
    const enc = encodeURIComponent
    const base = () => `/api/spaces/${spaceId}`
    const folder = `${root}/home`
    const daily = (query: string) => call(admin, 'GET', `${base()}/daily?${query}`)
    const setDaily = (value: unknown) => call(admin, 'PUT', `${base()}/daily/settings`, { daily: value })
    const favs = async () =>
      (await call(admin, 'GET', `${base()}/favorites`)).json as { items: Record<string, any>[]; limit: number }
    const favLine = (items: Record<string, any>[]) =>
      items.map((i) => `${i.kind}:${i.name}${i.missing ? '!' : ''}`).join(' ')
    const DAYS = Array.from({ length: 30 }, (_, i) => `2026-11-${String(i + 1).padStart(2, '0')}`)
    const pageIds: Record<string, string> = {}
    afterAll(async () => {
      globalThis.fetch = admin.fetch
      await setDaily(null).catch(() => undefined)
    })

    it('has no source until one is set: the pick is {"page": null} with a reason, the settings are null', async () => {
      await setDaily(null)
      const r = await daily('date=2026-11-23&lang=en')
      expect([r.status, r.json.page]).toEqual([200, null])
      // Another source may be there from the server's own settings; with none, this says so.
      expect(['no_source', undefined]).toContain(r.json.quote === undefined ? r.json.reason : undefined)
      expect((await call(admin, 'GET', `${base()}/daily/settings`)).json).toEqual({ daily: null, inEffect: true })
    })

    it('picks one page of a folder, the same for a whole day, another by skip, and cuts a long body at 2000 characters', async () => {
      for (const [name, status] of [
        ['甲', '定稿'],
        ['乙', '草稿'],
        ['丙', '定稿'],
        ['丁', '草稿'],
        ['戊', '定稿'],
      ]) {
        pageIds[name] = (
          await mk(`${folder}/poems`, { title: `${name}${run}`, status, tags: [`t${run}`] }, `《${name}》｜正文\n`)
        ).id
      }
      pageIds.long = (
        await mk(
          `${folder}/long`,
          { title: `长${run}`, tags: [`long${run}`] },
          '长'.repeat(1999) + 'AB' + 'x'.repeat(500) + '\n',
        )
      ).id
      const set = await setDaily({ folder: `${folder}/poems` })
      expect(set.status).toBe(200)
      expect(set.json).toEqual({ daily: { folder: `${folder}/poems`, filter: [] }, inEffect: true })
      expect((await call(admin, 'GET', `${base()}/daily/settings`)).json).toEqual(set.json)
      const first = await daily('date=2026-11-23&skip=0')
      expect(first.status).toBe(200)
      const page = first.json.page
      expect(page).toMatchObject({ folder: `${folder}/poems`, bodyTruncated: false })
      expect(Object.values(pageIds)).toContain(page.id)
      expect(page.body).toMatch(/^《.》｜正文/)
      expect(first.json.count).toBe(5)
      for (let i = 0; i < 3; i++) expect((await daily('date=2026-11-23')).json.page.id).toBe(page.id)
      const seen = new Set<string>()
      for (const d of DAYS) seen.add((await daily(`date=${d}`)).json.page.id)
      expect(seen.size).toBeGreaterThanOrEqual(3)
      const next = (await daily('date=2026-11-23&skip=1')).json.page.id
      expect(next).not.toBe(page.id)
      expect((await daily('date=2026-11-23&skip=5')).json.page.id).toBe(page.id)
      // The long body.
      expect((await setDaily({ tag: `long${run}` })).status).toBe(200)
      const long = (await daily('date=2026-11-23')).json.page
      expect(long.id).toBe(pageIds.long)
      expect(long.bodyTruncated).toBe(true)
      expect(Array.from(long.body as string)).toHaveLength(2000)
      expect(long.body.endsWith('长A')).toBe(true)
    })

    it('filters, a tag, a type and no match; a source that meets no page says no_match', async () => {
      const done = new Set([pageIds['甲'], pageIds['丙'], pageIds['戊']])
      const f = await setDaily({ folder: `${folder}/poems`, filter: [{ key: 'status', op: 'in', values: ['定稿'] }] })
      expect(f.json.daily.filter).toEqual([{ key: 'status', op: 'in', values: ['定稿'] }])
      for (const d of DAYS.slice(0, 10)) {
        const r = await daily(`date=${d}`)
        expect(done.has(r.json.page.id)).toBe(true)
        expect(r.json.count).toBe(3)
      }
      expect((await setDaily({ tag: `t${run}` })).status).toBe(200)
      expect((await daily('date=2026-11-23')).json.count).toBe(5)
      const none = await setDaily({ tag: `nothing${run}` })
      expect(none.status).toBe(200)
      expect((await daily('date=2026-11-23')).json).toEqual({ page: null, reason: 'no_match' })
      // A type: the header's, and the folder's.
      const type = `daily${run}`
      expect(
        (
          await S.putType(type, {
            title: '文案',
            fields: [{ key: 'status', kind: 'select', options: ['草稿', '定稿'] }],
          })
        ).name,
      ).toBe(type)
      await S.bindFolder(`${folder}/poems`, type)
      const own = await mk(`${folder}/elsewhere`, { title: `自带${run}`, type }, 'x\n')
      expect((await setDaily({ type })).status).toBe(200)
      const typed = await daily('date=2026-11-23')
      expect(typed.json.count).toBe(6)
      expect(typed.json.page.effectiveType).toBe(type)
      expect(own.id).toBeTruthy()
      await S.bindFolder(`${folder}/poems`, '')
    })

    it('a source by language: the language, its region, and a language nobody wrote one for', async () => {
      const zh = await mk(`${folder}/zh`, { title: `中文${run}` }, 'x\n')
      const en = await mk(`${folder}/en`, { title: `English${run}` }, 'x\n')
      const set = await setDaily({ zh: { folder: `${folder}/zh` }, 'en-US': { folder: `${folder}/en` } })
      expect(set.status).toBe(200)
      expect(set.json.daily).toEqual({
        'zh-Hans': { folder: `${folder}/zh`, filter: [] },
        'en-US': { folder: `${folder}/en`, filter: [] },
      })
      const title = async (lang: string) => (await daily(`date=2026-11-23&lang=${lang}`)).json.page.id
      expect([await title('zh'), await title('zh-CN'), await title('en-US'), await title('en')]).toEqual([
        zh.id,
        zh.id,
        en.id,
        en.id,
      ])
      // A language with no source of its own gets none: not the source of another language.
      for (const lang of ['fr', 'de', '']) expect((await daily(`date=2026-11-23&lang=${lang}`)).json.page).toBeNull()
      // The same source for every language.
      expect((await setDaily({ folder: `${folder}/zh` })).status).toBe(200)
      expect(await title('en')).toBe(zh.id)
    })

    it('refuses a source that is malformed, a folder that is not there, and a date, skip or language that cannot be', async () => {
      const bad: [string, unknown, number][] = [
        ['two sources', { folder: `${folder}/poems`, tag: 'x' }, 400],
        ['none', {}, 400],
        ['an empty tag', { tag: '' }, 400],
        ['no such type', { type: `nothing${run}` }, 400],
        ['an unknown key', { folder: `${folder}/poems`, recursive: true }, 400],
        [
          'a filter that is none',
          { folder: `${folder}/poems`, filter: [{ key: 'a', op: 'like', values: ['x'] }] },
          400,
        ],
        [
          '11 filters',
          {
            folder: `${folder}/poems`,
            filter: Array.from({ length: 11 }, (_, i) => ({ key: `k${i}`, op: 'notEmpty', values: [] })),
          },
          400,
        ],
        ['a bad language', { 'chinese!': { tag: 'x' } }, 400],
        ['a language that is none', { cn: { tag: 'x' } }, 400],
        ['a language source that is none', { zh: 'folder' }, 400],
        ['a folder that is not there', { folder: `${root}/no-such-folder` }, 404],
      ]
      for (const [what, value, status] of bad) {
        const r = await setDaily(value)
        expect([what, r.status]).toEqual([what, status])
      }
      expect((await call(admin, 'PUT', `${base()}/daily/settings`, {})).status).toBe(400)
      expect((await call(admin, 'PUT', `${base()}/daily/settings`, { daily: {}, extra: 1 })).status).toBe(400)
      expect((await setDaily({ folder: `${folder}/poems` })).status).toBe(200)
      for (const q of [
        'date=tomorrow',
        'date=2026-13-01',
        'date=1969-12-31',
        'date=2026-11-23&skip=-1',
        'date=2026-11-23&skip=x',
      ]) {
        const r = await daily(q)
        expect([q, r.status, r.json.error]).toEqual([q, 400, 'invalid'])
      }
      for (const q of [
        'date=2026-11-23&lang=%E4%B8%AD%E6%96%87',
        'date=2026-11-23&lang=x',
        'date=2026-11-23&lang=cn',
        'date=2026-11-23&lang=english',
        'date=2026-11-23&lang=zh_CN',
      ]) {
        const r = await daily(q)
        expect([q, r.status, r.json.error]).toEqual([q, 400, 'bad_language'])
      }
      const far = await daily('date=2026-11-23&skip=99999999')
      expect([far.status, far.json.error]).toEqual([400, 'skip_too_large'])
      expect((await daily('')).status).toBe(200) // today
      expect((await setDaily({ folder: `${folder}/poems` })).status).toBe(200)
    })

    it('a folder that is moved takes the source with it, and taking the source off puts the block back to nothing', async () => {
      expect((await setDaily({ zh: { folder: `${folder}/zh` }, en: { folder: `${folder}/poems` } })).status).toBe(200)
      const moved = await call(admin, 'POST', `${base()}/folders/move`, {
        from: `${folder}/poems`,
        to: `${folder}/verse`,
      })
      expect(moved.status).toBe(200)
      expect((await call(admin, 'GET', `${base()}/daily/settings`)).json.daily).toEqual({
        'zh-Hans': { folder: `${folder}/zh`, filter: [] },
        en: { folder: `${folder}/verse`, filter: [] },
      })
      expect((await daily('date=2026-11-23&lang=en')).json.page.folder).toBe(`${folder}/verse`)
      expect((await setDaily(null)).json).toEqual({ daily: null, inEffect: true })
      expect((await setDaily(null)).json).toEqual({ daily: null, inEffect: true })
      expect((await daily('date=2026-11-23&lang=en')).json.page).toBeNull()
    })

    it('a source for every language and some languages together: the language wins, the fallback is for the rest', async () => {
      const zh = await mk(`${folder}/mixzh`, { title: `中${run}` }, 'x\n')
      const any = await mk(`${folder}/mixall`, { title: `全${run}` }, 'x\n')
      const set = await setDaily({ folder: `${folder}/mixall`, zh: { folder: `${folder}/mixzh` } })
      expect(set.status).toBe(200)
      expect(set.json.daily).toEqual({
        folder: `${folder}/mixall`,
        filter: [],
        'zh-Hans': { folder: `${folder}/mixzh`, filter: [] },
      })
      expect((await call(admin, 'GET', `${base()}/daily/settings`)).json).toEqual(set.json)
      const id = async (lang: string) => (await daily(`date=2026-11-23&lang=${lang}`)).json.page.id
      expect([await id('zh'), await id('zh-TW'), await id('en'), await id('')]).toEqual([zh.id, zh.id, any.id, any.id])
      // A move carries both.
      expect(
        (await call(admin, 'POST', `${base()}/folders/move`, { from: `${folder}/mixall`, to: `${folder}/mixmoved` }))
          .status,
      ).toBe(200)
      expect((await call(admin, 'GET', `${base()}/daily/settings`)).json.daily.folder).toBe(`${folder}/mixmoved`)
      // Without all languages, only the ones that have a source are given a text: another language gets none.
      expect((await setDaily({ zh: { folder: `${folder}/mixzh` } })).json.daily).toEqual({
        'zh-Hans': { folder: `${folder}/mixzh`, filter: [] },
      })
      expect((await daily('date=2026-11-23&lang=fr')).json.page).toBeNull()
    })

    it('tells a browser to ask again each time, with a validator', async () => {
      expect((await setDaily({ folder: `${folder}/mixzh` })).status).toBe(200)
      const first = await daily('date=2026-11-23')
      expect(first.headers.get('Cache-Control')).toBe('private, no-cache')
      const etag = first.headers.get('ETag') ?? ''
      expect(etag).toMatch(/^"[0-9a-f]{8,}"$/)
      expect((await daily('date=2100-01-01&tz=-480')).headers.get('Cache-Control')).toBe('private, no-cache')
      for (const tz of ['9999', 'x', '-841'])
        expect([tz, (await daily(`date=2026-11-23&tz=${tz}`)).status]).toEqual([tz, 400])
      // A change of what the administrators chose is seen at once: the old validator no longer matches.
      expect((await setDaily({ tag: `nothing${run}` })).status).toBe(200)
      const after = await daily('date=2026-11-23')
      expect(after.headers.get('Cache-Control')).toBe('private, no-cache')
      expect(after.headers.get('ETag')).not.toBe(etag)
      await setDaily(null)
    })

    it("keeps a person's favourites: pages, folders and views, in order, with their names, each once", async () => {
      const a = (await mk(`${folder}/fav`, { title: `暮雪${run}` }, 'x\n')).id
      const b = (await mk(`${folder}/fav`, { title: `春晓${run}` }, 'x\n')).id
      await S.createFolder(`${folder}/fav/sub`)
      const views = (await call(admin, 'GET', `${base()}/views?folder=${enc(`${folder}/fav`)}`)).json
      const saved = await call(admin, 'PUT', `${base()}/views`, {
        folder: `${folder}/fav`,
        baseRev: views.rev,
        views: [{ name: '按作者', layout: 'table' }],
      })
      const viewT = `folder:${folder}/fav#${saved.json.views[0].id}`
      const post = (kind: string, target: string) => call(admin, 'POST', `${base()}/favorites`, { kind, target })
      expect((await favs()).items.filter((i) => i.target === a)).toEqual([])
      const r1 = await post('page', a)
      expect(r1.status).toBe(200)
      expect(r1.json.limit).toBe(200)
      for (const [k, t] of [
        ['folder', `${folder}/fav/sub`],
        ['view', viewT],
        ['page', b],
      ])
        expect((await post(k, t)).status).toBe(200)
      const again = await post('page', a)
      const mine = again.json.items.filter((i: any) => [a, b, `${folder}/fav/sub`, viewT].includes(i.target))
      expect(favLine(mine)).toBe(`page:暮雪${run} folder:sub view:按作者 page:春晓${run}`)
      expect(mine[0]).toMatchObject({ kind: 'page', target: a, path: `${folder}/fav/暮雪${run}.md`, missing: false })
      expect(mine[1]).toMatchObject({ path: `${folder}/fav/sub`, missing: false })
      expect(mine[2]).toMatchObject({ of: `${folder}/fav`, ofKind: 'folder', target: viewT, missing: false })
      expect(typeof mine[0].created).toBe('string')
      // The order: those named first, the rest after; names that are no favourite are ignored.
      const order = (items: [string, string][]) =>
        call(admin, 'PUT', `${base()}/favorites/order`, { items: items.map(([kind, target]) => ({ kind, target })) })
      const full = (await favs()).items.map((i) => [i.kind, i.target] as [string, string])
      const named: [string, string][] = [
        ['page', b],
        ['view', viewT],
        ['page', '01JD3K8Q7Z'],
        ['page', a],
      ]
      const ordered = await order(named)
      expect(ordered.status).toBe(200)
      const mine2 = ordered.json.items.filter((i: any) => [a, b, `${folder}/fav/sub`, viewT].includes(i.target))
      expect(favLine(mine2)).toBe(`page:春晓${run} view:按作者 page:暮雪${run} folder:sub`)
      expect(full.length).toBe(ordered.json.items.length)
      expect((await favs()).items.map((i) => i.target)).toEqual(ordered.json.items.map((i: any) => i.target))
      // What cannot be a favourite.
      const refused: [string, string, number][] = [
        ['page', '01JD3K8Q7Z', 404],
        ['page', '../x', 400],
        ['folder', `${root}/no-such`, 404],
        ['folder', '', 400],
        ['view', `folder:${folder}/fav#nope`, 404],
        ['view', 'fav', 400],
        ['star', 'x', 400],
      ]
      for (const [k, t, status] of refused) expect([k, t, (await post(k, t)).status]).toEqual([k, t, status])
      expect((await call(admin, 'POST', `${base()}/favorites`, { kind: 'page' })).status).toBe(400)
      expect((await call(admin, 'PUT', `${base()}/favorites/order`, {})).status).toBe(400)
      expect(
        (await call(admin, 'PUT', `${base()}/favorites/order`, { items: [{ kind: 'star', target: 'x' }] })).status,
      ).toBe(400)
      // Gone: shown as missing, and taken off; taking off what is not there is fine.
      expect((await call(admin, 'DELETE', `${SP_PAGES}/${b}`)).status).toBe(204)
      expect((await call(admin, 'DELETE', `${base()}/folders?path=${enc(`${folder}/fav/sub`)}`)).status).toBe(204)
      const gone = (await favs()).items.filter((i) => [a, b, `${folder}/fav/sub`, viewT].includes(i.target))
      expect(favLine(gone)).toBe(`page:! view:按作者 page:暮雪${run} folder:sub!`)
      expect(gone.find((i) => i.target === b)!.missing).toBe(true)
      const del = (kind: string, target: string) =>
        call(admin, 'DELETE', `${base()}/favorites?kind=${kind}&target=${enc(target)}`)
      for (const [k, t] of [
        ['page', b],
        ['folder', `${folder}/fav/sub`],
        ['view', viewT],
        ['page', a],
      ])
        expect([k, (await del(k, t)).status]).toEqual([k, 200])
      expect((await del('page', a)).status).toBe(200)
      expect((await favs()).items.filter((i) => [a, b, viewT].includes(i.target))).toEqual([])
      for (const q of ['', '?kind=page', '?target=x', '?kind=star&target=x'])
        expect([q, (await call(admin, 'DELETE', `${base()}/favorites${q}`)).status]).toEqual([q, 400])
    })

    it('favourites follow a folder that is moved, with its views, and nothing else', async () => {
      await S.createFolder(`${folder}/mv/inner`)
      const views = (await call(admin, 'GET', `${base()}/views?folder=${enc(`${folder}/mv/inner`)}`)).json
      const saved = await call(admin, 'PUT', `${base()}/views`, {
        folder: `${folder}/mv/inner`,
        baseRev: views.rev,
        views: [{ name: 'wide', layout: 'cards' }],
      })
      const vid = saved.json.views[0].id
      const post = (kind: string, target: string) => call(admin, 'POST', `${base()}/favorites`, { kind, target })
      await S.createFolder(`${folder}/mvx`)
      for (const [k, t] of [
        ['folder', `${folder}/mv`],
        ['folder', `${folder}/mv/inner`],
        ['folder', `${folder}/mvx`],
        ['view', `folder:${folder}/mv/inner#${vid}`],
      ])
        expect((await post(k, t)).status).toBe(200)
      expect(
        (await call(admin, 'POST', `${base()}/folders/move`, { from: `${folder}/mv`, to: `${folder}/moved` })).status,
      ).toBe(200)
      const mine = (await favs()).items.filter((i) => i.kind !== 'page')
      const line = mine.filter(
        (i) =>
          [`${folder}/moved`, `${folder}/moved/inner`, `${folder}/mvx`].includes(i.target) ||
          i.target.endsWith(`#${vid}`),
      )
      expect(line.map((i) => i.target).sort()).toEqual(
        [`${folder}/moved`, `${folder}/moved/inner`, `${folder}/mvx`, `folder:${folder}/moved/inner#${vid}`].sort(),
      )
      expect(line.every((i) => !i.missing)).toBe(true)
      for (const i of line) await call(admin, 'DELETE', `${base()}/favorites?kind=${i.kind}&target=${enc(i.target)}`)
    })

    if (accounts) {
      it('lines keep their author and source, split off the text and shown beforehand; sources of several kinds are one default', async () => {
        const adm = (method: string, path: string, body?: unknown) =>
          call(admin, method, `/api/admin/daily${path}`, body)
        try {
          const pre = await adm('POST', '/lines/preview', {
            text: '人到情多情转薄，而今真个不多情。 —— 纳兰性德《山花子》\nThe best way out is always through. — Robert Frost\nI think — therefore I am',
            format: 'lines',
          })
          expect(pre.status).toBe(200)
          expect(pre.json.lines).toEqual([
            { text: '人到情多情转薄，而今真个不多情。', author: '纳兰性德', source: '山花子' },
            { text: 'The best way out is always through.', author: 'Robert Frost' },
            { text: 'I think — therefore I am' },
          ])
          // A dash inside a sentence is not a mark: the text before it does not end one, and there is no 《source》.
          const dashes = await adm('POST', '/lines/preview', {
            text: '春眠不觉晓 —— 处处闻啼鸟\nNever give up — Never\nWar — Peace and Love\n春眠不觉晓 —— 孟浩然《春晓》',
            format: 'lines',
          })
          expect(dashes.json.lines).toEqual([
            { text: '春眠不觉晓 —— 处处闻啼鸟' },
            { text: 'Never give up — Never' },
            { text: 'War — Peace and Love' },
            { text: '春眠不觉晓', author: '孟浩然', source: '春晓' },
          ])
          const csv = await adm('POST', '/lines/preview', {
            text: 'text,author,source\n"One, two",Seneca,Letters\n',
            format: 'csv',
          })
          expect(csv.json.lines).toEqual([{ text: 'One, two', author: 'Seneca', source: 'Letters' }])
          expect((await adm('POST', '/lines/preview', { text: 'x', format: 'xml' })).status).toBe(400)
          expect(
            (
              await adm('PUT', '/lines', {
                lang: '',
                lines: [{ text: 'He said — no', author: 'Someone', source: 'Somewhere' }],
              })
            ).status,
          ).toBe(200)
          expect((await adm('GET', '/lines?lang=')).json.lines).toEqual([
            { text: 'He said — no', author: 'Someone', source: 'Somewhere' },
          ])
          const mixed = await adm('PUT', '', { default: { kind: 'sources', sources: { '': [{ kind: 'list' }] } } })
          expect(mixed.status).toBe(200)
          expect(mixed.json.default).toEqual({ kind: 'sources', sources: { '': [{ kind: 'list' }] } })
          // Options belong to a source, not to the default.
          expect(
            (
              await adm('PUT', '', {
                default: { kind: 'sources', sources: { '': [{ kind: 'list' }] }, options: { x: { y: ['z'] } } },
              })
            ).status,
          ).toBe(400)
          // A single id is a list of one; a kind that is none is refused.
          expect((await adm('PUT', '', { default: { kind: 'api', apis: { '': 'nothing' } } })).status).toBe(400)
          expect(
            (await adm('PUT', '', { default: { kind: 'sources', sources: { '': [{ kind: 'rss' }] } } })).status,
          ).toBe(400)
        } finally {
          await adm('DELETE', '/lines?lang=')
          await adm('PUT', '', { default: null })
        }
      })

      it('the administrators give a default for spaces with no source: lines by language, a choice per space, and the settings are theirs alone', async () => {
        const adm = (method: string, path: string, body?: unknown) =>
          call(admin, method, `/api/admin/daily${path}`, body)
        const cleanup = async () => {
          await adm('DELETE', '/lines?lang=zh')
          await adm('DELETE', '/lines?lang=en')
          await adm('DELETE', '/lines?lang=')
          await adm('PUT', '', { default: null })
        }
        try {
          const empty = await adm('GET', '')
          expect(empty.status).toBe(200)
          expect(empty.json.hosts).toBeUndefined()
          expect(empty.json.notices).toBeUndefined()
          expect(empty.json).toMatchObject({
            providers: expect.any(Array),
            knownLanguages: expect.any(Array),
            apis: expect.any(Array),
            lines: { total: expect.any(Number), max: 5000 },
          })
          // Bad requests.
          for (const [what, method, path, body] of [
            ['an unknown key', 'PUT', '', { extra: 1 }],
            ['hosts, which is no setting', 'PUT', '', { hosts: [] }],
            ['notices, which are none', 'PUT', '', { notices: [] }],
            ['a default with no kind', 'PUT', '', { default: {} }],
            ['a single id where a list goes', 'PUT', '', { default: { kind: 'sources', sources: { '': 'x' } } }],
            ['a bad language', 'PUT', '/lines', { lang: 'chinese!', text: 'x' }],
            ['no text', 'PUT', '/lines', { lang: 'en', text: ' \n ' }],
            ['a bad format', 'PUT', '/lines', { lang: 'en', text: 'x', format: 'html' }],
          ] as const)
            expect([what, (await adm(method, path, body)).status]).toEqual([what, 400])
          expect((await call(admin, 'DELETE', '/api/admin/daily/lines?lang=chinese!')).status).toBe(400)
          const set = await adm('PUT', '', { default: { kind: 'list' } })
          expect(set.status).toBe(200)
          expect(set.json.default).toEqual({ kind: 'list' })
          const put = (lang: string, text: string, format = 'lines') => adm('PUT', '/lines', { lang, text, format })
          expect((await put('zh', '山色空蒙雨亦奇\t苏轼\n\n行到水穷处\n')).status).toBe(200)
          expect((await put('en', 'Stay hungry\tJobs\nStay foolish\nBe kind')).json.lines.byLang).toMatchObject({
            'zh-Hans': 2,
            en: 3,
          })
          const any = await put('', 'For any language')
          expect(any.json.lines.byLang['']).toBe(1)
          expect(any.json.lines.total).toBeGreaterThanOrEqual(6)
          // A new space has no source, so it shows the default; the same text for everybody, all day.
          const sp = (await call(admin, 'POST', '/api/spaces', { name: `Home ${run}` })).json.id as string
          const dq = (query: string) => call(admin, 'GET', `/api/spaces/${sp}/daily?${query}`)
          const zh = new Set(['山色空蒙雨亦奇', '行到水穷处'])
          const seen = new Set<string>()
          for (const d of DAYS.slice(0, 12)) {
            const r = await dq(`date=${d}&lang=zh`)
            expect(r.json.page).toBeNull()
            expect(zh.has(r.json.quote.text)).toBe(true)
            expect(r.json.origin).toBe('site')
            expect((await dq(`date=${d}&lang=zh`)).json.quote.text).toBe(r.json.quote.text)
            seen.add(r.json.quote.text)
          }
          expect(seen.size).toBe(2)
          expect(
            (await dq('date=2026-11-23&lang=zh')).json.quote.source === '苏轼' ||
              (await dq('date=2026-11-23&lang=zh')).json.quote.source === undefined,
          ).toBe(true)
          expect((await dq('date=2026-11-23&lang=zh-CN')).json.quote.text).toBeTruthy()
          expect((await dq('date=2026-11-23&lang=fr')).json.quote.text).toBe('For any language')
          const enSeen = new Set<string>()
          for (let k = 0; k < 3; k++) enSeen.add((await dq(`date=2026-11-23&lang=en&skip=${k}`)).json.quote.text)
          expect(enSeen.size).toBe(3)
          // Markdown is cut at the rules.
          expect((await put('en', 'First\nsecond line\n---\nSecond\n---\n\n', 'markdown')).json.lines.byLang.en).toBe(2)
          // The owner of the space leaves the default, or takes it back.
          const prefs = await call(admin, 'GET', `/api/spaces/${sp}/daily/prefs`)
          expect(prefs.json).toMatchObject({
            inherit: true,
            apis: [],
            available: expect.any(Array),
            default: { kind: 'list' },
          })
          expect((await call(admin, 'PUT', `/api/spaces/${sp}/daily/prefs`, { inherit: false })).status).toBe(400)
          expect(
            (await call(admin, 'PUT', `/api/spaces/${sp}/daily/prefs`, { inherit: true, apis: ['nope'] })).status,
          ).toBe(400)
          // A space lists sources of its own, of several kinds, for each language; one that is none is refused.
          const prefsUrl = `/api/spaces/${sp}/daily/prefs`
          for (const bad of [
            [{ kind: 'rss' }],
            [{ kind: 'api', id: 'nope' }],
            [{ kind: 'own' }],
            [{ kind: 'list', id: 'x' }],
          ])
            expect((await call(admin, 'PUT', prefsUrl, { inherit: true, apis: [], sources: { '': bad } })).status).toBe(
              400,
            )
          const mixed = await call(admin, 'PUT', prefsUrl, {
            inherit: true,
            apis: [],
            sources: { '': [{ kind: 'list' }, { kind: 'own', source: { tag: 'nothing-here' } }] },
          })
          expect(mixed.status).toBe(200)
          expect(mixed.json.sources[''].map((x: { kind: string }) => x.kind)).toEqual(['list', 'own'])
          expect((await call(admin, 'PUT', prefsUrl, { inherit: true, apis: [] })).json.sources).toBeUndefined()
          const off = await call(admin, 'PUT', `/api/spaces/${sp}/daily/prefs`, { inherit: false, apis: [] })
          expect([off.status, off.json.inherit]).toEqual([200, false])
          expect((await dq('date=2026-11-23&lang=zh')).json).toEqual({ page: null, reason: 'no_source' })
          await call(admin, 'PUT', `/api/spaces/${sp}/daily/prefs`, { inherit: true, apis: [] })
          // The space's own source comes first.
          await call(admin, 'POST', `/api/spaces/${sp}/pages`, {
            folder: 'Own',
            meta: { title: 'Mine' },
            body: 'mine\n',
          })
          expect(
            (await call(admin, 'PUT', `/api/spaces/${sp}/daily/settings`, { daily: { folder: 'Own' } })).status,
          ).toBe(200)
          const mine = await dq('date=2026-11-23&lang=zh')
          expect([mine.json.origin, mine.json.page.title, mine.json.quote]).toEqual(['space', 'Mine', undefined])
          // The administrators can lock the space's own source and its choice of an interface: what the space
          // saved is kept and says it is not in effect, and counts again when they unlock it.
          const locks = async (allow: unknown) => (await adm('PUT', '', { allow })).json.allow
          expect(await locks({ own: false })).toEqual({ own: false, api: true })
          expect((await dq('date=2026-11-23&lang=zh')).json.origin).toBe('site')
          const kept = await call(admin, 'GET', `/api/spaces/${sp}/daily/settings`)
          expect([kept.json.inEffect, kept.json.daily.folder]).toEqual([false, 'Own'])
          expect((await call(admin, 'GET', `/api/spaces/${sp}/daily/prefs`)).json.allowed).toEqual({
            own: false,
            api: true,
          })
          expect(await locks({ own: true, api: false })).toEqual({ own: true, api: false })
          expect((await call(admin, 'GET', `/api/spaces/${sp}/daily/prefs`)).json.allowed).toEqual({
            own: true,
            api: false,
          })
          expect((await dq('date=2026-11-23&lang=zh')).json.origin).toBe('space')
          for (const bad of [{}, 'no', { own: 'no' }, null])
            expect((await adm('PUT', '', { allow: bad })).status).toBe(400)
          expect(await locks({ api: true })).toEqual({ own: true, api: true })
          // Taking the lines away.
          expect((await adm('DELETE', '/lines?lang=zh')).json.lines.byLang['zh-Hans']).toBeUndefined()
          // Another person has no way in.
          const outsider = browser()
          expect((await call(outsider, 'GET', '/api/admin/daily')).status).toBe(401)
        } finally {
          await cleanup()
        }
      })
      it('the administrators configure external interfaces themselves: any number, headers kept secret, a list of languages, built-in interfaces only offered', async () => {
        const adm = (method: string, path: string, body?: unknown) =>
          call(admin, method, `/api/admin/daily${path}`, body)
        const iface = (over: Record<string, unknown> = {}) => ({
          name: `Quotes ${run}`,
          lang: 'en',
          url: 'https://quotes.example.org/api/{lang}/{date}',
          text: '[0].q',
          source: '[0].s',
          author: '[0].a',
          link: 'https://quotes.example.org/q/{[0].id}',
          refresh: 'hourly',
          skipNew: true,
          rps: 5,
          headers: [{ name: 'X-Api-Key', value: `secret-${run}` }],
          ...over,
        })
        const ids = [`c${run}a`, `c${run}b`]
        try {
          const start = await adm('GET', '')
          expect(start.json.languages).toEqual(expect.any(Array))
          expect(start.json.presets).toBeUndefined()
          expect(start.json.providers.map((p: any) => [p.id, p.languages, p.rps])).toEqual([
            ['hitokoto', ['zh-Hans', 'zh-Hant'], 20],
            ['zenquotes', ['en'], 0.1],
            ['favqs', ['en'], 1],
            ['animechan', ['en'], 1 / 60],
            ['dummyjson', ['en'], 1],
            ['jinrishici', ['zh-Hans', 'zh-Hant'], 1],
            ['shanbay', ['zh-Hans', 'en'], 1],
            ['iciba', ['zh-Hans', 'en'], 1],
          ])
          expect(start.json.apis.some((a: any) => a.builtin)).toBe(false)
          // Making one: the value of a header is kept and never given back.
          const made = await adm('PUT', `/apis/${ids[0]}`, iface())
          expect(made.status).toBe(200)
          const got = made.json.apis.find((a: any) => a.id === ids[0])
          expect(got).toMatchObject({
            id: ids[0],
            lang: 'en',
            refresh: 'hourly',
            skipNew: true,
            rps: 5,
            enabled: true,
            text: '[0].q',
            link: 'https://quotes.example.org/q/{[0].id}',
            headers: [{ name: 'X-Api-Key', set: true }],
          })
          expect(JSON.stringify(made.json)).not.toContain(`secret-${run}`)
          expect(made.json.hosts).toBeUndefined()
          // A header that comes without a value keeps what it has; one that is left out goes.
          const kept = await adm('PUT', `/apis/${ids[0]}`, iface({ name: 'Renamed', headers: [{ name: 'x-api-key' }] }))
          expect(kept.json.apis.find((a: any) => a.id === ids[0])).toMatchObject({
            name: 'Renamed',
            headers: [{ name: 'x-api-key', set: true }],
          })
          const bare = await adm('PUT', `/apis/${ids[0]}`, iface({ name: 'Renamed', headers: [] }))
          expect(bare.json.apis.find((a: any) => a.id === ids[0]).headers).toEqual([])
          // Any number of them, by language or for all of them.
          const second = await adm(
            'PUT',
            `/apis/${ids[1]}`,
            iface({ lang: '', refresh: 'minutes', refreshMinutes: 20, headers: [] }),
          )
          expect(second.json.apis.filter((a: any) => ids.includes(a.id)).map((a: any) => a.lang)).toEqual(['en', ''])
          expect(second.json.apis.find((a: any) => a.id === ids[1])).toMatchObject({
            refresh: 'minutes',
            refreshMinutes: 20,
          })
          // What the rules refuse.
          const refuse: [string, string, Record<string, unknown>][] = [
            ['http', 'http://quotes.example.org/', { url: 'http://quotes.example.org/' }],
            ['loopback', 'x', { url: 'https://127.0.0.1/' }],
            ['localhost', 'x', { url: 'https://localhost:8443/' }],
            ['the metadata address', 'x', { url: 'https://169.254.169.254/latest' }],
            ['a private address', 'x', { url: 'https://10.0.0.1/' }],
            ['no text path', 'x', { text: '' }],
            ['a refresh that is none', 'x', { refresh: 'weekly' }],
            ['minutes with none', 'x', { refresh: 'minutes' }],
            ['a rate that is too high', 'x', { rps: 1000 }],
            ['a header with no value', 'x', { headers: [{ name: 'X-New' }] }],
            ['a header that cannot be set', 'x', { headers: [{ name: 'Host', value: 'x' }] }],
            ['a bad language', 'x', { lang: '中文' }],
            ['a lang that is no language', 'x', { lang: 'cn' }],
            ['a language mapping from no language', 'x', { langMap: { chinese: 'zh' } }],
          ]
          for (const [what, , over] of refuse)
            expect([what, (await adm('PUT', `/apis/${ids[1]}`, iface(over))).status]).toEqual([what, 400])
          expect((await adm('PUT', '/apis/BAD ID', iface())).status).toBe(400)
          expect((await adm('PUT', `/apis/${ids[1]}`, iface({ id: 'another' }))).status).toBe(400)
          // Spaces may choose the enabled ones, not enter addresses.
          const sp = (await call(admin, 'POST', '/api/spaces', { name: `Ext ${run}` })).json.id as string
          const prefs = await call(admin, 'GET', `/api/spaces/${sp}/daily/prefs`)
          expect(prefs.json.available).toEqual(expect.arrayContaining([{ id: ids[0], name: 'Renamed', lang: 'en' }]))
          expect(JSON.stringify(prefs.json)).not.toContain('quotes.example.org')
          expect(prefs.json.languages).toEqual(start.json.languages)
          const off = await adm('PUT', `/apis/${ids[0]}`, iface({ name: 'Renamed', enabled: false, headers: [] }))
          expect(off.json.apis.find((a: any) => a.id === ids[0]).enabled).toBe(false)
          expect(
            (await call(admin, 'PUT', `/api/spaces/${sp}/daily/prefs`, { inherit: true, apis: [ids[0]] })).status,
          ).toBe(400)
          const none = await call(admin, 'GET', `/api/spaces/${sp}/daily/prefs`)
          expect(none.json.available.some((a: any) => a.id === ids[0])).toBe(false)
          // A trial: a refusal with a code is a 400 with what the trial showed in `result`; a failure with none is a 200 with ok false.
          const trial = (url: string) =>
            adm('POST', '/try', { api: iface({ id: ids[1], url, headers: [] }), lang: 'en', date: '2026-11-23' })
          // There is no list of hosts: another address is not refused for its host.
          const elsewhere = await trial(`https://elsewhere-${run}.example.net/q`)
          expect(elsewhere.json.error).not.toBe('host_not_allowed')
          const down = await trial('https://quotes.example.org/down')
          expect(down.status).toBe(200)
          expect(down.json).toMatchObject({ ok: false, url: expect.any(String), message: expect.any(String) })
          expect(down.json.error).toBeUndefined()
          // Taking away.
          expect((await adm('DELETE', `/apis/${ids[0]}`)).json.apis.some((a: any) => a.id === ids[0])).toBe(false)
          expect((await adm('DELETE', `/apis/${ids[0]}`)).status).toBe(404)
          // The languages are a list of the administrators' own.
          const langs = await adm('PUT', '', { languages: ['zh', 'en', 'ja', 'JA', 'pt-br'] })
          expect(langs.json.languages).toEqual(['zh-Hans', 'en', 'ja', 'pt-BR'])
          expect(langs.json.knownLanguages[0]).toEqual({
            tag: 'zh-Hans',
            name: '中文（简体）',
            english: 'Chinese (Simplified)',
          })
          for (const bad of [['chinese!'], ['cn'], ['zh_CN'], 'zh', null])
            expect([JSON.stringify(bad), (await adm('PUT', '', { languages: bad })).status]).toEqual([
              JSON.stringify(bad),
              400,
            ])
          expect((await adm('PUT', '', { languages: ['cn'] })).json.error).toBe('bad_language')
          await adm('PUT', '', { languages: start.json.languages })
          expect((await call(browser(), 'PUT', `/api/admin/daily/apis/${ids[1]}`, iface())).status).toBe(401)
        } finally {
          for (const id of ids) await adm('DELETE', `/apis/${id}`)
        }
      })
    } else {
      it('has no settings of administrators or choices of spaces in single-user mode', async () => {
        for (const [method, path] of [
          ['GET', '/api/admin/daily'],
          ['PUT', '/api/admin/daily'],
          ['PUT', '/api/admin/daily/lines'],
          ['GET', `${base()}/daily/prefs`],
          ['PUT', `${base()}/daily/prefs`],
        ]) {
          const r = await call(admin, method, path, method === 'GET' ? undefined : {})
          expect([method, path, r.status, r.json?.error]).toEqual([method, path, 404, 'not_found'])
        }
      })
    }
  })

  describe('create and read', () => {
    it('a page carries its header as text: the lines between the --- lines, naming its id and fields, the same from create, get and save', async () => {
      const p = await mk(`${root}/header`, { title: `头部原文${run}`, tags: ['a'] }, 'x\n')
      expect(typeof p.header).toBe('string')
      expect(p.header!.endsWith('\n')).toBe(true)
      expect(p.header).not.toContain('---')
      expect(p.header).toMatch(new RegExp(`^id: ["']?${p.id}["']?$`, 'm'))
      expect(p.header).toMatch(/^title: /m)
      expect((await S.getPage(p.id)).header).toBe(p.header)
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, series: '冬' }, body: p.body })
      expect(saved.header).toMatch(/^series: /m)
      expect((await S.getPage(p.id)).header).toBe(saved.header)
    })
    it('creates a titled page: assigned id and created, file named from the title, missing folders made', async () => {
      const p = await mk(
        `${root}/titled/deep`,
        { id: 'CLIENTSENT', created: '1999-01-01', title: `暮雪${run}`, subtitle: '副', tags: [`tg${run}`] },
        '正文\n',
      )
      expect(p.id).toMatch(/^[0-9A-HJKMNP-TV-Z]{10}$/)
      expect(p.id).not.toBe('CLIENTSENT')
      expect(p.rev).toMatch(/^[0-9a-f]{40}$/)
      expect(p.meta.id).toBe(p.id)
      expect(p.meta.created).toMatch(/^\d{4}-\d{2}-\d{2}$/)
      expect(p.meta.created).not.toBe('1999-01-01')
      expect(p.meta.title).toBe(`暮雪${run}`)
      expect(p.folder).toBe(`${root}/titled/deep`)
      expect(p.path).toBe(`${root}/titled/deep/暮雪${run}.md`)
      expect(p.body).toBe('正文\n')
      const { folders } = await S.tree()
      expect(folders.map((f) => f.path)).toEqual(
        expect.arrayContaining([`${root}`, `${root}/titled`, `${root}/titled/deep`]),
      )
    })
    it('creates an untitled page: no title key, file named from date and id', async () => {
      const p = await mk(`${root}/untitled`, {}, '没有标题的一句话\n')
      expect('title' in p.meta).toBe(false)
      expect(p.path).toMatch(/\/\d{4}-\d{2}-\d{2}-[^/]+\.md$/)
      const s = (await walk({ folder: `${root}/untitled` })).find((i) => i.id === p.id)!
      expect(s.untitled).toBe(true)
      expect(s.title).toBeUndefined()
      expect(s.label).toBe('没有标题的一句话')
    })

    it('creates with no fields at all, in the root folder', async () => {
      const p = await S.createPage()
      expect(p.folder).toBe('')
      expect(p.body).toBe('')
      expect(p.id).toMatch(/^[0-9A-HJKMNP-TV-Z]{10}$/)
      await S.deletePage(p.id)
    })

    it('an untitled page with an empty body has the empty string as its label', async () => {
      const p = await mk(`${root}/emptylabel`)
      const s = (await walk({ folder: `${root}/emptylabel` }))[0]
      expect(s.id).toBe(p.id)
      expect(s.untitled).toBe(true)
      expect(s.label).toBe('')
    })

    it('cuts an untitled label to about 30 characters', async () => {
      const p = await mk(`${root}/longlabel`, {}, '字'.repeat(80) + '\n')
      const s = (await walk({ folder: `${root}/longlabel` })).find((i) => i.id === p.id)!
      expect(Array.from(s.label).length).toBeGreaterThan(0)
      expect(Array.from(s.label).length).toBeLessThanOrEqual(40)
    })

    it('gets a page, and 404s for one that does not exist', async () => {
      const p = await mk(`${root}/get`, { title: `读取${run}` }, 'x\n')
      expect(await S.getPage(p.id)).toEqual(p)
      const e = await failure(S.getPage('01ZZZZZZZZ'))
      expect(e.status).toBe(404)
      expect(e.code).toBe('not_found')
    })
  })

  describe('save', () => {
    it('saves with the current rev: new rev, same id, body replaced', async () => {
      const p = await mk(`${root}/save`, { title: `保存${run}` }, '旧\n')
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '新\n' })
      expect(saved.id).toBe(p.id)
      expect(saved.rev).not.toBe(p.rev)
      expect(saved.body).toBe('新\n')
      expect((await S.getPage(p.id)).rev).toBe(saved.rev)
    })

    it('renames the file when the title changes; the id stays', async () => {
      const p = await mk(`${root}/rename`, { title: `旧名${run}` }, 'x\n')
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, title: `新名${run}` }, body: p.body })
      expect(saved.id).toBe(p.id)
      expect(saved.path).toBe(`${root}/rename/新名${run}.md`)
      expect((await S.getPage(p.id)).path).toBe(saved.path)
    })

    it('a save and a commit answer textRev, which is rev when no link was rewritten; GET has none', async () => {
      const p = await mk(`${root}/textrev`, { title: `文本${run}` }, '一\n')
      expect(p.textRev).toBeUndefined()
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '二\n' })
      expect(saved.textRev).toBe(saved.rev)
      const d = await S.putDraft(p.id, { prev: '', baseRev: saved.rev, meta: saved.meta, body: '三\n' })
      const committed = await S.commitPage(p.id, d.version)
      expect(committed.textRev).toBe(committed.rev)
      expect((await S.getPage(p.id)).textRev).toBeUndefined()
    })

    // The mock rewrites no links when a page is renamed.
    it.skipIf(!REAL)(
      'server only: a save rebased over a rename answers a textRev of its own; text typed on it keeps the rewrite',
      async () => {
        const tg = await mk(`${root}/textrev-b`, { title: `乙${run}` }, 'x\n')
        const text = `[看](../textrev-b/乙${run}.md "看乙")\n`
        const a = await mk(`${root}/textrev-a`, { title: `甲${run}` }, text)
        // The page it links to is renamed while a is open: the server rewrites the link.
        await S.savePage(tg.id, { baseRev: tg.rev, meta: { ...tg.meta, title: `丙${run}` }, body: tg.body })
        const saved = await S.savePage(a.id, { baseRev: a.rev, meta: a.meta, body: text + '一\n' })
        expect(saved.body).toBe(`[看](../textrev-b/丙${run}.md "看乙")\n一\n`)
        expect(saved.textRev).toBeTruthy()
        expect(saved.textRev).not.toBe(saved.rev)
        // Typed while the save was out, on the old text: based on textRev.
        const next = await S.savePage(a.id, { baseRev: saved.textRev!, meta: saved.meta, body: text + '一\n二\n' })
        expect(next.body).toBe(`[看](../textrev-b/丙${run}.md "看乙")\n一\n二\n`)
      },
    )

    it('a stale baseRev is 409 conflict carrying the current page, and nothing is overwritten', async () => {
      const p = await mk(`${root}/conflict`, { title: `冲突${run}` }, '起点\n')
      const first = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '先到\n' })
      const e = await failure(S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '后到\n' }))
      expect(e.status).toBe(409)
      expect(e.code).toBe('conflict')
      expect(e.current?.rev).toBe(first.rev)
      expect(e.current?.body).toBe('先到\n')
      expect((await S.getPage(p.id)).body).toBe('先到\n')
    })

    it('a request without meta, body or baseRev is 400 invalid; an id mismatch too', async () => {
      const p = await mk(`${root}/partial`, { title: `残缺${run}` }, 'x\n')
      const url = `${SP_PAGES}/${p.id}`
      const put = (payload: unknown) =>
        fetch(url, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(payload) })
      for (const payload of [
        { baseRev: p.rev, meta: p.meta },
        { baseRev: p.rev, body: 'y\n' },
        { meta: p.meta, body: 'y\n' },
        {},
      ]) {
        const r = await put(payload)
        expect(r.status).toBe(400)
        expect(((await r.json()) as { error: string }).error).toBe('invalid')
      }
      const e = await failure(S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, id: '01AAAAAAAA' }, body: 'y\n' }))
      expect(e.status).toBe(400)
      expect(e.code).toBe('invalid')
      expect((await S.getPage(p.id)).body).toBe('x\n')
    })

    it('refuses a write that does not say it is JSON', async () => {
      const p = await mk(`${root}/ctype`, { title: `类型${run}` })
      const r = await fetch(`${SP_PAGES}/${p.id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'text/plain' },
        body: JSON.stringify({ baseRev: p.rev, meta: p.meta, body: 'z\n' }),
      })
      expect([400, 415]).toContain(r.status)
      expect(((await r.json()) as { error: string }).error).toBe('invalid')
    })

    it('keeps created and id when left out, removes other absent keys, and returns unknown fields exactly', async () => {
      const p = await mk(`${root}/header`, { title: `头部${run}` }, 'x\n')
      const created = p.meta.created
      const weird = { nested: { a: [1, 2, { b: null }] }, 数字: 42, flag: true, 空: '' }
      const a = await S.savePage(p.id, {
        baseRev: p.rev,
        meta: { title: `头部${run}`, series: '冬季', ...weird },
        body: 'x\n',
      })
      expect(a.meta.id).toBe(p.id)
      expect(a.meta.created).toBe(created)
      expect(a.meta.series).toBe('冬季')
      for (const [k, v] of Object.entries(weird)) expect(a.meta[k]).toEqual(v)
      // Send back exactly what was served: nothing changes.
      const b = await S.savePage(a.id, { baseRev: a.rev, meta: a.meta, body: a.body })
      expect(b.meta).toEqual(a.meta)
      // A key left out is removed; id and created stay.
      const { series: _series, ...without } = a.meta
      void _series
      const c = await S.savePage(b.id, { baseRev: b.rev, meta: without, body: b.body })
      expect('series' in c.meta).toBe(false)
      expect(c.meta.created).toBe(created)
      expect(c.meta.id).toBe(p.id)
      expect(c.meta.nested).toEqual(weird.nested)
    })

    it('a title added later and removed again leaves the page untitled', async () => {
      const p = await mk(`${root}/retitle`, {}, '一句话\n')
      const a = await S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, title: `加了${run}` }, body: p.body })
      expect(a.meta.title).toBe(`加了${run}`)
      const { title: _t, ...rest } = a.meta
      void _t
      const b = await S.savePage(a.id, { baseRev: a.rev, meta: rest, body: a.body })
      expect('title' in b.meta).toBe(false)
      const s = (await walk({ folder: `${root}/retitle` })).find((i) => i.id === p.id)!
      expect(s.untitled).toBe(true)
    })
  })

  describe('move and delete', () => {
    it('moves a page, creating the folder; id and content stay', async () => {
      const p = await mk(`${root}/from`, { title: `搬家${run}` }, '内容\n')
      const moved = await S.movePage(p.id, `${root}/to/new`)
      expect(moved.id).toBe(p.id)
      expect(moved.folder).toBe(`${root}/to/new`)
      expect(moved.path).toBe(`${root}/to/new/搬家${run}.md`)
      expect(moved.body).toBe('内容\n')
      expect((await S.getPage(p.id)).path).toBe(moved.path)
      const tree = Object.fromEntries((await S.tree()).folders.map((f) => [f.path, f.pages]))
      expect(tree[`${root}/to/new`]).toBe(1)
      expect(tree[`${root}/from`]).toBe(0)
    })

    it('moves to the root folder', async () => {
      const p = await mk(`${root}/toroot`, { title: `回根${run}` })
      const moved = await S.movePage(p.id, '')
      expect(moved.folder).toBe('')
      await S.deletePage(p.id)
    })

    it('refuses a bad destination with 400 invalid', async () => {
      const p = await mk(`${root}/badmove`, { title: `坏路径${run}` })
      for (const folder of ['.hidden', 'a/..', '/abs', 'trail/', 'assets']) {
        const e = await failure(S.movePage(p.id, folder))
        expect(e.status, folder).toBe(400)
        expect(e.code).toBe('invalid')
      }
      expect((await S.getPage(p.id)).folder).toBe(`${root}/badmove`)
    })

    it('deletes with 204, then the page is gone', async () => {
      const p = await mk(`${root}/del`, { title: `删除${run}` })
      expect(await S.deletePage(p.id)).toBeUndefined()
      expect((await failure(S.getPage(p.id))).code).toBe('not_found')
      expect((await failure(S.deletePage(p.id))).status).toBe(404)
      expect((await walk({ folder: `${root}/del` })).length).toBe(0)
    })
  })

  describe('folders and tree', () => {
    const bad = [
      '.hidden',
      'a/.b',
      '..',
      'a/..',
      'name.',
      'name ',
      'a/name.',
      'assets',
      'assets/inside',
      '长'.repeat(86),
      'a//b',
      '/lead',
      'trail/',
    ]

    it('lists empty folders and counts only pages directly inside', async () => {
      await S.createFolder(`${root}/tree/empty`)
      await mk(`${root}/tree`, { title: `树一${run}` })
      await mk(`${root}/tree/sub`, { title: `树二${run}` })
      await mk(`${root}/tree/sub`, { title: `树三${run}` })
      const tree = Object.fromEntries((await S.tree()).folders.map((f) => [f.path, f.pages]))
      expect(tree['']).toBeGreaterThanOrEqual(0)
      expect(tree[`${root}/tree/empty`]).toBe(0)
      expect(tree[`${root}/tree`]).toBe(1)
      expect(tree[`${root}/tree/sub`]).toBe(2)
    })

    it('creates a folder, 409 exists the second time, and 409 exists for the root', async () => {
      expect(await S.createFolder(`${root}/mkdir/a`)).toEqual({ path: `${root}/mkdir/a` })
      const again = await failure(S.createFolder(`${root}/mkdir/a`))
      expect([again.status, again.code]).toEqual([409, 'exists'])
      const rootErr = await failure(S.createFolder(''))
      expect([rootErr.status, rootErr.code]).toEqual([409, 'exists'])
    })

    it('refuses folder names the server does not allow, with 400 invalid', async () => {
      for (const name of bad) {
        const path =
          name.startsWith('/') ||
          name.startsWith('.') ||
          ['assets', 'assets/inside'].includes(name) ||
          name === '..' ||
          name === 'a/..' ||
          name === 'a//b'
            ? name
            : `${root}/${name}`
        const e = await failure(S.createFolder(path))
        expect(e.status, name).toBe(400)
        expect(e.code, name).toBe('invalid')
      }
      const e = await failure(S.createPage({ folder: '.hidden', body: '' }))
      expect([e.status, e.code]).toEqual([400, 'invalid'])
    })

    it('accepts a name that merely contains a dot or the word assets below the root', async () => {
      expect(await S.createFolder(`${root}/v1.2`)).toEqual({ path: `${root}/v1.2` })
      expect(await S.createFolder(`${root}/assets`)).toEqual({ path: `${root}/assets` })
    })
  })

  describe('batch, folder move and folder delete', () => {
    const treeOf = async () => Object.fromEntries((await S.tree()).folders.map((f) => [f.path, f.pages]))

    it('batch move moves every page and counts them, one already in the folder included', async () => {
      const a = await mk(`${root}/bm/src`, { title: `批一${run}` }, 'a\n')
      const b = await mk(`${root}/bm/src`, { title: `批二${run}` }, 'b\n')
      const c = await mk(`${root}/bm/dst`, { title: `批三${run}` }, 'c\n')
      const r = await S.batchPages({ op: 'move', ids: [a.id, b.id, c.id], folder: `${root}/bm/dst` })
      expect(r).toEqual({ pages: 3 })
      for (const p of [a, b, c]) {
        const now = await S.getPage(p.id)
        expect(now.folder).toBe(`${root}/bm/dst`)
        expect(now.body).toBe(p.body)
      }
      const tree = await treeOf()
      expect(tree[`${root}/bm/dst`]).toBe(3)
      expect(tree[`${root}/bm/src`]).toBe(0)
      // A page that is already in the folder counts and stays; so does a move to the root.
      expect(await S.batchPages({ op: 'move', ids: [a.id], folder: `${root}/bm/dst` })).toEqual({ pages: 1 })
      expect(await S.batchPages({ op: 'move', ids: [a.id, b.id], folder: '' })).toEqual({ pages: 2 })
      expect((await S.getPage(a.id)).folder).toBe('')
      await S.batchPages({ op: 'delete', ids: [a.id, b.id, c.id] })
    })

    it('batch delete removes every page and counts them', async () => {
      const a = await mk(`${root}/bd`, { title: `批删一${run}` })
      const b = await mk(`${root}/bd`, { title: `批删二${run}` })
      expect(await S.batchPages({ op: 'delete', ids: [a.id, b.id] })).toEqual({ pages: 2 })
      expect((await failure(S.getPage(a.id))).code).toBe('not_found')
      expect((await failure(S.getPage(b.id))).code).toBe('not_found')
      expect((await walk({ folder: `${root}/bd` })).length).toBe(0)
    })

    it('an id that is not a page is 404 not_found and nothing at all is changed', async () => {
      const a = await mk(`${root}/bu`, { title: `批未知${run}` })
      const nowhere = '0ZZZZZZZZZ'
      for (const input of [
        { op: 'move' as const, ids: [a.id, nowhere], folder: `${root}/bu/elsewhere` },
        { op: 'delete' as const, ids: [nowhere, a.id] },
      ]) {
        const e = await failure(S.batchPages(input))
        expect([e.status, e.code], input.op).toEqual([404, 'not_found'])
        expect((await S.getPage(a.id)).folder, input.op).toBe(`${root}/bu`)
      }
      expect((await treeOf())[`${root}/bu/elsewhere`]).toBeUndefined()
      await S.deletePage(a.id)
    })

    it('a bad op, folder or list is 400 invalid and nothing is changed', async () => {
      const a = await mk(`${root}/bb`, { title: `批坏${run}` })
      const bad: unknown[] = [
        { op: 'copy', ids: [a.id], folder: `${root}/bb` },
        { op: 'move', ids: [a.id], folder: '.hidden' },
        { op: 'move', ids: [a.id], folder: 'assets' },
        { op: 'move', ids: [], folder: `${root}/bb/x` },
        { op: 'delete', ids: [] },
        { op: 'delete', ids: [a.id, a.id] },
        { op: 'delete', ids: Array.from({ length: 501 }, (_, i) => `0${String(i).padStart(9, '0')}`) },
      ]
      for (const input of bad) {
        const e = await failure(S.batchPages(input as Parameters<SpaceApi['batchPages']>[0]))
        expect([e.status, e.code], JSON.stringify(input).slice(0, 60)).toEqual([400, 'invalid'])
      }
      expect((await S.getPage(a.id)).folder).toBe(`${root}/bb`)
      await S.deletePage(a.id)
    })

    it('moves a folder with everything in it: ids stay, paths and the tree follow', async () => {
      const top = await mk(`${root}/fm/old`, { title: `夹顶${run}` }, 'top\n')
      const deep = await mk(`${root}/fm/old/inner/deep`, { title: `夹深${run}` }, 'deep\n')
      await S.createFolder(`${root}/fm/old/emptyinside`)
      const r = await S.moveFolder(`${root}/fm/old`, `${root}/fm2/made/new`)
      expect(r).toEqual({ path: `${root}/fm2/made/new` })
      const topNow = await S.getPage(top.id)
      const deepNow = await S.getPage(deep.id)
      expect(topNow.id).toBe(top.id)
      expect(topNow.folder).toBe(`${root}/fm2/made/new`)
      expect(topNow.path).toBe(`${root}/fm2/made/new/夹顶${run}.md`)
      expect(topNow.body).toBe('top\n')
      expect(deepNow.folder).toBe(`${root}/fm2/made/new/inner/deep`)
      const tree = await treeOf()
      expect(tree[`${root}/fm2/made/new`]).toBe(1)
      expect(tree[`${root}/fm2/made/new/inner/deep`]).toBe(1)
      expect(tree[`${root}/fm2/made/new/emptyinside`]).toBe(0)
      expect(tree[`${root}/fm/old`]).toBeUndefined()
      expect(tree[`${root}/fm/old/inner/deep`]).toBeUndefined()
      expect(ids(await walk({ folder: `${root}/fm2/made/new`, recursive: true }))).toEqual(
        expect.arrayContaining([top.id, deep.id]),
      )
      expect(await walk({ folder: `${root}/fm/old`, recursive: true })).toEqual([])
    })

    it('renames a folder: a move to a sibling path; a change of letter case alone is allowed', async () => {
      const p = await mk(`${root}/Rn`, { title: `改名${run}` })
      expect(await S.moveFolder(`${root}/Rn`, `${root}/rn`)).toEqual({ path: `${root}/rn` })
      const tree = await treeOf()
      expect(tree[`${root}/rn`]).toBe(1)
      expect(tree[`${root}/Rn`]).toBeUndefined()
      expect((await S.getPage(p.id)).folder).toBe(`${root}/rn`)
      expect(await S.moveFolder(`${root}/rn`, `${root}/renamed`)).toEqual({ path: `${root}/renamed` })
      expect((await S.getPage(p.id)).folder).toBe(`${root}/renamed`)
    })

    it('refuses a folder move: 404 for a missing source, 409 exists for an existing target (no merging), 400 for the rest, and nothing changes', async () => {
      const p = await mk(`${root}/fr/src`, { title: `夹拒${run}` })
      await S.createFolder(`${root}/fr/taken`)
      const cases: [string, string, number, string][] = [
        [`${root}/fr/nothere`, `${root}/fr/x`, 404, 'not_found'],
        [`${root}/fr/src`, `${root}/fr/taken`, 409, 'exists'],
        ['', `${root}/fr/fromroot`, 400, 'invalid'],
        [`${root}/fr/src`, `${root}/fr/src`, 400, 'invalid'],
        [`${root}/fr/src`, `${root}/fr/src/inside`, 400, 'invalid'],
        [`${root}/fr/src`, `${root}/fr/.hidden`, 400, 'invalid'],
        [`${root}/fr/src`, 'assets', 400, 'invalid'],
        [`${root}/fr/src`, `${root}/fr/ends.md`, 400, 'invalid'],
        [`${root}/fr/src`, `${root}/fr//double`, 400, 'invalid'],
      ]
      for (const [from, to, status, code] of cases) {
        const e = await failure(S.moveFolder(from, to))
        expect([e.status, e.code], `${from} -> ${to}`).toEqual([status, code])
      }
      expect((await S.getPage(p.id)).folder).toBe(`${root}/fr/src`)
      const tree = await treeOf()
      expect(tree[`${root}/fr/src`]).toBe(1)
      expect(tree[`${root}/fr/taken`]).toBe(0)
    })

    it('deletes an empty folder with 204, empty subfolders going with it', async () => {
      await S.createFolder(`${root}/fd/empty/sub/subsub`)
      expect(await S.deleteFolder(`${root}/fd/empty`)).toBeUndefined()
      const tree = await treeOf()
      expect(tree[`${root}/fd/empty`]).toBeUndefined()
      expect(tree[`${root}/fd/empty/sub`]).toBeUndefined()
      expect(tree[`${root}/fd/empty/sub/subsub`]).toBeUndefined()
    })

    it('without recursive a folder holding pages, in it or below it, is 409 conflict and stays whole', async () => {
      const here = await mk(`${root}/fc/full`, { title: `夹满${run}` })
      const below = await mk(`${root}/fc/below/deeper`, { title: `夹下${run}` })
      for (const folder of [`${root}/fc/full`, `${root}/fc/below`]) {
        const e = await failure(S.deleteFolder(folder))
        expect([e.status, e.code], folder).toEqual([409, 'conflict'])
        expect((await treeOf())[folder], folder).not.toBeUndefined()
      }
      expect((await S.getPage(here.id)).folder).toBe(`${root}/fc/full`)
      expect((await S.getPage(below.id)).folder).toBe(`${root}/fc/below/deeper`)
    })

    it('with recursive=true the pages in it and below it are deleted too', async () => {
      const a = await mk(`${root}/fx/all`, { title: `夹删一${run}` })
      const b = await mk(`${root}/fx/all/in/in`, { title: `夹删二${run}` })
      const keep = await mk(`${root}/fx/keep`, { title: `夹留${run}` })
      expect(await S.deleteFolder(`${root}/fx/all`, true)).toBeUndefined()
      expect((await failure(S.getPage(a.id))).code).toBe('not_found')
      expect((await failure(S.getPage(b.id))).code).toBe('not_found')
      expect((await S.getPage(keep.id)).folder).toBe(`${root}/fx/keep`)
      const tree = await treeOf()
      expect(tree[`${root}/fx/all`]).toBeUndefined()
      expect(tree[`${root}/fx/all/in/in`]).toBeUndefined()
      expect(tree[`${root}/fx`]).not.toBeUndefined()
    })

    it('deleting the root is 400 invalid and a folder that is not there is 404 not_found', async () => {
      for (const recursive of [false, true]) {
        const e = await failure(S.deleteFolder('', recursive))
        expect([e.status, e.code]).toEqual([400, 'invalid'])
      }
      const e = await failure(S.deleteFolder(`${root}/fx/never/was`, true))
      expect([e.status, e.code]).toEqual([404, 'not_found'])
    })
  })

  describe('tags', () => {
    it('counts pages per tag, ordered by count descending', async () => {
      const a = `a${run}`
      const b = `b${run}`
      await mk(`${root}/tags`, { title: `标签一${run}`, tags: [a, b] })
      await mk(`${root}/tags`, { title: `标签二${run}`, tags: [a] })
      await mk(`${root}/tags`, { title: `标签三${run}`, tags: [a] })
      const { tags } = await S.tags()
      const count = (t: string) => tags.find((x) => x.tag === t)?.pages
      expect(count(a)).toBe(3)
      expect(count(b)).toBe(1)
      for (let i = 1; i < tags.length; i++) expect(tags[i - 1].pages).toBeGreaterThanOrEqual(tags[i].pages)
    })

    it('a tag change on save moves the counts', async () => {
      const t = `m${run}`
      const p = await mk(`${root}/tagmove`, { title: `改标签${run}`, tags: [t] })
      expect((await S.tags()).tags.find((x) => x.tag === t)?.pages).toBe(1)
      await S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, tags: [] }, body: p.body })
      expect((await S.tags()).tags.find((x) => x.tag === t)).toBeUndefined()
    })

    it('sets a colour on a tag, shows it in the listing, and removes it with null', async () => {
      const t = `col${run}`
      await mk(`${root}/tagcolor`, { title: `染色${run}`, tags: [t] })
      expect((await S.tags()).tags.find((x) => x.tag === t)?.color).toBeUndefined()
      expect(await S.setTagColor(t, 'rose')).toEqual({ tag: t, color: 'rose' })
      expect((await S.tags()).tags.find((x) => x.tag === t)).toMatchObject({ tag: t, pages: 1, color: 'rose' })
      // Another colour replaces it.
      expect(await S.setTagColor(t, 'teal')).toEqual({ tag: t, color: 'teal' })
      expect((await S.tags()).tags.find((x) => x.tag === t)?.color).toBe('teal')
      // null removes it; the answer then has no `color` at all.
      const removed = await S.setTagColor(t, null)
      expect(removed).toEqual({ tag: t })
      expect('color' in removed).toBe(false)
      expect('color' in (await S.tags()).tags.find((x) => x.tag === t)!).toBe(false)
    })

    it('accepts each of the ten colour names', async () => {
      const t = `ten${run}`
      await mk(`${root}/tagcolor`, { title: `十色${run}`, tags: [t] })
      for (const c of ['gray', 'red', 'orange', 'amber', 'green', 'teal', 'blue', 'purple', 'pink', 'rose'] as const) {
        expect((await S.setTagColor(t, c)).color).toBe(c)
      }
      await S.setTagColor(t, null)
    })

    it('a custom #rrggbb colour is stored in lower case and listed that way', async () => {
      const t = `hex${run}`
      await mk(`${root}/tagcolor`, { title: `自定${run}`, tags: [t] })
      expect(await S.setTagColor(t, '#A1B2C3')).toEqual({ tag: t, color: '#a1b2c3' })
      expect((await S.tags()).tags.find((x) => x.tag === t)?.color).toBe('#a1b2c3')
      await S.setTagColor(t, null)
    })

    it('#abc, #gggggg and a hex without the # are 400 invalid and change nothing', async () => {
      const t = `hexbad${run}`
      await mk(`${root}/tagcolor`, { title: `坏自定${run}`, tags: [t] })
      await S.setTagColor(t, '#112233')
      for (const color of ['#abc', '#gggggg', 'abcdef', '#a1b2c3d']) {
        const r = await call(admin, 'PUT', `/api/spaces/${spaceId}/tags/${encodeURIComponent(t)}`, { color })
        expect([r.status, r.json?.error]).toEqual([400, 'invalid'])
      }
      expect((await S.tags()).tags.find((x) => x.tag === t)?.color).toBe('#112233')
      await S.setTagColor(t, null)
    })

    it('another colour name, or a colour of the wrong kind, is 400 invalid and changes nothing', async () => {
      const t = `bad${run}`
      await mk(`${root}/tagcolor`, { title: `坏色${run}`, tags: [t] })
      await S.setTagColor(t, 'blue')
      for (const color of ['magenta', '', 'Rose', 7, true, ['red']]) {
        const r = await call(admin, 'PUT', `/api/spaces/${spaceId}/tags/${encodeURIComponent(t)}`, { color })
        expect([r.status, r.json?.error]).toEqual([400, 'invalid'])
      }
      expect((await S.tags()).tags.find((x) => x.tag === t)?.color).toBe('blue')
      await S.setTagColor(t, null)
    })

    it('a tag need not be in use, and a tag with a slash or non-ASCII letters keeps its name', async () => {
      const t = `${run}/子/标签`
      expect(await S.setTagColor(t, 'amber')).toEqual({ tag: t, color: 'amber' })
      await mk(`${root}/tagcolor`, { title: `斜杠${run}`, tags: [t] })
      expect((await S.tags()).tags.find((x) => x.tag === t)?.color).toBe('amber')
      await S.setTagColor(t, null)
    })

    it('a write that does not say it is JSON is refused, as everywhere', async () => {
      const r = await call(admin, 'PUT', `/api/spaces/${spaceId}/tags/x${run}`, '{"color":"red"}', {})
      expect(r.status).toBeGreaterThanOrEqual(400)
    })
  })

  describe('listing', () => {
    const folder = `${root}/list`
    let created: Page[] = []

    beforeAll(async () => {
      created = []
      for (let i = 0; i < 7; i++)
        created.push(
          await mk(
            folder,
            {
              title: `列${String(i).padStart(2, '0')}${run}`,
              tags: i % 2 ? [`odd${run}`, `all${run}`] : [`all${run}`],
            },
            `正文${i}\n`,
          ),
        )
      await mk(`${folder}/inner`, { title: `内层${run}`, tags: [`all${run}`] })
    })

    it('walks every page with the cursor, to an empty next, without repeats', async () => {
      const items = await walk({ folder }, 3)
      expect(items).toHaveLength(7)
      expect(new Set(ids(items)).size).toBe(7)
      expect(ids(items).sort()).toEqual(created.map((p) => p.id).sort())
      const last = await S.listPages({ folder, limit: 200 })
      expect(last.next).toBe('')
    })

    it('a page of exactly the limit may still have more; the walk ends cleanly', async () => {
      const first = await S.listPages({ folder, limit: 7 })
      expect(first.items).toHaveLength(7)
      if (first.next) expect((await S.listPages({ folder, limit: 7, cursor: first.next })).items).toHaveLength(0)
    })

    it('folder is direct children only unless recursive', async () => {
      expect((await walk({ folder })).every((p) => p.folder === folder)).toBe(true)
      const deep = await walk({ folder, recursive: true })
      expect(deep).toHaveLength(8)
      expect(deep.every((p) => p.folder === folder || p.folder.startsWith(folder + '/'))).toBe(true)
    })

    it('the root folder is listed on its own, and recursive from the root includes everything', async () => {
      const rootPage = await mk('', { title: `根页${run}` })
      expect((await walk({ folder: '' })).some((p) => p.id === rootPage.id)).toBe(true)
      expect((await walk({ folder: '' })).every((p) => p.folder === '')).toBe(true)
      expect((await walk({ folder: '', recursive: true })).some((p) => p.id === created[0].id)).toBe(true)
      await S.deletePage(rootPage.id)
    })

    it('tag filters combine with AND', async () => {
      expect(await walk({ tag: [`all${run}`] })).toHaveLength(8)
      const both = await walk({ tag: [`all${run}`, `odd${run}`] })
      expect(both).toHaveLength(3)
      expect(both.every((p) => p.tags?.includes(`odd${run}`))).toBe(true)
      expect(await walk({ tag: [`all${run}`, `nope${run}`] })).toEqual([])
    })

    it('sorts by title in both directions, by code point and ignoring case', async () => {
      const f = `${root}/titlesort`
      for (const title of [`beta${run}`, `Alpha${run}`, `alpine${run}`, `中文${run}`]) await mk(f, { title })
      await mk(f, {}, '无标题甲\n')
      await mk(f, {}, '无标题乙\n')
      const asc = await walk({ folder: f, sort: 'title' })
      expect(asc.map((p) => p.untitled)).toEqual([false, false, false, false, true, true])
      expect(asc.slice(0, 4).map((p) => p.title)).toEqual([`Alpha${run}`, `alpine${run}`, `beta${run}`, `中文${run}`])
      const desc = await walk({ folder: f, sort: '-title' })
      expect(desc.map((p) => p.untitled)).toEqual([false, false, false, false, true, true])
      expect(desc.slice(0, 4).map((p) => p.title)).toEqual([`中文${run}`, `beta${run}`, `alpine${run}`, `Alpha${run}`])
    })

    it('sorts by updated and created, newest first by default', async () => {
      const items = await walk({ folder })
      for (let i = 1; i < items.length; i++) expect(items[i - 1].updated >= items[i].updated).toBe(true)
      const asc = await walk({ folder, sort: 'updated' })
      for (let i = 1; i < asc.length; i++) expect(asc[i - 1].updated <= asc[i].updated).toBe(true)
      const byCreated = await walk({ folder, sort: '-created' })
      for (let i = 1; i < byCreated.length; i++)
        expect((byCreated[i - 1].created ?? '') >= (byCreated[i].created ?? '')).toBe(true)
      expect(ids(await walk({ folder, sort: 'created' })).sort()).toEqual(ids(items).sort())
    })

    it('summaries carry the documented fields', async () => {
      const s = (await walk({ folder })).find((p) => p.id === created[1].id)!
      expect(s).toMatchObject({
        id: created[1].id,
        folder,
        untitled: false,
        title: `列01${run}`,
        label: `列01${run}`,
        tags: [`odd${run}`, `all${run}`],
      })
      expect(s.path).toBe(`${folder}/列01${run}.md`)
      expect(s.updated).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/)
      expect(s.created).toMatch(/^\d{4}-\d{2}-\d{2}$/)
      expect(s.excerpt).toContain('正文1')
    })

    it('an excerpt is up to 400 characters and keeps line breaks as single \\n, without blank lines', async () => {
      const f = `${root}/excerpt`
      const lines = await mk(f, { title: `行${run}` }, '第一行\n第二行\n\n\n第三段\n')
      const long = await mk(f, { title: `长${run}` }, `${'字'.repeat(500)}\n`)
      const items = await walk({ folder: f })
      expect(items.find((i) => i.id === lines.id)!.excerpt).toBe('第一行\n第二行\n第三段')
      expect(Array.from(items.find((i) => i.id === long.id)!.excerpt ?? '')).toHaveLength(400)
    })
  })

  describe('search', () => {
    const f = `${root}/search`
    const latin = `zephyr${run}`
    let a: Page, b: Page, c: Page, d: Page

    beforeAll(async () => {
      a = await mk(f, { title: `夏夜${run}`, subtitle: '萤火' }, '蕨类植物在潮湿的墙角生长。\n')
      b = await mk(f, {}, `The ${latin} wind came in from the west.\n`)
      c = await mk(f, {}, '蕨类，植物\n')
      d = await mk(f, {}, '蕨类\n植物\n')
    })

    const hits = async (q: string) => ids(await walk({ q, folder: f, recursive: true }))

    it('finds a one-character Chinese term', async () => {
      const r = await hits('蕨')
      expect(r).toEqual(expect.arrayContaining([a.id, c.id, d.id]))
      expect(r).not.toContain(b.id)
    })

    it('finds a two-character Chinese term, only as adjacent characters', async () => {
      const r = await hits('类植')
      expect(r).toContain(a.id)
      // Not across punctuation or a line break.
      expect(r).not.toContain(c.id)
      expect(r).not.toContain(d.id)
      expect(await hits('蕨物')).toEqual([])
    })

    it('finds a Latin word, ignoring case', async () => {
      expect(await hits(latin)).toEqual([b.id])
      expect(await hits(latin.toUpperCase())).toEqual([b.id])
    })

    it('searches the title and the subtitle too', async () => {
      expect(await hits(`夏夜${run}`)).toEqual([a.id])
      expect(await hits('萤火')).toEqual([a.id])
    })

    it('terms separated by spaces must all match', async () => {
      expect(await hits(`蕨类 ${run}`)).toEqual([a.id])
      expect(await hits(`${latin} 蕨类`)).toEqual([])
    })

    it('finds nothing for a term that is not there', async () => {
      expect(await hits(`没有这个词${run}`)).toEqual([])
    })

    it('can be sorted explicitly and combined with a tag filter', async () => {
      const r = await walk({ q: '蕨', folder: f, recursive: true, sort: 'title' })
      expect(r[r.length - 1].untitled).toBe(true)
    })
  })

  describe('listing: title only and date ranges', () => {
    const f = `${root}/ranges`
    const word = `quince${run}`
    const dayOf = (iso: string) => iso.slice(0, 10)
    // A day before or after a `YYYY-MM-DD`, in UTC.
    const shift = (day: string, n: number) => new Date(Date.parse(day) + n * 86_400_000).toISOString().slice(0, 10)
    let titled: Page, bodied: Page

    beforeAll(async () => {
      titled = await mk(f, { title: `${word} 记` }, '没有那个词\n')
      bodied = await mk(f, { title: `别的${run}` }, `正文里有 ${word}\n`)
    })

    it('`in=title` with q searches titles only', async () => {
      expect(ids(await walk({ q: word, folder: f }))).toEqual(expect.arrayContaining([titled.id, bodied.id]))
      expect(ids(await walk({ q: word, in: 'title', folder: f }))).toEqual([titled.id])
      expect(await walk({ q: `正文里有`, in: 'title', folder: f })).toEqual([])
    })

    it('updatedFrom and updatedTo are days, both ends included', async () => {
      const day = dayOf(titled.updated)
      expect(ids(await walk({ folder: f, updatedFrom: day }))).toEqual(expect.arrayContaining([titled.id, bodied.id]))
      expect(await walk({ folder: f, updatedFrom: shift(day, 2) })).toEqual([])
      expect(ids(await walk({ folder: f, updatedTo: day }))).toEqual(expect.arrayContaining([titled.id, bodied.id]))
      expect(await walk({ folder: f, updatedTo: shift(day, -2) })).toEqual([])
      expect(ids(await walk({ folder: f, updatedFrom: day, updatedTo: day }))).toEqual(
        expect.arrayContaining([titled.id]),
      )
      // An empty range is an empty list, not an error.
      expect(await walk({ folder: f, updatedFrom: shift(day, 1), updatedTo: shift(day, -1) })).toEqual([])
    })

    it("createdFrom and createdTo use the header's created day", async () => {
      const day = String(titled.meta.created)
      expect(day).toMatch(/^\d{4}-\d{2}-\d{2}$/)
      expect(ids(await walk({ folder: f, createdFrom: day, createdTo: day }))).toEqual(
        expect.arrayContaining([titled.id, bodied.id]),
      )
      expect(await walk({ folder: f, createdFrom: shift(day, 2) })).toEqual([])
      expect(await walk({ folder: f, createdTo: shift(day, -2) })).toEqual([])
    })

    it('ranges combine with the text, the folder and the tag filters, and with the cursor', async () => {
      const day = dayOf(titled.updated)
      expect(ids(await walk({ q: word, folder: f, updatedFrom: day, updatedTo: day }, 1)).sort()).toEqual(
        [titled.id, bodied.id].sort(),
      )
      expect(await walk({ q: word, folder: f, updatedFrom: shift(day, 2) })).toEqual([])
      expect(
        await walk({ q: word, in: 'title', folder: f, createdTo: shift(String(titled.meta.created), -2) }),
      ).toEqual([])
    })

    it('a date that is not a day is 400 invalid', async () => {
      for (const q of ['updatedFrom=yesterday', 'updatedTo=2026-13-40', 'createdFrom=2026-1-1']) {
        const r = await call(admin, 'GET', `${SP_PAGES}?${q}`)
        expect([r.status, r.json?.error]).toEqual([400, 'invalid'])
      }
    })

    it.skipIf(!!REAL)('mock only: a created range picks pages by their own created day (set with a save)', async () => {
      const older = await mk(f, { title: `旧${run}` })
      const saved = await S.savePage(older.id, {
        baseRev: older.rev,
        meta: { ...older.meta, created: '2020-03-15' },
        body: older.body,
      })
      expect(saved.meta.created).toBe('2020-03-15')
      expect(ids(await walk({ folder: f, createdFrom: '2020-03-15', createdTo: '2020-03-15' }))).toEqual([older.id])
      expect(ids(await walk({ folder: f, createdTo: '2020-03-14' }))).toEqual([])
      expect(ids(await walk({ folder: f, createdTo: '2020-03-16' }))).toEqual([older.id])
      expect(ids(await walk({ folder: f, createdFrom: '2020-03-16', updatedFrom: dayOf(saved.updated) }))).toEqual(
        expect.arrayContaining([titled.id]),
      )
    })
  })

  describe('sources, icons and colours (3.10)', () => {
    const folder = `${root}/style`
    const titles = (items: PageSummary[]) => items.map((i) => i.title).sort()

    it('the source is a list: filtered, sorted, counted, and the sources are listed by use', async () => {
      await S.createFolder(folder)
      await mk(folder, { title: 'sA', source: ['书', '电影'] })
      await mk(folder, { title: 'sB', source: ['书'] })
      await mk(folder, { title: 'sC' })
      const all = await walk({ folder })
      expect(all.find((i) => i.title === 'sA')?.source).toEqual(['书', '电影'])
      expect(all.find((i) => i.title === 'sC')?.source).toEqual([])
      const f = (values: string[]) => walk({ folder, filter: [{ key: 'source', op: 'in', values }] })
      expect(titles(await f(['电影']))).toEqual(['sA'])
      expect(titles(await f(['书']))).toEqual(['sA', 'sB'])
      // Sorted by the first source, the pages with none last.
      const sorted = await walk({ folder, sort: 'prop:source' })
      expect(sorted.at(-1)?.title).toBe('sC')
      const used = (await S.sources()).sources
      expect(used.find((x) => x.source === '书')?.pages).toBeGreaterThanOrEqual(2)
      expect(used.find((x) => x.source === '电影')?.pages).toBeGreaterThanOrEqual(1)
      // The sources are not among the properties of the page.
      const withProps = await walk({ folder, props: true })
      expect(withProps.find((i) => i.title === 'sA')?.props ?? {}).not.toHaveProperty('source')
    })

    it('a folder has an icon and a colour that follow it when it moves; a page has them in its header', async () => {
      const a = `${folder}/a${run}`
      await S.createFolder(a)
      await S.createFolder(`${a}/in`)
      expect(await S.setFolderStyle(a, 'book-open', 'teal')).toMatchObject({
        folder: a,
        icon: 'book-open',
        color: 'teal',
      })
      await S.setFolderStyle(`${a}/in`, '🌸', '')
      const styles = (await S.appearance()).folders
      expect(styles).toContainEqual({ folder: a, icon: 'book-open', color: 'teal' })
      expect(styles).toContainEqual({ folder: `${a}/in`, icon: '🌸' })
      expect((await failure(S.setFolderStyle(a, 'x', 'mauve' as never))).code).toBe('invalid')
      // A name the icons do not have (and `constructor`, a key of every object) is refused, so is more than one emoji.
      for (const icon of ['constructor', '__proto__', 'not-an-icon', '🌸🌸', '<b>'])
        expect((await failure(S.setFolderStyle(a, icon, ''))).code).toBe('invalid')
      expect((await failure(mk(folder, { title: 'sX', icon: 'constructor' }))).code).toBe('invalid')
      expect((await failure(mk(folder, { title: 'sX', color: 'mauve' }))).code).toBe('invalid')
      expect((await failure(S.setFolderStyle(`${a}/nowhere`, 'x', ''))).code).toBe('not_found')
      const b = `${folder}/b${run}`
      await S.moveFolder(a, b)
      const moved = (await S.appearance()).folders
      expect(moved).toContainEqual({ folder: b, icon: 'book-open', color: 'teal' })
      expect(moved).toContainEqual({ folder: `${b}/in`, icon: '🌸' })
      expect(moved.some((x) => x.folder === a)).toBe(false)
      await S.setFolderStyle(b, '', '')
      expect((await S.appearance()).folders.some((x) => x.folder === b)).toBe(false)
      const p = await mk(folder, { title: 'sI', icon: 'flower', color: 'pink' })
      expect((await walk({ folder })).find((i) => i.id === p.id)).toMatchObject({ icon: 'flower', color: 'pink' })
    })

    it("the presets of the sources are the space's, and null goes back to the defaults", async () => {
      expect((await S.appearance()).sourcePresets).toBeNull()
      expect(await S.setSourcePresets([' 书 ', '电影', '书', ''])).toEqual({ presets: ['书', '电影'] })
      expect((await S.appearance()).sourcePresets).toEqual(['书', '电影'])
      // None is not the defaults: an empty list stays one.
      expect(await S.setSourcePresets([])).toEqual({ presets: [] })
      expect((await S.appearance()).sourcePresets).toEqual([])
      await S.setSourcePresets(null)
      expect((await S.appearance()).sourcePresets).toBeNull()
    })

    it('renames the key of a field of a type in the type and in the pages that have it', async () => {
      const name = `rn${run}`
      const rf = `${root}/rn-fields`
      const t = await S.putType(name, {
        title: 'R',
        fields: [
          { key: 'status', kind: 'text' },
          { key: 'due', kind: 'date' },
        ],
      })
      await S.createFolder(rf)
      await S.bindFolder(rf, name)
      const a = await mk(rf, { title: 'rA', status: 'open', due: '2026-12-01' })
      const both = await mk(rf, { title: 'rB', status: 'open', state: 'kept' })
      await mk(`${root}/rn-other`, { title: 'rO', status: 'kept' })
      await S.putViews(
        { folder: rf },
        {
          baseRev: (await S.views({ folder: rf })).rev,
          views: [
            {
              name: 'B',
              layout: 'board',
              group: 'status',
              sort: '-prop:status',
              filter: [{ key: 'status', op: 'in', values: ['open'] }],
              hidden: ['status'],
            },
          ],
        },
      )
      const r = await S.renameTypeField(name, { baseRev: t.rev, from: 'status', to: 'state' })
      expect(r.pages).toBe(1)
      // A page that has the new key already is left as it is, and said so.
      expect(r.skipped).toEqual([{ id: both.id, path: both.path, reason: 'has_key' }])
      expect((await S.getPage(both.id)).meta).toMatchObject({ status: 'open', state: 'kept' })
      // The views of the folder follow the key.
      expect((await S.views({ folder: rf })).views[0]).toMatchObject({
        group: 'state',
        sort: '-prop:state',
        filter: [{ key: 'state', op: 'in', values: ['open'] }],
        hidden: ['state'],
      })
      expect(r.type.fields.map((f) => f.key)).toEqual(['state', 'due'])
      expect((await S.getPage(a.id)).meta).toMatchObject({ state: 'open', due: '2026-12-01' })
      expect('status' in (await S.getPage(a.id)).meta).toBe(false)
      expect((await failure(S.renameTypeField(name, { baseRev: r.type.rev, from: 'state', to: 'due' }))).code).toBe(
        'invalid',
      )
      expect((await failure(S.renameTypeField(name, { baseRev: r.type.rev, from: 'nothing', to: 'x' }))).code).toBe(
        'not_found',
      )
      expect((await failure(S.renameTypeField(name, { baseRev: t.rev, from: 'state', to: 'y' }))).code).toBe('conflict')
      expect((await failure(S.renameTypeField(`nope${run}`, { baseRev: '', from: 'a', to: 'b' }))).code).toBe(
        'not_found',
      )
    })
  })

  describe('page types, properties and the table listing', () => {
    const folder = `${root}/types`
    const typeName = `task${run}`
    const spec = {
      title: '任务',
      fields: [
        { key: 'status', title: '状态', kind: 'select' as const, options: ['todo', 'doing', 'done'] },
        { key: 'points', kind: 'number' as const },
        { key: 'due', kind: 'date' as const },
        { key: 'labels', kind: 'multiselect' as const, options: ['a', 'b'] },
      ],
    }
    const titles = (items: PageSummary[]) => items.map((i) => i.title).join(' ')
    const filter = (f: unknown[]) => JSON.stringify(f)
    let A: Page
    let B: Page

    let typeRev = ''
    it('makes a type with its fields in order, and lists it with the raw schema; names and keys are checked', async () => {
      const t = await S.putType(typeName, spec)
      expect(t).toMatchObject({ name: typeName, title: '任务' })
      expect(t.rev).toMatch(/^[0-9a-f]{40}$/)
      typeRev = t.rev
      expect((await S.types()).types.find((x) => x.name === typeName)?.rev).toBe(typeRev)
      // Made again with no rev, or from one that is not the file's: 409 with the type as it is.
      for (const baseRev of [undefined, '0'.repeat(40)]) {
        const e = await failure(S.putType(typeName, { ...spec, baseRev }))
        expect(e.code).toBe('conflict')
        expect(e.current).toMatchObject({ name: typeName, rev: typeRev })
      }
      expect(t.fields.map((f) => [f.key, f.title, f.kind])).toEqual([
        ['status', '状态', 'select'],
        ['points', '', 'number'],
        ['due', '', 'date'],
        ['labels', '', 'multiselect'],
      ])
      expect(t.fields[0].options).toEqual(['todo', 'doing', 'done'])
      expect(t.fields[0].schema).toEqual({ title: '状态', type: 'string', enum: ['todo', 'doing', 'done'] })
      const listed = await S.types()
      expect(listed.types.find((x) => x.name === typeName)?.fields).toHaveLength(4)
      expect(Array.isArray(listed.bindings)).toBe(true)
      expect((await failure(S.putType(`bad:${run}`, spec))).code).toBe('invalid')
      expect((await failure(S.putType(`x${run}`, { title: '', fields: [{ key: 'tags', kind: 'text' }] }))).code).toBe(
        'invalid',
      )
      expect(
        (await failure(S.putType(`x${run}`, { title: '', fields: [{ key: 'a', kind: 'select', options: [] }] }))).code,
      ).toBe('invalid')
    })

    it('binds a folder, which a page made there takes as its effective type without a type in its header; unbinding takes it away', async () => {
      expect((await failure(S.bindFolder(`${folder}/nowhere${run}`, typeName))).code).toBe('not_found')
      await S.createFolder(`${folder}/sub`)
      expect((await failure(S.bindFolder(folder, `nothing${run}`))).code).toBe('invalid')
      expect(await S.bindFolder(folder, typeName)).toEqual({ folder, type: typeName })
      expect((await S.types()).bindings).toContainEqual({ folder, type: typeName })
      A = await mk(folder, { title: 'A', status: 'done', points: 5, due: '2026-03-01', labels: ['a'] }, '正文 A\n')
      B = await mk(folder, { title: 'B', status: 'todo', points: 12, due: '2026-01-01', labels: ['b', 'a'] })
      const C = await mk(`${folder}/sub`, { title: 'C', status: 'doing', points: 7.5 })
      const D = await mk(folder, { title: 'D', type: `memo${run}` })
      const E = await mk(`${root}/untyped`, { title: 'E', status: 'todo' })
      expect([A.effectiveType, C.effectiveType, D.effectiveType, E.effectiveType]).toEqual([
        typeName,
        typeName,
        `memo${run}`,
        '',
      ])
      expect('type' in A.meta).toBe(false)
      expect((await S.getPage(A.id)).effectiveType).toBe(typeName)
      const folderItems = await walk({ folder, recursive: true })
      expect(folderItems.find((i) => i.id === C.id)?.effectiveType).toBe(typeName)
      expect(folderItems.find((i) => i.id === D.id)).toMatchObject({ type: `memo${run}`, effectiveType: `memo${run}` })
    })

    it('props=1 adds props and rev to the summaries, and only then', async () => {
      const plain = await S.listPages({ folder, sort: 'title' })
      expect(plain.items[0].props).toBeUndefined()
      expect(plain.items[0].rev).toBeUndefined()
      const withProps = await S.listPages({ folder, sort: 'title', props: true })
      expect(withProps.items[0]).toMatchObject({
        title: 'A',
        rev: A.rev,
        props: { status: 'done', points: 5, due: '2026-03-01', labels: ['a'] },
      })
      expect(withProps.items[2].props).toEqual({})
    })

    it('props=1 says which properties it holds only in part: cut text, an object, a list that lost an element', async () => {
      const whole = await mk(`${root}/partialprops`, { title: 'Whole', status: 'done', labels: ['a'] })
      const cut = await mk(`${root}/partialprops`, {
        title: 'Cut',
        note: '字'.repeat(301),
        obj: { a: 1 },
        list: ['a', { x: 1 }],
        fine: 'ok',
      })
      const items = (await S.listPages({ folder: `${root}/partialprops`, sort: 'title', props: true })).items
      const of = (id: string) => items.find((i) => i.id === id)!
      expect(of(cut.id).partialProps).toEqual(['note', 'obj', 'list'])
      expect(Array.from(String(of(cut.id).props?.note)).length).toBe(300)
      expect(of(cut.id).props).not.toHaveProperty('obj')
      expect(of(whole.id).partialProps).toBeUndefined()
      expect('partialProps' in of(whole.id)).toBe(false)
      const plain = (await S.listPages({ folder: `${root}/partialprops` })).items
      expect(plain.some((i) => 'partialProps' in i)).toBe(false)
    })

    it('sorts by a property both ways, pages without it last, and the cursor walks the whole order', async () => {
      const order = async (sort: ListParams['sort']) =>
        titles(await walk({ folder, recursive: true, sort, props: true }, 1))
      expect(await order('prop:points')).toBe('A C B D')
      expect(await order('-prop:points')).toBe('B C A D')
      expect(await order('prop:due')).toBe('B A C D')
      expect(await order('-prop:due')).toBe('A B C D')
      // A select field, in a folder whose type defines it: the order of its options (todo, doing, done), not of the text.
      expect(await order('prop:status')).toBe('B C A D')
      expect(await order('-prop:status')).toBe('A C B D')
      expect(await order('prop:labels')).toBe('A B C D')
    })

    it('filters by in, range, empty, notEmpty and contains; several conditions all hold; a bad filter is 400', async () => {
      const q = async (f: ListParams['filter'], sort: ListParams['sort'] = 'title') =>
        titles(await walk({ folder, recursive: true, sort, filter: f }))
      expect(await q([{ key: 'status', op: 'in', values: ['todo', 'doing'] }])).toBe('B C')
      expect(await q([{ key: 'labels', op: 'in', values: ['b'] }])).toBe('B')
      expect(await q([{ key: 'points', op: 'range', values: [6, null] }])).toBe('B C')
      expect(await q([{ key: 'due', op: 'range', values: ['2026-02-01', '2026-12-31'] }])).toBe('A')
      expect(await q([{ key: 'due', op: 'empty' }])).toBe('C D')
      expect(await q([{ key: 'due', op: 'notEmpty' }])).toBe('A B')
      expect(await q([{ key: 'status', op: 'contains', values: ['DO'] }])).toBe('A B C')
      expect(
        await q(
          [
            { key: 'status', op: 'in', values: ['todo', 'done'] },
            { key: 'due', op: 'notEmpty' },
          ],
          '-prop:points',
        ),
      ).toBe('B A')
      for (const bad of [
        filter([{ key: 'a', op: 'like', values: ['x'] }]),
        filter([{ key: 'a', op: 'range', values: [1] }]),
        'nope',
      ]) {
        const r = await call(
          admin,
          'GET',
          `${SP_PAGES}?folder=${encodeURIComponent(folder)}&filter=${encodeURIComponent(bad)}`,
        )
        expect([r.status, r.json.error], bad).toEqual([400, 'invalid'])
      }
      expect((await call(admin, 'GET', `${SP_PAGES}?props=maybe`)).status).toBe(400)
    })

    it('changes properties of the header only: body and other keys stay, a list is replaced, unset and blank values remove, the listing sees it at once', async () => {
      const p = await S.patchProps(A.id, {
        baseRev: A.rev,
        set: { status: 'doing', owner: 'Ann', labels: ['b'] },
        unset: ['due'],
      })
      expect(p.rev).not.toBe(A.rev)
      expect(p.body).toBe(A.body)
      expect(p.meta).toMatchObject({ status: 'doing', owner: 'Ann', labels: ['b'], title: 'A', points: 5 })
      expect('due' in p.meta).toBe(false)
      expect(p.effectiveType).toBe(typeName)
      const seen = (await S.listPages({ folder, props: true, filter: [{ key: 'owner', op: 'in', values: ['Ann'] }] }))
        .items
      expect(seen).toHaveLength(1)
      expect(seen[0]).toMatchObject({ rev: p.rev, props: { status: 'doing' } })
      // Blank text removes the key, as unset does; a change to nothing keeps the rev.
      const q = await S.patchProps(A.id, { baseRev: p.rev, set: { owner: '  ' } })
      expect('owner' in q.meta).toBe(false)
      expect((await S.patchProps(A.id, { baseRev: q.rev, set: { status: 'doing' } })).rev).toBe(q.rev)
      A = q
    })

    it('a stale baseRev is a 409 with the current page; malformed requests, built-in keys and unknown pages are refused', async () => {
      const stale = await failure(S.patchProps(A.id, { baseRev: B.rev, set: { status: 'done' } }))
      expect([stale.status, stale.code]).toEqual([409, 'conflict'])
      expect(stale.current?.rev).toBe(A.rev)
      const bad: Record<string, unknown>[] = [
        { set: { x: 1 } },
        { baseRev: A.rev },
        { baseRev: A.rev, set: { title: 'x' } },
        { baseRev: A.rev, unset: ['tags'] },
        { baseRev: A.rev, set: { x: 1 }, unset: ['x'] },
        { baseRev: A.rev, set: [1] },
      ]
      for (const body of bad) {
        const r = await call(admin, 'PATCH', `${SP_PAGES}/${A.id}/props`, body)
        expect([r.status, r.json.error], JSON.stringify(body)).toEqual([400, 'invalid'])
      }
      expect((await failure(S.patchProps('ZZZZZZZZZZ', { baseRev: A.rev, set: { x: 1 } }))).code).toBe('not_found')
      expect((await S.getPage(A.id)).rev).toBe(A.rev)
    })

    it("keeps the caller's draft when properties change", async () => {
      const page = await S.getPage(A.id)
      await S.putDraft(A.id, { baseRev: page.rev, meta: page.meta, body: '打了一半\n', prev: '' })
      const p = await S.patchProps(A.id, { baseRev: page.rev, set: { status: 'done' } })
      const again = await S.getPage(A.id)
      expect(again.draft).toMatchObject({ body: '打了一半\n', stale: true })
      await S.deleteDraft(A.id)
      A = p
    })

    it('changes a type by its keys: the order, titles and options follow the request, a field left out goes, pages keep their values', async () => {
      const t = await S.putType(typeName, {
        baseRev: typeRev,
        title: '任务单',
        fields: [
          { key: 'points', title: '点数' },
          { key: 'status', title: '状态', options: ['todo', 'doing', 'done', 'dropped'] },
        ],
      })
      expect(t.title).toBe('任务单')
      expect(t.rev).not.toBe(typeRev)
      // The old rev is stale now, for a change and for a delete.
      const stale = await failure(S.putType(typeName, { baseRev: typeRev, title: 'late', fields: [] }))
      expect(stale).toMatchObject({ code: 'conflict' })
      expect(stale.current).toMatchObject({ title: '任务单', rev: t.rev })
      expect((await failure(S.deleteType(typeName, typeRev))).code).toBe('conflict')
      typeRev = t.rev
      expect(t.fields.map((f) => f.key)).toEqual(['points', 'status'])
      expect(t.fields[0]).toMatchObject({ kind: 'number', title: '点数' })
      expect(t.fields[1].options).toEqual(['todo', 'doing', 'done', 'dropped'])
      expect((await S.getPage(B.id)).meta.due).toBe('2026-01-01')
    })

    it('deleting a type takes the bindings with it; the pages keep what their headers say; a second delete is 404', async () => {
      await S.deleteType(typeName, typeRev)
      expect((await failure(S.deleteType(typeName, typeRev))).code).toBe('not_found')
      const after = await S.types()
      expect(after.types.some((x) => x.name === typeName)).toBe(false)
      expect(after.bindings.some((b) => b.type === typeName)).toBe(false)
      expect((await S.getPage(A.id)).effectiveType).toBe('')
      const memo = (await walk({ folder, sort: 'title' })).find((i) => i.title === 'D')
      expect(memo?.effectiveType).toBe(`memo${run}`)
    })

    it('a folder renamed carries its binding with it', async () => {
      const t = `mv${run}`
      const made = await S.putType(t, { title: '', fields: [{ key: 'k', kind: 'text' }] })
      await S.createFolder(`${root}/bound`)
      await S.bindFolder(`${root}/bound`, t)
      await S.moveFolder(`${root}/bound`, `${root}/bound2`)
      const { bindings } = await S.types()
      expect(bindings).toContainEqual({ folder: `${root}/bound2`, type: t })
      expect(bindings.some((b) => b.folder === `${root}/bound`)).toBe(false)
      await S.deleteType(t, made.rev)
    })
  })

  describe('saved views, the groups of a board and the bodies of a listing', () => {
    const folder = `${root}/views`
    const typeName = `board${run}`
    const enc = encodeURIComponent
    const viewsUrl = (scope: string) => `/api/spaces/${spaceId}/views?${scope}`
    const getViews = async (scope: string) =>
      (await call(admin, 'GET', viewsUrl(scope))).json as { views: Record<string, any>[]; rev: string }
    const putViews = (body: Record<string, unknown>) => call(admin, 'PUT', `/api/spaces/${spaceId}/views`, body)
    const titles = (items: PageSummary[]) => items.map((i) => i.title).join(' ')
    const listUrl = (query: string) => `${SP_PAGES}?folder=${enc(folder)}&${query}`
    const groupsUrl = (query: string) => `${SP_PAGES}/groups?${query}`
    const options = ['draft', 'review', 'final']
    let typeRev = ''

    it('has no views until some are saved; a save makes the ids and the rev, and reads back the same', async () => {
      const t = await S.putType(typeName, {
        title: '文案',
        fields: [
          { key: 'status', kind: 'select', options },
          { key: 'points', kind: 'number' },
        ],
      })
      typeRev = t.rev
      await S.createFolder(folder)
      await S.bindFolder(folder, typeName)
      const empty = await getViews(`folder=${enc(folder)}`)
      expect(empty.views).toEqual([])
      expect(empty.rev).toMatch(/^[0-9a-f]{40}$/)
      const board = {
        name: '按状态',
        layout: 'board',
        group: 'status',
        sort: '-prop:points',
        filter: [{ key: 'points', op: 'range', values: [1, null] }],
      }
      const table = { name: '表格', layout: 'table', hidden: ['points'], color: 'teal' }
      const saved = await putViews({ folder, baseRev: empty.rev, views: [board, table] })
      expect(saved.status).toBe(200)
      const [a, b] = saved.json.views
      expect(a).toMatchObject({
        name: '按状态',
        layout: 'board',
        group: 'status',
        sort: '-prop:points',
        hidden: [],
        filter: [{ key: 'points', op: 'range', values: [1, null] }],
      })
      expect(b).toMatchObject({
        name: '表格',
        layout: 'table',
        group: '',
        sort: '',
        filter: [],
        hidden: ['points'],
        color: 'teal',
      })
      expect(a.id).toMatch(/^[a-z0-9]{4,}$/)
      expect(a.id).not.toBe(b.id)
      expect(saved.json.rev).not.toBe(empty.rev)
      expect(await getViews(`folder=${enc(folder)}`)).toEqual(saved.json)
      expect((await getViews(`folder=${enc(`${folder}/elsewhere`)}`)).views).toEqual([])
    })

    it('a save from an old rev is a 409 with the views as they are; kept views keep their ids, a view that is left out goes', async () => {
      const cur = await getViews(`folder=${enc(folder)}`)
      const stale = await putViews({ folder, baseRev: '0'.repeat(40), views: [] })
      expect([stale.status, stale.json.error]).toEqual([409, 'conflict'])
      expect(stale.json.current).toEqual(cur)
      const [a, b] = cur.views
      const next = await putViews({
        folder,
        baseRev: cur.rev,
        views: [b, { ...a, name: '看板' }, { name: '连读', layout: 'read' }],
      })
      expect(next.status).toBe(200)
      expect(next.json.views.map((v: any) => v.name)).toEqual(['表格', '看板', '连读'])
      expect([next.json.views[0].id, next.json.views[1].id]).toEqual([b.id, a.id])
      expect(next.json.views[2].id).not.toBe('')
      expect(next.json.views[0].color).toBe('teal')
      const dropped = await putViews({ folder, baseRev: next.json.rev, views: [next.json.views[1]] })
      expect(dropped.json.views.map((v: any) => v.name)).toEqual(['看板'])
      // An id is made by the server and is unique in the space: another folder's id cannot be claimed.
      const other = `${root}/views2`
      await S.createFolder(other)
      const mine = await getViews(`folder=${enc(other)}`)
      const claim = await putViews({ folder: other, baseRev: mine.rev, views: [{ ...dropped.json.views[0] }] })
      expect([claim.status, claim.json.error]).toEqual([400, 'invalid'])
      const made = await putViews({ folder: other, baseRev: mine.rev, views: [{ name: 'x', layout: 'list' }] })
      expect(made.json.views[0].id).not.toBe(dropped.json.views[0].id)
    })

    it('refuses what is malformed: the limits of a view, 21 views, an unknown id, no scope or both, a folder that is not there', async () => {
      const cur = await getViews(`folder=${enc(folder)}`)
      const ok = { name: 'v', layout: 'list' }
      const many = (n: number) => Array.from({ length: n }, (_, i) => ({ name: `v${i}`, layout: 'list' }))
      const bad: [string, Record<string, unknown>, number, string][] = [
        ['no baseRev', { folder, views: [] }, 400, 'invalid'],
        ['no views', { folder, baseRev: cur.rev }, 400, 'invalid'],
        ['no scope', { baseRev: cur.rev, views: [] }, 400, 'invalid'],
        ['both scopes', { folder, tag: 't', baseRev: cur.rev, views: [] }, 400, 'invalid'],
        ['no name', { folder, baseRev: cur.rev, views: [{ layout: 'list' }] }, 400, 'invalid'],
        ['a layout that is none', { folder, baseRev: cur.rev, views: [{ ...ok, layout: 'gantt' }] }, 400, 'invalid'],
        ['a sort that is none', { folder, baseRev: cur.rev, views: [{ ...ok, sort: 'size' }] }, 400, 'invalid'],
        [
          'a filter that is none',
          { folder, baseRev: cur.rev, views: [{ ...ok, filter: [{ key: 'a', op: 'like' }] }] },
          400,
          'invalid',
        ],
        ['hidden twice', { folder, baseRev: cur.rev, views: [{ ...ok, hidden: ['a', 'a'] }] }, 400, 'invalid'],
        ['an id nobody has', { folder, baseRev: cur.rev, views: [{ ...ok, id: 'zzzzzz' }] }, 400, 'invalid'],
        ['21 views', { folder, baseRev: cur.rev, views: many(21) }, 400, 'invalid'],
        ['a folder that is not there', { folder: `${folder}/nowhere`, baseRev: cur.rev, views: [] }, 404, 'not_found'],
      ]
      for (const [what, body, status, error] of bad) {
        const r = await putViews(body)
        expect([r.status, r.json?.error], what).toEqual([status, error])
      }
      expect((await putViews({ folder, baseRev: cur.rev, views: many(20) })).json.views).toHaveLength(20)
      expect(
        (await putViews({ folder, baseRev: (await getViews(`folder=${enc(folder)}`)).rev, views: [ok] })).status,
      ).toBe(200)
      for (const q of ['', 'folder=a&tag=b']) expect((await call(admin, 'GET', viewsUrl(q))).status, q).toBe(400)
    })

    it('a tag has its own views, which need not be used by any page', async () => {
      const tag = `标签${run}`
      const none = await getViews(`tag=${enc(tag)}`)
      expect(none.views).toEqual([])
      const saved = await putViews({ tag, baseRev: none.rev, views: [{ name: '连读', layout: 'read' }] })
      expect(saved.status).toBe(200)
      expect(await getViews(`tag=${enc(tag)}`)).toEqual(saved.json)
      expect((await putViews({ tag, baseRev: none.rev, views: [] })).status).toBe(409)
      // The colour of the tag and its views do not get in each other's way.
      await S.setTagColor(tag, 'rose')
      expect((await getViews(`tag=${enc(tag)}`)).views).toHaveLength(1)
      await S.setTagColor(tag, null)
      expect((await getViews(`tag=${enc(tag)}`)).views).toHaveLength(1)
    })

    it('views follow their folder when it is renamed, and go when it is deleted', async () => {
      const from = `${root}/vmove`
      await S.createFolder(`${from}/sub`)
      const top = await putViews({
        folder: from,
        baseRev: (await getViews(`folder=${enc(from)}`)).rev,
        views: [{ name: '上', layout: 'list' }],
      })
      const low = await putViews({
        folder: `${from}/sub`,
        baseRev: (await getViews(`folder=${enc(`${from}/sub`)}`)).rev,
        views: [{ name: '下', layout: 'board', group: 'status' }],
      })
      await S.moveFolder(from, `${root}/vmoved`)
      expect(await getViews(`folder=${enc(`${root}/vmoved`)}`)).toEqual(top.json)
      expect((await getViews(`folder=${enc(`${root}/vmoved/sub`)}`)).views[0]).toMatchObject({
        id: low.json.views[0].id,
        name: '下',
        group: 'status',
      })
      expect((await getViews(`folder=${enc(from)}`)).views).toEqual([])
      await S.deleteFolder(`${root}/vmoved`, true)
      expect((await getViews(`folder=${enc(`${root}/vmoved`)}`)).views).toEqual([])
      expect((await getViews(`folder=${enc(`${root}/vmoved/sub`)}`)).views).toEqual([])
    })

    it('is reachable through the typed client: views, a save, a stale save with the views as they are, and the groups', async () => {
      const place = `${root}/typed${run}`
      await S.createFolder(place)
      const none = await S.views({ folder: place })
      expect(none.views).toEqual([])
      const saved = await S.putViews(
        { folder: place },
        {
          baseRev: none.rev,
          views: [
            { name: '一', layout: 'list', sort: '', group: '', filter: [], hidden: [] },
            {
              name: '二',
              layout: 'board',
              sort: '',
              group: 'status',
              filter: [{ key: 'status', op: 'notIn', values: ['draft'] }],
              hidden: [],
              cardFields: ['points'],
            },
          ],
        },
      )
      expect(saved.views.map((v) => v.name)).toEqual(['一', '二'])
      expect(saved.views[1]).toMatchObject({
        group: 'status',
        cardFields: ['points'],
        filter: [{ key: 'status', op: 'notIn', values: ['draft'] }],
      })
      expect(await S.views({ folder: place })).toEqual(saved)
      // A save from the rev before is refused, and the error carries the views as they are.
      const stale = await S.putViews({ folder: place }, { baseRev: none.rev, views: [] }).then(
        () => null,
        (e: unknown) => e as ApiError,
      )
      expect([stale?.status, stale?.code]).toEqual([409, 'conflict'])
      expect((stale?.current as unknown as { views: unknown[]; rev: string }).rev).toBe(saved.rev)
      expect((stale?.current as unknown as { views: unknown[] }).views).toHaveLength(2)
      // A view of a tag is kept under the tag.
      const tag = `typedtag${run}`
      const tagViews = await S.putViews(
        { tag },
        {
          baseRev: (await S.views({ tag })).rev,
          views: [{ name: '读', layout: 'read', sort: '', group: '', filter: [], hidden: [] }],
        },
      )
      expect((await S.views({ tag })).views[0].id).toBe(tagViews.views[0].id)
      // The groups of a folder: how many pages hold each value.
      await mk(place, { title: 'g1', status: 'draft' }, '')
      await mk(place, { title: 'g2', status: 'draft' }, '')
      await mk(place, { title: 'g3' }, '')
      expect(await S.groups({ folder: place, key: 'status' })).toEqual({
        groups: [{ value: 'draft', count: 2 }],
        empty: 1,
        truncated: false,
      })
      expect((await S.groups({ tag: [tag], key: 'status' })).groups).toEqual([])
    })

    describe('a board over a select field', () => {
      const pages: Record<string, Page> = {}
      const filter = (f: unknown[]) => `filter=${enc(JSON.stringify(f))}`
      it('lists each column on its own: the options, the other values, the empty', async () => {
        for (const [title, meta] of [
          ['甲', { status: 'final', points: 3 }],
          ['乙', { status: 'draft', points: 9 }],
          ['丙', { status: 'review' }],
          ['丁', { status: 'legacy' }],
          ['戊', {}],
          ['己', { status: 'draft' }],
          ['庚', { status: ['draft', 'x'] }],
        ] as [string, Record<string, unknown>][]) {
          pages[title] = await mk(folder, { title, ...meta }, `正文 ${title}\n`)
        }
        const col = async (f: unknown[]) =>
          titles((await call(admin, 'GET', listUrl(`${filter(f)}&sort=title&limit=20`))).json.items)
        expect(await col([{ key: 'status', op: 'in', values: ['draft'] }])).toBe('乙 己 庚')
        expect(await col([{ key: 'status', op: 'in', values: ['review'] }])).toBe('丙')
        // Not empty and not an option: a page that holds one of the options (a list) is in that column.
        expect(await col([{ key: 'status', op: 'notIn', values: options }])).toBe('丁')
        expect(await col([{ key: 'status', op: 'notIn', values: ['draft'] }])).toBe('丁 丙 甲')
        expect(await col([{ key: 'status', op: 'empty' }])).toBe('戊')
        const r = await call(admin, 'GET', listUrl(`${filter([{ key: 'status', op: 'notIn' }])}`))
        expect([r.status, r.json.error]).toEqual([400, 'invalid'])
      })

      it('counts the pages of each column from the index: values, the empty, and a filter', async () => {
        const g = (await call(admin, 'GET', groupsUrl(`folder=${enc(folder)}&key=status`))).json
        expect(g.groups).toEqual([
          { value: 'draft', count: 3 },
          { value: 'final', count: 1 },
          { value: 'legacy', count: 1 },
          { value: 'review', count: 1 },
          { value: 'x', count: 1 },
        ])
        expect([g.empty, g.truncated]).toEqual([1, false])
        const some = (
          await call(
            admin,
            'GET',
            groupsUrl(`folder=${enc(folder)}&key=status&${filter([{ key: 'points', op: 'notEmpty' }])}`),
          )
        ).json
        expect(some.groups).toEqual([
          { value: 'draft', count: 1 },
          { value: 'final', count: 1 },
        ])
        expect(some.empty).toBe(0)
        expect((await call(admin, 'GET', groupsUrl(`folder=${enc(folder)}&key=nothing${run}`))).json).toMatchObject({
          groups: [],
          empty: 7,
        })
        for (const q of [
          'folder=a',
          'key=status',
          `folder=${enc(folder)}&key=s&recursive=maybe`,
          `folder=${enc(folder)}&key=s&filter=nope`,
        ]) {
          const r = await call(admin, 'GET', groupsUrl(q))
          expect([r.status, r.json.error], q).toEqual([400, 'invalid'])
        }
      })

      it('sorts by the options of the field when the folder has it as a select: options, other values, none; both ways, by cursor', async () => {
        const walkTitles = async (sort: string, limit: number) =>
          titles(await walk({ folder, sort: sort as never }, limit))
        // draft: 乙 己 庚 (a list sorts by its first value); review; final; legacy; none.
        for (const limit of [1, 2, 20]) {
          expect(await walkTitles('prop:status', limit)).toBe('乙 己 庚 丙 甲 丁 戊')
          expect(await walkTitles('-prop:status', limit)).toBe('丁 甲 丙 庚 己 乙 戊')
        }
        // A field the type does not give as a select sorts by its text; so does a listing without the folder.
        expect(await walkTitles('prop:points', 20)).toBe('甲 乙 丙 丁 戊 己 庚')
        const noFolder = (
          await call(
            admin,
            'GET',
            `${SP_PAGES}?${filter([{ key: 'status', op: 'in', values: options }])}&sort=prop:status&limit=50`,
          )
        ).json.items as PageSummary[]
        expect(titles(noFolder.filter((i) => i.folder === folder))).toBe('乙 己 庚 甲 丙')
        // A cursor of the order of the options is no cursor of another sort.
        const first = (await call(admin, 'GET', listUrl('sort=prop:status&limit=1'))).json
        const other = await call(admin, 'GET', listUrl(`sort=-prop:status&limit=1&cursor=${enc(first.next)}`))
        expect([other.status, other.json.error]).toEqual([400, 'invalid'])
      })

      it('an option reordered in the type changes the order at once', async () => {
        const t = await S.putType(typeName, {
          baseRev: typeRev,
          title: '文案',
          fields: [{ key: 'status', options: ['final', 'review', 'draft'] }, { key: 'points' }],
        })
        typeRev = t.rev
        expect(titles(await walk({ folder, sort: 'prop:status' }, 20))).toBe('甲 丙 乙 己 庚 丁 戊')
      })
    })

    it('gives the Markdown of each page with body=1, 20 at a time at most, and not otherwise', async () => {
      const plain = (await call(admin, 'GET', listUrl('sort=title&limit=3'))).json.items
      expect(plain.some((i: any) => 'body' in i)).toBe(false)
      const r = await call(admin, 'GET', listUrl('sort=title&body=1&limit=3'))
      expect(r.status).toBe(200)
      expect(r.json.items).toHaveLength(3)
      for (const it of r.json.items) expect(it.body).toBe(`正文 ${it.title}\n`)
      // With props too; the default is 20.
      const withProps = (await call(admin, 'GET', listUrl('sort=title&body=1&props=1'))).json
      expect(withProps.items.length).toBeLessThanOrEqual(20)
      expect(withProps.items[0]).toMatchObject({ body: expect.any(String), rev: expect.any(String) })
      for (const q of ['limit=21&body=1', 'limit=200&body=1', 'limit=0&body=1', 'body=yes']) {
        const bad = await call(admin, 'GET', listUrl(q))
        expect([bad.status, bad.json.error], q).toEqual([400, 'invalid'])
      }
      // The next 20 by cursor.
      for (let i = 0; i < 22; i++)
        await mk(`${folder}/long`, { title: `长${String(i).padStart(2, '0')}` }, `第 ${i} 篇\n`)
      const first = (await call(admin, 'GET', `${SP_PAGES}?folder=${enc(`${folder}/long`)}&sort=title&body=1`)).json
      expect(first.items).toHaveLength(20)
      const rest = (
        await call(
          admin,
          'GET',
          `${SP_PAGES}?folder=${enc(`${folder}/long`)}&sort=title&body=1&cursor=${enc(first.next)}`,
        )
      ).json
      expect(rest.items.map((i: any) => i.body)).toEqual(['第 20 篇\n', '第 21 篇\n'])
      expect(rest.next).toBe('')
    })
  })

  describe('templates', () => {
    const enc = encodeURIComponent
    const folder = `${root}/tpl`
    const names = { diary: `日记${run}`, quick: `随记${run}`, tiny: `小${run}` }
    const url = (name = '') => `/api/spaces/${spaceId}/templates${name ? '/' + enc(name) : ''}`
    const put = (name: string, baseRev: string, meta: Record<string, unknown>, body: string) =>
      call(admin, 'PUT', url(name), { baseRev, meta, body })
    const list = async () =>
      (await call(admin, 'GET', url())).json as {
        templates: Record<string, any>[]
        defaults: { folders: Record<string, string>; quickNote: string }
      }
    const defaults = (body: Record<string, unknown>) => call(admin, 'PUT', url('defaults'), body)
    const day = (tz: number) => new Date(Date.now() + tz * 60_000).toISOString().slice(0, 10)
    let rev = ''

    it('makes a template and reads it back: path, rev, header and body; the list has no body; a stale or repeated create is a 409 with the current one', async () => {
      const meta = { title: '{{date}} 日记', tags: ['日记', 'd-{{date}}'], type: `diary${run}`, mood: '{{time}}' }
      const body = '## 今天 {{time}}\n{{datetime}} {{nope}} {{ date }}\n'
      const made = await put(names.diary, '', meta, body)
      expect(made.status).toBe(200)
      expect(made.json).toMatchObject({ name: names.diary, path: `.petalbook/templates/${names.diary}.md`, meta, body })
      expect(made.json.rev).toMatch(/^[0-9a-f]{40}$/)
      rev = made.json.rev
      expect((await call(admin, 'GET', url(names.diary))).json).toEqual(made.json)
      const listed = (await list()).templates.find((t) => t.name === names.diary)
      expect(listed).toEqual({ name: names.diary, path: made.json.path, rev, meta })
      for (const baseRev of ['', '0'.repeat(40)]) {
        const again = await put(names.diary, baseRev, meta, 'x')
        expect([again.status, again.json.error]).toEqual([409, 'conflict'])
        expect(again.json.current).toEqual(made.json)
      }
      const gone = await put(`没有${run}`, '0'.repeat(40), {}, '')
      expect([gone.status, gone.json.error, gone.json.current]).toEqual([409, 'conflict', undefined])
      const changed = await put(names.diary, rev, meta, body + '更多\n')
      expect(changed.status).toBe(200)
      expect(changed.json.rev).not.toBe(rev)
      rev = changed.json.rev
      expect((await put(names.diary, rev, meta, body + '更多\n')).json.rev).toBe(rev)
      const missing = await call(admin, 'GET', url(`没有${run}`))
      expect([missing.status, missing.json.error]).toEqual([404, 'not_found'])
    })

    it('checks names and size: a name that is not a file name 400 invalid, over 256 KiB 400 template_too_large', async () => {
      for (const bad of ['a:b', '.hidden', 'con', 'x'.repeat(90).replaceAll('x', '长'), 'defaults']) {
        const r = await call(admin, 'PUT', url(bad), { baseRev: '', meta: {}, body: '' })
        expect([r.status, r.json.error], bad).toEqual([400, 'invalid'])
      }
      const big = await put(names.tiny, '', {}, 'x'.repeat((256 << 10) + 1))
      expect([big.status, big.json.error]).toEqual([400, 'template_too_large'])
      expect((await call(admin, 'GET', url(names.tiny))).status).toBe(404)
      const fits = await put(names.tiny, '', {}, 'x'.repeat(200 << 10))
      expect(fits.status).toBe(200)
      expect((await call(admin, 'DELETE', `${url(names.tiny)}?baseRev=${fits.json.rev}`)).status).toBe(204)
    })

    it('has at most 200 templates: the 201st is 400 template_limit, one that is there can still be changed', async () => {
      const have = (await list()).templates.length
      const made: Record<string, string> = {}
      try {
        for (let i = have; i < 200; i++) {
          const r = await put(`限${run}-${i}`, '', {}, 'x')
          expect(r.status).toBe(200)
          made[`限${run}-${i}`] = r.json.rev
        }
        const over = await put(`限${run}-over`, '', {}, 'x')
        expect([over.status, over.json.error]).toEqual([400, 'template_limit'])
        const first = Object.keys(made)[0]
        expect((await put(first, made[first], {}, 'y')).status).toBe(200)
        made[first] = (await call(admin, 'GET', url(first))).json.rev
      } finally {
        for (const [name, r] of Object.entries(made)) await call(admin, 'DELETE', `${url(name)}?baseRev=${r}`)
      }
    })

    it('sets the default template of a folder and of quick notes; an unknown template is 400 template_not_found, a folder that is not there 404', async () => {
      await S.createFolder(folder)
      const missing = await defaults({ folder, template: `没有${run}` })
      expect([missing.status, missing.json.error]).toEqual([400, 'template_not_found'])
      expect([(await defaults({ quickNote: `没有${run}` })).json.error]).toEqual(['template_not_found'])
      const noFolder = await defaults({ folder: `${root}/nowhere`, template: names.diary })
      expect(noFolder.status).toBe(404)
      for (const body of [{}, { folder }, { folder, template: names.diary, quickNote: names.diary }])
        expect((await defaults(body)).json.error).toBe('invalid')
      const set = await defaults({ folder, template: names.diary })
      expect(set.status).toBe(200)
      expect(set.json.folders[folder]).toBe(names.diary)
      await put(names.quick, '', { title: '随记 {{time}}' }, '')
      const quick = await defaults({ quickNote: names.quick })
      expect(quick.json.quickNote).toBe(names.quick)
      expect((await list()).defaults).toEqual(quick.json)
    })

    it('makes a page from a template: variables in the title, header values and body in the client time zone; templates are not pages and not found by search', async () => {
      for (const tz of [0, 480, -480, 840, -840]) {
        const before = day(tz)
        const p = await S.createPage({ folder: `${folder}/tz`, template: names.diary, tz })
        const after = day(tz)
        expect([`${before} 日记`, `${after} 日记`]).toContain(p.meta.title)
        expect(p.meta.type).toBe(`diary${run}`)
        expect(p.meta.mood).toMatch(/^\d\d:\d\d$/)
        expect((p.meta.tags as string[])[0]).toBe('日记')
        expect([`d-${before}`, `d-${after}`]).toContain((p.meta.tags as string[])[1])
        expect(p.body).toMatch(/^## 今天 \d\d:\d\d\n\d{4}-\d\d-\d\d \d\d:\d\d \{\{nope\}\} \{\{ date \}\}\n更多\n$/)
      }
      const bad = await call(admin, 'POST', SP_PAGES, { folder, template: names.diary, tz: 841 })
      expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
      const unknown = await call(admin, 'POST', SP_PAGES, { folder, template: `没有${run}` })
      expect([unknown.status, unknown.json.error]).toEqual([400, 'template_not_found'])
      const all = await walk({ folder: '', recursive: true }, 100)
      expect(all.filter((i) => i.path.includes('.petalbook') || i.path.includes('templates'))).toEqual([])
      expect((await S.listPages({ q: 'nope' })).items.filter((i) => i.path.includes('.petalbook'))).toEqual([])
    })

    it('the request wins field by field and the template\'s type is kept; a folder\'s default applies unless "" is sent; quick notes use the quick-note default', async () => {
      const mine = await S.createPage({
        folder: `${folder}/p`,
        template: names.diary,
        meta: { title: '我的', type: 'other', extra: 1, tags: [] },
        body: '自己的\n',
      })
      expect(mine.meta).toMatchObject({ title: '我的', type: `diary${run}`, extra: 1 })
      expect(mine.meta.tags).toHaveLength(2)
      expect(mine.body).toBe('自己的\n')
      const tags = await S.createPage({ folder: `${folder}/p`, template: names.diary, meta: { tags: ['我的'] } })
      expect(tags.meta.tags).toEqual(['我的'])
      expect(tags.body).toContain('## 今天')
      const byDefault = await S.createPage({ folder })
      expect(byDefault.meta.type).toBe(`diary${run}`)
      const blank = await S.createPage({ folder, template: '' })
      expect([blank.meta.type, blank.body]).toEqual([undefined, ''])
      const inbox = (await call(admin, 'GET', '/api/spaces')).json.spaces.find((s: SpaceInfo) => s.id === spaceId)
        .inbox as string
      const note = await S.createPage({ folder: inbox, body: '想到的\n' })
      expect(String(note.meta.title)).toMatch(/^随记 \d\d:\d\d$/)
      expect(note.body).toBe('想到的\n')
      const plain = await S.createPage({ folder: inbox, template: '' })
      expect(plain.meta.title).toBeUndefined()
      expect((await defaults({ quickNote: '' })).json.quickNote).toBe('')
    })

    it('the defaults follow their folder when it is moved, and go when it is deleted', async () => {
      const a = `${root}/tplmv`
      await S.createFolder(`${a}/in`)
      await defaults({ folder: a, template: names.diary })
      await defaults({ folder: `${a}/in`, template: names.quick })
      const moved = `${root}/tplmoved`
      expect((await call(admin, 'POST', `/api/spaces/${spaceId}/folders/move`, { from: a, to: moved })).status).toBe(
        200,
      )
      const d = (await list()).defaults.folders
      expect([d[a], d[`${a}/in`], d[moved], d[`${moved}/in`]]).toEqual([undefined, undefined, names.diary, names.quick])
      expect((await S.createPage({ folder: moved })).meta.type).toBe(`diary${run}`)
      expect(
        (await call(admin, 'DELETE', `/api/spaces/${spaceId}/folders?path=${enc(moved)}&recursive=true`)).status,
      ).toBe(204)
      const after = (await list()).defaults.folders
      expect([after[moved], after[`${moved}/in`]]).toEqual([undefined, undefined])
    })

    it('deleting a template needs its rev, and takes the defaults that name it away in the same change', async () => {
      const stale = await call(admin, 'DELETE', `${url(names.diary)}?baseRev=${'0'.repeat(40)}`)
      expect([stale.status, stale.json.error]).toEqual([409, 'conflict'])
      expect(stale.json.current.rev).toBe(rev)
      await defaults({ quickNote: names.diary })
      expect((await list()).defaults.folders[folder]).toBe(names.diary)
      expect((await call(admin, 'DELETE', `${url(names.diary)}?baseRev=${rev}`)).status).toBe(204)
      const l = await list()
      expect(l.templates.map((t) => t.name)).not.toContain(names.diary)
      expect(l.defaults.folders[folder]).toBeUndefined()
      expect(l.defaults.quickNote).toBe('')
      expect((await call(admin, 'GET', url(names.diary))).status).toBe(404)
      expect((await call(admin, 'DELETE', `${url(names.diary)}?baseRev=${rev}`)).status).toBe(404)
      const q = (await call(admin, 'GET', url(names.quick))).json
      expect((await call(admin, 'DELETE', `${url(names.quick)}?baseRev=${q.rev}`)).status).toBe(204)
    })
  })

  describe('the check page', () => {
    const folder = `${root}/checks`
    const typeName = `chk${run}`
    const mine = (items: Problem[]) => items.filter((p) => p.path.startsWith(`${folder}/`))
    const walkChecks = async (kind?: ProblemKind) => {
      const items: Problem[] = []
      let cursor = ''
      for (let guard = 0; guard < 100; guard++) {
        const r = await S.checks({ kind, cursor: cursor || undefined })
        expect(r.items.length).toBeLessThanOrEqual(50)
        items.push(...r.items)
        cursor = r.next
        if (!cursor) return items
      }
      throw new Error('cursor never ended')
    }
    const of = (items: Problem[], kind: ProblemKind) => mine(items).filter((p) => p.kind === kind)
    const at = (items: Problem[], kind: ProblemKind, path: string) =>
      mine(items).find((p) => p.kind === kind && p.path === path)
    let rev = ''
    let strayId = ''

    it('counts every kind, listed or not, and lists in the order of the kinds, then paths', async () => {
      await S.createFolder(folder)
      const t = await S.putType(typeName, {
        title: '检查',
        fields: [
          { key: 'status', kind: 'select', options: ['todo', 'done'] },
          { key: 'points', kind: 'number' },
        ],
      })
      expect(t.rev).toMatch(/^[0-9a-f]{40}$/)
      await S.bindFolder(folder, typeName)
      const r = await S.checks()
      expect(Object.keys(r.counts).sort()).toEqual([
        'bad_header',
        'broken_link',
        'duplicate_id',
        'duplicate_title',
        'missing_asset',
        'no_id',
        'not_in_type',
        'type_mismatch',
      ])
      for (const n of Object.values(r.counts)) expect(Number.isInteger(n)).toBe(true)
      const order = [
        'no_id',
        'duplicate_id',
        'bad_header',
        'type_mismatch',
        'not_in_type',
        'broken_link',
        'missing_asset',
        'duplicate_title',
      ]
      const items = await walkChecks()
      const keys = items.map((p) => [order.indexOf(p.kind), p.path] as const)
      for (let i = 1; i < keys.length; i++)
        expect(keys[i - 1][0] < keys[i][0] || (keys[i - 1][0] === keys[i][0] && keys[i - 1][1] <= keys[i][1])).toBe(
          true,
        )
      for (const p of items) {
        expect(typeof p.message).toBe('string')
        expect(p.fix === null || p.fix === 'add_id' || p.fix === 'remove_fields').toBe(true)
        expect(p.rev).toMatch(/^[0-9a-f]{40}$/)
      }
    })

    it('finds fields a type does not have and values that do not fit it, per page, and says what fixes what', async () => {
      const before = (await S.checks()).counts
      const fine = await mk(folder, { title: `好${run}`, status: 'todo', points: 3 }, 'x\n')
      const stray = await mk(folder, { title: `多${run}`, status: 'todo', colour: 'red', size: 3, slug: 's' }, 'x\n')
      const bad = await mk(folder, { title: `错${run}`, status: 'later', points: 'many' }, 'x\n')
      const both = await mk(folder, { title: `两${run}`, status: 'nope', extra: 1 }, 'x\n')
      strayId = stray.id
      rev = stray.rev
      const items = await walkChecks()
      expect(mine(items).filter((p) => p.id === fine.id)).toEqual([])
      const s = at(items, 'not_in_type', stray.path)!
      expect(s).toMatchObject({ id: stray.id, fix: 'remove_fields', rev: stray.rev })
      expect(s.detail).toEqual({ fields: ['colour', 'size'] })
      expect(s.message).not.toBe('')
      const m = at(items, 'type_mismatch', bad.path)!
      expect(m).toMatchObject({ id: bad.id, fix: null })
      expect((m.detail!.fields as string[]).sort()).toEqual(['points', 'status'])
      expect(at(items, 'type_mismatch', both.path)!.detail).toEqual({ fields: ['status'] })
      expect(at(items, 'not_in_type', both.path)!.detail).toEqual({ fields: ['extra'] })
      expect(of(items, 'not_in_type').map((p) => p.path)).toEqual([both.path, stray.path].sort())
      const after = (await S.checks()).counts
      expect(after.not_in_type - before.not_in_type).toBe(2)
      expect(after.type_mismatch - before.type_mismatch).toBe(2)
      // A save that repairs one page and breaks another moves the counts at once.
      const repaired = await S.savePage(bad.id, {
        baseRev: bad.rev,
        meta: { ...bad.meta, status: 'done', points: 4 },
        body: bad.body,
      })
      await S.savePage(fine.id, { baseRev: fine.rev, meta: { ...fine.meta, odd: true }, body: fine.body })
      const next = await walkChecks()
      expect(at(next, 'type_mismatch', repaired.path)).toBeUndefined()
      expect(at(next, 'not_in_type', fine.path)!.detail).toEqual({ fields: ['odd'] })
      expect((await S.checks()).counts.not_in_type - before.not_in_type).toBe(3)
    })

    it('finds two pages of one folder with one title, not in two folders, not untitled ones', async () => {
      const before = (await S.checks()).counts.duplicate_title
      const a = await mk(`${folder}/dup`, { title: `同名${run}` }, '1\n')
      const b = await mk(`${folder}/dup`, { title: `同名${run}` }, '2\n')
      await mk(`${folder}/other`, { title: `同名${run}` }, '3\n')
      await mk(`${folder}/dup`, {}, '无题 一\n')
      await mk(`${folder}/dup`, {}, '无题 二\n')
      const items = of(await walkChecks('duplicate_title'), 'duplicate_title')
      expect(items.map((p) => p.path)).toEqual([a.path, b.path].sort())
      expect(items[0].detail).toEqual({ paths: [a.path, b.path].sort() })
      expect(items[0].fix).toBeNull()
      expect((await S.checks()).counts.duplicate_title - before).toBe(2)
      // One of them is retitled: the group is gone.
      await S.savePage(b.id, { baseRev: b.rev, meta: { ...b.meta, title: `另一个${run}` }, body: b.body })
      expect(of(await walkChecks('duplicate_title'), 'duplicate_title')).toEqual([])
      expect((await S.checks()).counts.duplicate_title).toBe(before)
    })

    it('finds an image or attachment of assets/ that is not there, and forgets it when the page stops pointing at it', async () => {
      const before = (await S.checks()).counts.missing_asset
      const p = await mk(
        folder,
        { title: `图${run}` },
        '![x](/assets/NOSUCHPAGE/gone.png) [f](/assets/NOSUCHPAGE/gone.pdf) [web](https://example.test/assets/a/b.png)\n',
      )
      const items = of(await walkChecks('missing_asset'), 'missing_asset')
      expect(items.map((x) => x.detail)).toEqual([
        { target: '/assets/NOSUCHPAGE/gone.png' },
        { target: '/assets/NOSUCHPAGE/gone.pdf' },
      ])
      expect(items.every((x) => x.id === p.id && x.path === p.path && x.fix === null)).toBe(true)
      expect((await S.checks()).counts.missing_asset - before).toBe(2)
      await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: 'no pictures\n' })
      expect((await S.checks()).counts.missing_asset).toBe(before)
    })

    it('finds links to pages that do not exist, by id or by path, and forgets them when the page appears or the link goes', async () => {
      const before = (await S.checks()).counts.broken_link
      const p = await mk(folder, { title: `断${run}` }, `[无](没有${run}.md)\n\n[别](别处${run}.md "01JZZZZZZZ")\n`)
      const items = of(await walkChecks('broken_link'), 'broken_link').filter((x) => x.path === p.path)
      expect(items.map((x) => x.detail!.target)).toEqual([`${folder}/没有${run}.md`, `${folder}/别处${run}.md`])
      expect(items.every((x) => x.id === p.id && x.rev === p.rev && x.fix === null && x.message !== '')).toBe(true)
      const [l1, l2] = items.map((x) => x.detail!.line as number)
      expect(Number.isInteger(l1) && l1 > 1 && l2 === l1 + 2).toBe(true)
      expect((await S.checks()).counts.broken_link - before).toBe(2)
      // The page the path names is made: that link is fine; an id is not found by its path.
      await mk(folder, { title: `没有${run}` }, 'y\n')
      await mk(folder, { title: `别处${run}` }, 'y\n')
      const left = of(await walkChecks('broken_link'), 'broken_link').filter((x) => x.path === p.path)
      expect(left.map((x) => x.detail!.target)).toEqual([`${folder}/别处${run}.md`])
      await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: 'no links\n' })
      expect((await S.checks()).counts.broken_link).toBe(before)
    })

    it('pages 50 at a time with a cursor, one kind or all of them; a kind or cursor it does not know is 400', async () => {
      const before = (await S.checks()).counts.not_in_type
      const made: string[] = []
      for (let i = 0; i < 55; i++)
        made.push((await mk(`${folder}/many`, { title: `多${i}-${run}`, stray: i + 1 }, 'x\n')).path)
      const first = await S.checks({ kind: 'not_in_type' })
      expect(first.items).toHaveLength(50)
      expect(first.next).not.toBe('')
      expect(first.counts.not_in_type - before).toBe(55)
      const all = await walkChecks('not_in_type')
      expect(all.length).toBe(first.counts.not_in_type)
      expect(
        of(all, 'not_in_type')
          .filter((p) => p.path.startsWith(`${folder}/many/`))
          .map((p) => p.path),
      ).toEqual([...made].sort())
      const every = await walkChecks()
      expect(every.length).toBe(Object.values((await S.checks()).counts).reduce((a, b) => a + b, 0))
      for (const query of ['?kind=nope', '?cursor=nope']) {
        const r = await call(admin, 'GET', `/api/spaces/${spaceId}/checks${query}`)
        expect([r.status, r.json.error]).toEqual([400, 'invalid'])
      }
    })

    it('fixes not_in_type: only the listed fields go and the rest of the header stays; a stale rev is 409; other kinds are 400 not_fixable', async () => {
      const cur = await S.getPage(strayId)
      expect(cur.rev).toBe(rev)
      const stale = await failure(S.fixCheck({ kind: 'not_in_type', path: cur.path, baseRev: '0'.repeat(40) }))
      expect([stale.status, stale.code]).toEqual([409, 'conflict'])
      expect(stale.current).toMatchObject({ id: strayId, rev })
      for (const kind of [
        'type_mismatch',
        'duplicate_id',
        'bad_header',
        'broken_link',
        'missing_asset',
        'duplicate_title',
        'nope',
      ]) {
        const e = await failure(S.fixCheck({ kind: kind as 'no_id', path: cur.path, baseRev: cur.rev }))
        expect([e.status, e.code], kind).toEqual([400, 'not_fixable'])
      }
      const gone = await failure(S.fixCheck({ kind: 'not_in_type', path: `${folder}/nowhere.md`, baseRev: cur.rev }))
      expect(gone.status).toBe(404)
      const fixed = await S.fixCheck({ kind: 'not_in_type', path: cur.path, baseRev: cur.rev })
      expect(fixed).toMatchObject({ id: strayId, path: cur.path, body: cur.body })
      expect(fixed.rev).not.toBe(cur.rev)
      expect(fixed.meta.colour).toBeUndefined()
      expect(fixed.meta.size).toBeUndefined()
      expect(fixed.meta).toMatchObject({ title: cur.meta.title, status: 'todo', slug: 's' })
      expect((await S.getPage(strayId)).rev).toBe(fixed.rev)
      expect(at(await walkChecks(), 'not_in_type', cur.path)).toBeUndefined()
      // The page has nothing left to remove, and the old rev is stale.
      expect((await failure(S.fixCheck({ kind: 'not_in_type', path: cur.path, baseRev: cur.rev }))).status).toBe(409)
      expect((await S.fixCheck({ kind: 'not_in_type', path: cur.path, baseRev: fixed.rev })).rev).toBe(fixed.rev)
    })

    // Only a server whose files are written by hand can have these: the mock is given them.
    it('mock only: a file without an id, with a broken header or with an id that is taken; no_id is fixed with the rev it lists', async () => {
      if (REAL) return
      const taken = await mk(folder, { title: `占了${run}` }, 'x\n')
      server!.addOrphan(spaceId, {
        path: `${folder}/noid.md`,
        kind: 'no_id',
        meta: { title: `无id${run}`, custom: 1 },
        body: 'orphan\n',
      })
      server!.addOrphan(spaceId, { path: `${folder}/broken.md`, kind: 'bad_header', line: 3 })
      server!.addOrphan(spaceId, { path: `${folder}/twin.md`, kind: 'duplicate_id', id: taken.id })
      const items = await walkChecks()
      const n = at(items, 'no_id', `${folder}/noid.md`)!
      expect(n).toMatchObject({ fix: 'add_id' })
      expect(n.id).toBeUndefined()
      expect(n.rev).toMatch(/^[0-9a-f]{40}$/)
      expect(n.detail).toBeUndefined()
      expect(at(items, 'bad_header', `${folder}/broken.md`)).toMatchObject({ fix: null, detail: { line: 3 } })
      expect(at(items, 'duplicate_id', `${folder}/twin.md`)).toMatchObject({
        id: taken.id,
        fix: null,
        detail: { paths: [taken.path, `${folder}/twin.md`] },
      })
      const stale = await failure(S.fixCheck({ kind: 'no_id', path: n.path, baseRev: '0'.repeat(40) }))
      expect([stale.status, stale.code]).toEqual([409, 'conflict'])
      const made = await S.fixCheck({ kind: 'no_id', path: n.path, baseRev: n.rev })
      expect(made.id).toMatch(/^[0-9A-HJKMNP-TV-Z]{10}$/)
      expect(made).toMatchObject({ path: n.path, body: 'orphan\n' })
      expect(made.meta).toMatchObject({ id: made.id, title: `无id${run}`, custom: 1 })
      expect((await S.getPage(made.id)).path).toBe(n.path)
      expect(at(await walkChecks(), 'no_id', n.path)).toBeUndefined()
      const again = await failure(S.fixCheck({ kind: 'no_id', path: `${folder}/broken.md`, baseRev: '0'.repeat(40) }))
      expect([400, 409]).toContain(again.status)
    })

    it('needs write to fix and read to list: the mock and a real server answer 400 invalid for a fix with no path or rev', async () => {
      const r = await call(admin, 'POST', `/api/spaces/${spaceId}/checks/fix`, { kind: 'no_id' })
      expect([r.status, r.json.error]).toEqual([400, 'invalid'])
    })
  })

  describe('links between pages', () => {
    const f = `${root}/links`
    // A page in `from` names the one in `to` as the editor writes it.
    const linkTo = (from: string, to: Page, text = '目标') => {
      const rel = to.path.split('/')
      const base = from.split('/')
      let i = 0
      while (i < base.length && i < rel.length - 1 && base[i] === rel[i]) i++
      const dest = [...base.slice(i).map(() => '..'), ...rel.slice(i)].map(encodeURIComponent).join('/')
      return `[${text}](${dest} "${to.id}")`
    }

    it('lists the links of a page in the order of the text, with where they lead now', async () => {
      const target = await mk(`${f}/a/目标`, { title: `被链到${run}` }, '正文\n')
      const src = await mk(
        `${f}/a`,
        { title: `出链${run}` },
        `先看 ${linkTo(`${f}/a`, target, '甲')}，再看[无](没有这一篇.md)，还有[外](https://example.com/x.md)。\n`,
      )
      const r = await S.links(src.id)
      expect(r.items.map((l) => l.text)).toEqual(['甲', '无'])
      expect(r.items[0]).toMatchObject({ text: '甲', id: target.id, path: target.path, broken: false })
      expect(r.items[0].target).toMatchObject({
        id: target.id,
        path: target.path,
        title: `被链到${run}`,
        untitled: false,
      })
      expect(r.items[1]).toMatchObject({ text: '无', path: `${f}/a/没有这一篇.md`, broken: true })
      expect(r.items[1].target).toBeUndefined()
    })

    it('a link that is in code, an image or an anchor on the page is not a link', async () => {
      const target = await mk(f, { title: `不算${run}` }, 'x\n')
      const rel = target.path.split('/').pop()!
      const src = await mk(f, {}, `\`[a](${rel})\`\n\n![b](${rel})\n\n[c](#节)\n\n\`\`\`\n[d](${rel})\n\`\`\`\n`)
      expect((await S.links(src.id)).items).toEqual([])
    })

    it('a link follows the page by its id; its title is the page as it is now', async () => {
      const target = await mk(f, { title: `旧名${run}` }, 'x\n')
      const src = await mk(f, {}, `${linkTo(f, target)}\n`)
      await S.savePage(target.id, {
        baseRev: target.rev,
        meta: { ...target.meta, title: `新名${run}` },
        body: target.body,
      })
      const l = (await S.links(src.id)).items[0]
      expect(l.broken).toBe(false)
      expect(l.target?.title).toBe(`新名${run}`)
    })

    it('says which pages link here, with the paragraph and the total, never the page itself', async () => {
      const made = await mk(`${f}/back`, { title: `被引用${run}` }, 'x\n')
      const target = await S.savePage(made.id, {
        baseRev: made.rev,
        meta: made.meta,
        body: `自己链自己：${linkTo(`${f}/back`, made, '我')}\n`,
      })
      const one = await mk(
        `${f}/back/甲`,
        { title: `引用甲${run}` },
        `前一段。\n\n这一段链到${linkTo(`${f}/back/甲`, target, '目标')}，写了两次${linkTo(`${f}/back/甲`, target, '又一次')}。\n\n后一段。\n`,
      )
      const r = await S.backlinks(target.id)
      expect(r.total).toBe(1)
      expect(r.next).toBe('')
      expect(r.items).toHaveLength(1)
      expect(r.items[0]).toMatchObject({ id: one.id, path: one.path, title: `引用甲${run}`, untitled: false })
      expect(r.items[0].excerpt).toContain('这一段链到')
      expect(r.items[0].excerpt).not.toContain('前一段')
      expect(r.items[0].excerpt).not.toContain('后一段')
    })

    it('pages the backlinks 20 at a time, each page once, with a cursor that ends', async () => {
      const target = await mk(`${f}/many`, { title: `热门${run}` }, 'x\n')
      const made: string[] = []
      for (let i = 0; i < 22; i++)
        made.push(
          (
            await mk(
              `${f}/many`,
              { title: `引用${String(i).padStart(2, '0')}${run}` },
              `见${linkTo(`${f}/many`, target)}\n`,
            )
          ).id,
        )
      const first = await S.backlinks(target.id)
      expect(first.items).toHaveLength(20)
      expect(first.total).toBe(22)
      expect(first.next).not.toBe('')
      const second = await S.backlinks(target.id, first.next)
      expect(second.items).toHaveLength(2)
      expect(second.total).toBe(22)
      expect(second.next).toBe('')
      expect([...first.items, ...second.items].map((b) => b.id).sort()).toEqual([...made].sort())
    })

    it('a page nothing links to has an empty list; a bad cursor is 400; an unknown page is 404', async () => {
      const lonely = await mk(f, { title: `孤立${run}` }, 'x\n')
      expect(await S.backlinks(lonely.id)).toEqual({ items: [], next: '', total: 0 })
      await expect(S.backlinks(lonely.id, 'not-a-cursor')).rejects.toMatchObject({ status: 400, code: 'invalid' })
      await expect(S.backlinks('01JZZZZZZZ')).rejects.toMatchObject({ status: 404 })
      await expect(S.links('01JZZZZZZZ')).rejects.toMatchObject({ status: 404 })
    })

    it('deleting the target makes the link broken and changes no other file', async () => {
      const target = await mk(f, { title: `将删${run}` }, 'x\n')
      const src = await mk(f, {}, `${linkTo(f, target)}\n`)
      await S.deletePage(target.id)
      const l = (await S.links(src.id)).items[0]
      expect(l.broken).toBe(true)
      expect(l.target).toBeUndefined()
      expect((await S.getPage(src.id)).body).toContain(target.id)
    })
  })

  describe('reindex', () => {
    it('rebuilds the index and answers with counts; nothing is lost', async () => {
      const p = await mk(`${root}/reindex`, { title: `重建${run}` }, '重建前\n')
      const r = await S.reindex()
      expect(r.pages).toBeGreaterThanOrEqual(1)
      expect(typeof r.ms).toBe('number')
      expect((await walk({ folder: `${root}/reindex` })).map((i) => i.id)).toEqual([p.id])
      expect((await S.getPage(p.id)).body).toBe('重建前\n')
    })
  })

  describe('offline sync: the catalogue, the changes, several pages and the heads', () => {
    const folder = `${root}/offline`
    const syncUrl = (rest: string) => `/api/spaces/${spaceId}${rest}`
    const walkCatalog = async (folders: string[], limit: number) => {
      const items: CatalogEntry[] = []
      let cursor = ''
      let first: CatalogBatch | undefined
      for (let guard = 0; guard < 200; guard++) {
        const b = await S.syncCatalog({ folders, limit, cursor: cursor || undefined })
        if (!cursor) first = b
        else expect(b.changes, 'changes only in the first batch').toBeUndefined()
        items.push(...b.items)
        cursor = b.next
        if (!cursor) return { items, first: first! }
      }
      throw new Error('catalogue never ended')
    }
    const changesAll = async (since: string, limit: number) => {
      const items: ChangeItem[] = []
      let next = since
      for (let guard = 0; guard < 200; guard++) {
        const r = await S.changes(next, limit)
        items.push(...r.items)
        next = r.next
        if (!r.more) return { items, next, last: r }
      }
      throw new Error('changes never ended')
    }

    it('the catalogue lists every page of the folders asked for, in batches, with the light summary', async () => {
      const a = await mk(`${folder}/a`, { title: `甲${run}`, tags: ['同步'] }, '甲的正文\n')
      await mk(`${folder}/a/sub`, { title: `乙${run}` }, '乙\n')
      await mk(`${folder}/b`, { title: `丙${run}` }, '丙\n')
      await mk(`${folder}/b`, {}, '没有标题的一页\n')
      const { items, first } = await walkCatalog([folder], 2)
      expect(items).toHaveLength(4)
      expect(first.items).toHaveLength(2)
      expect(first.changes).toBeTruthy()
      expect(first.tree).toBeTruthy()
      expect(first.settings).toBeTruthy()
      const got = items.find((e) => e.id === a.id)!
      expect(got).toMatchObject({
        id: a.id,
        path: a.path,
        folder: `${folder}/a`,
        title: `甲${run}`,
        label: `甲${run}`,
        untitled: false,
        tags: ['同步'],
        type: '',
      })
      expect(got.rev).toBe((await S.getPage(a.id)).rev)
      expect(got.updated).toMatch(/^\d{4}-\d\d-\d\dT/)
      expect(got).not.toHaveProperty('excerpt')
      expect(items.find((e) => e.untitled)).toBeDefined()
      // A folder and below; two folders; the root names everything.
      expect((await walkCatalog([`${folder}/a`], 1)).items.map((e) => e.id).sort()).toEqual(
        items
          .filter((e) => e.folder.startsWith(`${folder}/a`))
          .map((e) => e.id)
          .sort(),
      )
      expect((await walkCatalog([`${folder}/a/sub`, `${folder}/b`], 5)).items).toHaveLength(3)
      expect((await walkCatalog([''], 1000)).items.length).toBeGreaterThanOrEqual(4)
      // Refusals.
      for (const q of ['limit=0', 'limit=5001', 'limit=x', 'cursor=nope'])
        expect((await call(admin, 'GET', syncUrl(`/sync/catalog?${q}`))).status, q).toBe(400)
      const many = Array.from({ length: 51 }, (_, i) => `folder=f${i}`).join('&')
      expect((await call(admin, 'GET', syncUrl(`/sync/catalog?${many}`))).status).toBe(400)
      const cur = (await S.syncCatalog({ folders: [`${folder}/b`], limit: 1 })).next
      expect(
        (
          await call(
            admin,
            'GET',
            syncUrl(`/sync/catalog?folder=${encodeURIComponent(folder + '/a')}&cursor=${encodeURIComponent(cur)}`),
          )
        ).status,
        'a cursor of other folders',
      ).toBe(400)
    })

    it('changes after the cursor of the first batch: the pages that were added, changed and moved, and the removals, each once', async () => {
      const x = await mk(`${folder}/c`, { title: `一${run}` }, '一\n')
      const y = await mk(`${folder}/c`, { title: `二${run}` }, '二\n')
      const z = await mk(`${folder}/c`, { title: `三${run}` }, '三\n')
      const { first } = await walkCatalog([`${folder}/c`], 10)
      const since = first.changes!
      // Nothing yet.
      const none = await S.changes(since)
      expect(none.items).toEqual([])
      expect(none.more).toBe(false)
      expect(none.next).toBe(since)
      expect(none.tree).toBe(first.tree)

      const x2 = await S.savePage(x.id, {
        baseRev: x.rev,
        meta: { ...x.meta, title: `一改${run}` },
        body: '一，改了\n',
      })
      await S.savePage(x.id, { baseRev: x2.rev, meta: x2.meta, body: '一，又改了\n' })
      await S.movePage(y.id, `${folder}/d`)
      await S.deletePage(z.id)
      const w = await mk(`${folder}/c`, { title: `四${run}` }, '四\n')
      const { items, next } = await changesAll(since, 2)
      const by = new Map(items.map((i) => [i.id, i]))
      expect(items).toHaveLength(4)
      expect(by.get(x.id)).toMatchObject({ title: `一改${run}`, folder: `${folder}/c` })
      expect((by.get(x.id) as CatalogEntry).rev).toBe((await S.getPage(x.id)).rev)
      expect((by.get(y.id) as CatalogEntry).folder).toBe(`${folder}/d`)
      expect(by.get(z.id)).toEqual({ id: z.id, deleted: true })
      expect(by.get(w.id)).toBeDefined()
      // The tree changed (a folder, the number of pages in one); the cursor moved to where the space is.
      const again = await S.changes(next)
      expect(again.items).toEqual([])
      expect(again.next).toBe(next)
      expect(again.tree).not.toBe(first.tree)
      expect((await api.syncHeads([spaceId])).heads[spaceId]).toBe(next)
      // A page removed and made again with the same id (here: not possible through the API) is out of scope; a
      // limit that is too big or too small is refused.
      for (const q of ['limit=0', 'limit=5001'])
        expect((await call(admin, 'GET', syncUrl(`/changes?since=${encodeURIComponent(next)}&${q}`))).status, q).toBe(
          400,
        )
    })

    it('a change cursor that is missing or not ours is 400; one from before a reindex is 410 changes_expired, so is a catalogue cursor', async () => {
      expect((await call(admin, 'GET', syncUrl('/changes'))).status).toBe(400)
      expect((await call(admin, 'GET', syncUrl('/changes?since='))).status).toBe(400)
      expect((await call(admin, 'GET', syncUrl('/changes?since=nope'))).status).toBe(400)
      await mk(`${folder}/e`, { title: `e1${run}` }, 'x\n')
      await mk(`${folder}/e`, { title: `e2${run}` }, 'x\n')
      const { first } = await walkCatalog([`${folder}/e`], 1)
      const mid = (await S.syncCatalog({ folders: [`${folder}/e`], limit: 1 })).next
      expect(mid).not.toBe('')
      await S.reindex()
      const e = await failure(S.changes(first.changes!))
      expect([e.status, e.code]).toEqual([410, 'changes_expired'])
      const e2 = await failure(S.syncCatalog({ folders: [`${folder}/e`], limit: 1, cursor: mid }))
      expect([e2.status, e2.code]).toEqual([410, 'changes_expired'])
      // The walk starts again with a good cursor.
      const fresh = await walkCatalog([`${folder}/e`], 1)
      expect(fresh.items).toHaveLength(2)
      expect((await S.changes(fresh.first.changes!)).items).toEqual([])
    })

    it("several pages at once, as the single-page read gives them, with the caller's draft; unknown ids are missing", async () => {
      const p = await mk(`${folder}/f`, { title: `页${run}` }, '正文一\n')
      const q = await mk(`${folder}/f`, { title: `页二${run}` }, '正文二\n')
      const receipt = await S.putDraft(p.id, { baseRev: p.rev, meta: p.meta, body: '正文一，写到一半\n', prev: '' })
      const r = await S.syncPages([p.id, '01ZZZZZZZZ', q.id])
      expect(r.pages.map((x) => x.id)).toEqual([p.id, q.id])
      expect(r.missing).toEqual(['01ZZZZZZZZ'])
      expect(r.rest).toBeUndefined()
      const one = await S.getPage(p.id)
      expect(r.pages[0]).toEqual(one)
      expect(r.pages[0].draft).toMatchObject({ version: receipt.version, body: '正文一，写到一半\n' })
      expect(r.pages[1].draft).toBeUndefined()
      // 100 ids are fine; refusals.
      const hundred = Array.from({ length: 100 }, (_, i) => `01ZZ${String(i).padStart(6, '0')}`.replace(/[ILOU]/g, 'X'))
      expect((await S.syncPages(hundred)).missing).toHaveLength(100)
      expect((await call(admin, 'GET', syncUrl('/sync/pages'))).status).toBe(400)
      expect((await call(admin, 'GET', syncUrl(`/sync/pages?ids=${p.id},${p.id}`))).status).toBe(400)
      expect((await call(admin, 'GET', syncUrl(`/sync/pages?ids=${p.id},x`))).status).toBe(400)
      expect(
        (await call(admin, 'GET', syncUrl(`/sync/pages?ids=${[...hundred, 'XXXXXXXXXX'].join(',')}`))).status,
      ).toBe(400)
    })

    it('heads: the change cursor of each space asked for; a space that cannot be read is missing', async () => {
      const r = await api.syncHeads([spaceId, '01ZZZZZZZZ'])
      expect(r.heads[spaceId]).toBeTruthy()
      expect(r.missing).toEqual(['01ZZZZZZZZ'])
      // The head is a cursor of the space: asking for its changes works.
      expect((await S.changes(r.heads[spaceId])).items).toEqual([])
      // A write moves it, a read does not.
      await S.syncCatalog({ limit: 1 })
      expect((await api.syncHeads([spaceId])).heads[spaceId]).toBe(r.heads[spaceId])
      await mk(`${folder}/g`, { title: `头${run}` }, 'x\n')
      expect((await api.syncHeads([spaceId])).heads[spaceId]).not.toBe(r.heads[spaceId])
      const dup = await call(admin, 'GET', `/api/sync/heads?space=${spaceId}&space=${spaceId}`)
      expect(dup.status).toBe(400)
      expect((await call(admin, 'GET', '/api/sync/heads')).status).toBe(400)
      const hundredOne = Array.from({ length: 101 }, (_, i) => `space=s${i}`).join('&')
      expect((await call(admin, 'GET', `/api/sync/heads?${hundredOne}`)).status).toBe(400)
    })

    it.skipIf(!accounts)(
      'offline.copies: when the site locks it off, the four sync endpoints answer 403 offline_off; it is a setting a person only reads',
      async () => {
        const routes = [
          syncUrl('/sync/catalog'),
          syncUrl('/changes?since=x'),
          syncUrl('/sync/pages?ids=01ZZZZZZZZ'),
          `/api/sync/heads?space=${spaceId}`,
        ]
        const mineOf = async (c: typeof admin) =>
          (
            (await call(c, 'GET', '/api/settings')).json.settings as {
              key: string
              effective: unknown
              space?: unknown
              person?: unknown
            }[]
          ).find((x) => x.key === 'offline.copies')
        const def = (
          (await call(admin, 'GET', '/api/admin/settings')).json.settings as {
            key: string
            kind: string
            layers: string[]
            site: { value: unknown }
          }[]
        ).find((x) => x.key === 'offline.copies')!
        expect([def.kind, def.layers, def.site.value]).toEqual(['boolean', ['site', 'tenant'], true])
        expect(await mineOf(admin)).toEqual({ key: 'offline.copies', effective: true })
        try {
          expect(
            (await call(admin, 'PUT', '/api/admin/settings/offline.copies', { value: false, lower: { mode: 'lock' } }))
              .status,
          ).toBe(200)
          expect((await mineOf(admin))?.effective).toBe(false)
          for (const path of routes) {
            const got = await call(admin, 'GET', path)
            expect([got.status, got.json.error], path).toEqual([403, 'offline_off'])
          }
          // Nothing else is refused.
          expect((await call(admin, 'GET', `/api/spaces/${spaceId}/pages`)).status).toBe(200)
        } finally {
          await call(admin, 'PUT', '/api/admin/settings/offline.copies', { value: true, lower: { mode: 'allow' } })
        }
        expect((await call(admin, 'GET', `/api/sync/heads?space=${spaceId}`)).status).toBe(200)
        expect((await call(admin, 'GET', syncUrl('/sync/catalog'))).status).toBe(200)
        // A tenant may turn it off for itself (the one tenant of this server).
        const t = (
          (await call(admin, 'GET', '/api/admin/tenants')).json.items as { id: string; default: boolean }[]
        ).find((x) => x.default)!
        try {
          expect(
            (await call(admin, 'PUT', `/api/admin/tenants/${t.id}/settings/offline.copies`, { value: false })).status,
          ).toBe(200)
          expect((await call(admin, 'GET', `/api/sync/heads?space=${spaceId}`)).json.error).toBe('offline_off')
        } finally {
          await call(admin, 'PUT', `/api/admin/tenants/${t.id}/settings/offline.copies`, { value: true })
        }
        expect((await call(admin, 'GET', `/api/sync/heads?space=${spaceId}`)).status).toBe(200)
      },
    )

    it.skipIf(!accounts)(
      'somebody who is not a member of the space is told there is no such space, and has no head for it',
      async () => {
        const invite = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
        const b = browser()
        const name = `os${run}`.slice(0, 20)
        const r = await call(b, 'POST', '/api/invites/accept', {
          token: invite.token,
          username: name,
          name: `外 ${name}`,
          password: 'a-long-password',
        })
        expect(r.status).toBe(201)
        created.users.push(r.json.user.id)
        for (const path of ['/sync/catalog', '/changes?since=x', '/sync/pages?ids=01ZZZZZZZZ']) {
          const got = await call(b, 'GET', syncUrl(path))
          expect([got.status, got.json.error], path).toEqual([404, 'not_found'])
        }
        const h = await call(b, 'GET', `/api/sync/heads?space=${spaceId}`)
        expect(h.status).toBe(200)
        expect(h.json.heads).toEqual({})
        expect(h.json.missing).toEqual([spaceId])
        // Nor with no session at all.
        expect((await call(browser(), 'GET', `/api/sync/heads?space=${spaceId}`)).status).toBe(401)
        expect((await call(browser(), 'GET', syncUrl('/sync/catalog'))).status).toBe(401)
      },
    )
  })

  describe('drafts', () => {
    const draftOf = (p: Page, over: { title?: string; body?: string; baseRev?: string } = {}) => ({
      baseRev: over.baseRev ?? p.rev,
      meta: { ...p.meta, ...(over.title === undefined ? {} : { title: over.title }) },
      body: over.body ?? p.body,
    })
    // Store a draft, replacing version `prev` ("" when there is none).
    const store = (p: Page, over: Parameters<typeof draftOf>[1] = {}, prev = '') =>
      S.putDraft(p.id, { ...draftOf(p, over), prev })

    it('a stored draft leaves the file, the lists and search alone; GET carries it, with its version', async () => {
      const p = await mk(`${root}/dr1`, { title: `草稿${run}`, tags: [`dt${run}`] }, '原文\n')
      const receipt = await store(p, { title: `草稿新题${run}`, body: `仅在草稿里的${run}\n` })
      expect(receipt.baseRev).toBe(p.rev)
      expect(receipt.stale).toBe(false)
      expect(typeof receipt.version).toBe('string')
      expect(receipt.version).not.toBe('')
      expect(receipt.updated).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/)
      const got = await S.getPage(p.id)
      expect(got.rev).toBe(p.rev)
      expect(got.body).toBe('原文\n')
      expect(got.meta.title).toBe(`草稿${run}`)
      expect(got.draft).toEqual({
        version: receipt.version,
        baseRev: p.rev,
        meta: { ...p.meta, title: `草稿新题${run}` },
        body: `仅在草稿里的${run}\n`,
        updated: receipt.updated,
        stale: false,
      })
      const item = (await walk({ folder: `${root}/dr1` }))[0]
      expect(item.title).toBe(`草稿${run}`)
      expect(item.updated).toBe(p.updated)
      expect(await walk({ q: `仅在草稿里的${run}`, folder: '', recursive: true })).toEqual([])
    })

    it('a page without a draft has no `draft` in GET, and neither do save, create or commit answers', async () => {
      const p = await mk(`${root}/dr2`, { title: `无草稿${run}` })
      expect('draft' in (await S.getPage(p.id))).toBe(false)
      expect('draft' in p).toBe(false)
      const r = await store(p)
      expect('draft' in (await S.commitPage(p.id, r.version))).toBe(false)
    })

    it('every store has a new version, even in the same second; `prev` absent means "no draft known"', async () => {
      const p = await mk(`${root}/dr3`, { title: `版本${run}` }, 'a\n')
      const one = await store(p, { body: 'b\n' })
      const two = await store(p, { body: 'c\n' }, one.version)
      const three = await store(p, { body: 'd\n' }, two.version)
      expect(new Set([one.version, two.version, three.version]).size).toBe(3)
      const got = await S.getPage(p.id)
      expect(got.draft?.body).toBe('d\n')
      expect(got.draft?.version).toBe(three.version)
      expect(got.body).toBe('a\n')
      const q = await mk(`${root}/dr3b`, { title: `无prev${run}` }, 'a\n')
      const noPrev = await fetch(`${SP_PAGES}/${q.id}/draft`, {
        method: 'PUT',
        headers: JSON_HEADERS,
        body: JSON.stringify(draftOf(q, { body: 'x\n' })),
      })
      expect(noPrev.status).toBe(200)
    })

    it('a store that does not name the draft there is 409 with that draft and the page, and stores nothing', async () => {
      const p = await mk(`${root}/dr4a`, { title: `互相覆盖${run}` }, 'a\n')
      const mine = await store(p, { body: 'mine\n' })
      // Another device stores on top of it: it knew `mine.version`.
      const theirs = await store(p, { body: 'theirs\n' }, mine.version)
      // This device still believes in `mine.version`.
      const e = await failure(store(p, { body: 'mine again\n' }, mine.version))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect(e.draft).toMatchObject({ version: theirs.version, body: 'theirs\n', stale: false })
      expect(e.current?.rev).toBe(p.rev)
      expect((await S.getPage(p.id)).draft?.version).toBe(theirs.version)
      // A device that knows of no draft is refused the same way.
      const blank = await failure(store(p, { body: 'blank\n' }, ''))
      expect(blank.draft?.version).toBe(theirs.version)
    })

    it('a store naming a draft that is gone is 409 with `draft: null` and the file as it is now', async () => {
      const p = await mk(`${root}/dr4b`, { title: `已提交${run}` }, 'a\n')
      const first = await store(p, { body: 'b\n' })
      const committed = await S.commitPage(p.id, first.version)
      const e = await failure(store(p, { body: 'c\n', baseRev: p.rev }, first.version))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect(e.draft).toBeNull()
      expect(e.current?.rev).toBe(committed.rev)
      expect(e.current?.body).toBe('b\n')
      expect('draft' in (await S.getPage(p.id))).toBe(false)
    })

    it('is refused like a save: 400 for a missing field or another id, 404 for a page that is not there, 415 without JSON', async () => {
      const p = await mk(`${root}/dr5`, { title: `校验${run}` }, 'x\n')
      const url = `${SP_PAGES}/${p.id}/draft`
      for (const payload of [
        { baseRev: p.rev, meta: p.meta },
        { baseRev: p.rev, body: 'y\n' },
        { meta: p.meta, body: 'y\n' },
        {},
      ]) {
        const r = await fetch(url, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(payload) })
        expect(r.status, JSON.stringify(payload)).toBe(400)
        expect(((await r.json()) as { error: string }).error).toBe('invalid')
      }
      const mismatch = await failure(
        S.putDraft(p.id, { baseRev: p.rev, meta: { ...p.meta, id: '01AAAAAAAA' }, body: 'y\n', prev: '' }),
      )
      expect([mismatch.status, mismatch.code]).toEqual([400, 'invalid'])
      const missing = await failure(S.putDraft('01ZZZZZZZZ', { baseRev: p.rev, meta: {}, body: '', prev: '' }))
      expect([missing.status, missing.code]).toEqual([404, 'not_found'])
      const plain = await fetch(url, {
        method: 'PUT',
        headers: { 'Content-Type': 'text/plain' },
        body: JSON.stringify({ ...draftOf(p), prev: '' }),
      })
      expect([400, 415]).toContain(plain.status)
      expect('draft' in (await S.getPage(p.id))).toBe(false)
    })

    it('commit writes the draft to the file exactly as a save would, and removes the draft', async () => {
      const a = await mk(`${root}/dr6a`, { title: `提交${run}`, series: '冬季' }, '旧\n')
      const b = await mk(`${root}/dr6b`, { title: `提交${run}`, series: '冬季' }, '旧\n')
      const weird = { nested: { a: [1, { b: null }] }, flag: true }
      const edit = (p: Page) => ({ baseRev: p.rev, meta: { ...p.meta, title: `提交后${run}`, ...weird }, body: '新\n' })
      const r = await S.putDraft(a.id, { ...edit(a), prev: '' })
      const committed = await S.commitPage(a.id, r.version)
      const saved = await S.savePage(b.id, edit(b))
      expect(committed.id).toBe(a.id)
      expect(committed.rev).not.toBe(a.rev)
      expect(committed.body).toBe('新\n')
      expect(committed.path).toBe(`${root}/dr6a/提交后${run}.md`)
      expect(committed.meta.nested).toEqual(weird.nested)
      expect(Object.keys(committed.meta).filter((k) => k !== 'id' && k !== 'created')).toEqual(
        Object.keys(saved.meta).filter((k) => k !== 'id' && k !== 'created'),
      )
      expect(committed.body).toBe(saved.body)
      const got = await S.getPage(a.id)
      // The file, as GET shows it: the answer adds only textRev.
      expect(got).toEqual({ ...committed, textRev: undefined })
      expect('draft' in got).toBe(false)
      expect((await walk({ folder: `${root}/dr6a` }))[0].title).toBe(`提交后${run}`)
    })

    it('commit needs the version: without one it is 400; with a wrong one 409 with the draft and the page, and nothing is written', async () => {
      const p = await mk(`${root}/dr7`, { title: `要版本${run}` }, 'x\n')
      const r = await store(p, { body: 'y\n' })
      const none = await fetch(`${SP_PAGES}/${p.id}/commit`, { method: 'POST', headers: JSON_HEADERS, body: '{}' })
      expect(none.status).toBe(400)
      expect(((await none.json()) as { error: string }).error).toBe('invalid')
      const wrong = await failure(S.commitPage(p.id, 'not-the-version'))
      expect([wrong.status, wrong.code]).toEqual([409, 'conflict'])
      expect(wrong.draft?.version).toBe(r.version)
      expect(wrong.current?.rev).toBe(p.rev)
      const got = await S.getPage(p.id)
      expect(got.body).toBe('x\n')
      expect(got.draft?.version).toBe(r.version)
      // The right one still works.
      expect((await S.commitPage(p.id, r.version)).body).toBe('y\n')
    })

    it('commit when the draft is gone (committed already) is 409 with `draft: null` and the file: a client compares it with what it meant to commit', async () => {
      const p = await mk(`${root}/dr8`, { title: `已经提交${run}` }, 'x\n')
      const r = await store(p, { body: 'y\n' })
      const done = await S.commitPage(p.id, r.version)
      const again = await failure(S.commitPage(p.id, r.version))
      expect([again.status, again.code]).toEqual([409, 'conflict'])
      expect(again.draft).toBeNull()
      expect(again.current).toEqual({ ...done, textRev: undefined })
      // And with no draft ever.
      const q = await mk(`${root}/dr8b`, { title: `从没有${run}` })
      const none = await failure(S.commitPage(q.id, 'whatever'))
      expect(none.draft).toBeNull()
      expect(none.current?.rev).toBe(q.rev)
    })

    it('a draft that says what the file says commits to the same rev', async () => {
      const p = await mk(`${root}/dr9`, { title: `相同${run}` }, 'x\n')
      const r = await store(p)
      expect((await S.commitPage(p.id, r.version)).rev).toBe(p.rev)
    })

    it('delete is 204, also when there was none; with a version it deletes only that draft (409 for another)', async () => {
      const p = await mk(`${root}/dr10`, { title: `删草稿${run}` }, 'x\n')
      const one = await store(p, { body: 'y\n' })
      const two = await store(p, { body: 'z\n' }, one.version)
      const e = await failure(S.deleteDraft(p.id, one.version))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect((await S.getPage(p.id)).draft?.version).toBe(two.version)
      expect(await S.deleteDraft(p.id, two.version)).toBeUndefined()
      expect('draft' in (await S.getPage(p.id))).toBe(false)
      expect(await S.deleteDraft(p.id)).toBeUndefined()
      const q = await store(p, { body: 'w\n' })
      expect(await S.deleteDraft(p.id)).toBeUndefined()
      void q
      expect('draft' in (await S.getPage(p.id))).toBe(false)
    })

    it('committing a stale draft that says what the file says is 200 with the page, and the draft is gone', async () => {
      const p = await mk(`${root}/dr11b`, { title: `殊途同归${run}` }, '起点\n')
      const moved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '一样的字\n' })
      const r = await store(p, { body: '一样的字\n' })
      expect(r.stale).toBe(true)
      const done = await S.commitPage(p.id, r.version)
      expect(done.rev).toBe(moved.rev)
      expect(done.body).toBe('一样的字\n')
      expect('draft' in (await S.getPage(p.id))).toBe(false)
    })

    it('a versioned delete after the draft was committed is 409 with `draft: null` and the file; an unversioned one is 204', async () => {
      const p = await mk(`${root}/dr10b`, { title: `删已提交${run}` }, 'x\n')
      const r = await store(p, { body: 'y\n' })
      const done = await S.commitPage(p.id, r.version)
      const e = await failure(S.deleteDraft(p.id, r.version))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect(e.draft).toBeNull()
      expect(e.current).toEqual({ ...done, textRev: undefined })
      expect((await S.getPage(p.id)).body).toBe('y\n')
      expect(await S.deleteDraft(p.id)).toBeUndefined()
    })

    it('a versioned delete that names a rev the file has left is 409 with the draft and the file, and deletes nothing', async () => {
      const p = await mk(`${root}/dr10r`, { title: `删旧${run}` }, 'x\n')
      const r = await store(p, { body: 'draft\n' })
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: 'elsewhere\n' })
      const e = await failure(S.deleteDraft(p.id, r.version, p.rev))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect(e.draft).toMatchObject({ version: r.version, stale: true })
      expect(e.current).toMatchObject({ rev: saved.rev, body: 'elsewhere\n' })
      expect((await S.getPage(p.id)).draft?.version).toBe(r.version)
      // Naming the rev the file is at, it goes.
      expect(await S.deleteDraft(p.id, r.version, saved.rev)).toBeUndefined()
      expect('draft' in (await S.getPage(p.id))).toBe(false)
    })

    it('saving the page leaves the draft (stale), unless the draft says what was saved', async () => {
      const p = await mk(`${root}/dr11`, { title: `存盘${run}` }, 'x\n')
      const r = await store(p, { body: 'draft\n' })
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: 'saved\n' })
      const got = await S.getPage(p.id)
      expect(got.body).toBe('saved\n')
      expect(got.rev).toBe(saved.rev)
      expect(got.draft).toMatchObject({ version: r.version, body: 'draft\n', stale: true })
      // Saving the draft's own text leaves it nothing to add.
      await S.savePage(p.id, { baseRev: saved.rev, meta: saved.meta, body: 'draft\n' })
      expect('draft' in (await S.getPage(p.id))).toBe(false)
    })

    it('a draft on a rev the file has left is stored but stale, in the answer and in GET; commit is 409 with the page and the draft, which is kept', async () => {
      const p = await mk(`${root}/dr12`, { title: `过期${run}` }, '起点\n')
      const moved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '别处改的\n' })
      const receipt = await store(p, { body: '我的草稿\n' })
      expect(receipt).toMatchObject({ baseRev: p.rev, stale: true })
      const got = await S.getPage(p.id)
      expect(got.rev).toBe(moved.rev)
      expect(got.draft).toMatchObject({ version: receipt.version, baseRev: p.rev, body: '我的草稿\n', stale: true })
      const e = await failure(S.commitPage(p.id, receipt.version))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect(e.current?.rev).toBe(moved.rev)
      expect(e.current?.body).toBe('别处改的\n')
      expect(e.draft).toMatchObject({ version: receipt.version, stale: true })
      expect((await S.getPage(p.id)).draft?.body).toBe('我的草稿\n')
      expect((await S.getPage(p.id)).body).toBe('别处改的\n')
    })

    it('"keep mine": storing the draft again on the current rev makes it committable; "take theirs": deleting it ends the conflict', async () => {
      const p = await mk(`${root}/dr13`, { title: `两条路${run}` }, '起点\n')
      const moved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '别处改的\n' })
      const stale = await store(p, { body: '我的草稿\n' })
      const again = await S.putDraft(p.id, {
        baseRev: moved.rev,
        meta: moved.meta,
        body: '我的草稿\n',
        prev: stale.version,
      })
      expect(again.stale).toBe(false)
      expect((await S.commitPage(p.id, again.version)).body).toBe('我的草稿\n')

      const q = await mk(`${root}/dr13b`, { title: `放弃${run}` }, '起点\n')
      const movedQ = await S.savePage(q.id, { baseRev: q.rev, meta: q.meta, body: '别处改的\n' })
      const staleQ = await store(q, { body: '我的草稿\n' })
      await S.deleteDraft(q.id, staleQ.version)
      const got = await S.getPage(q.id)
      expect(got.rev).toBe(movedQ.rev)
      expect('draft' in got).toBe(false)
    })

    it('deleting a page deletes its drafts: nothing can be stored or committed afterwards', async () => {
      const p = await mk(`${root}/dr14`, { title: `删页${run}` }, 'x\n')
      const r = await store(p, { body: 'y\n' })
      await S.deletePage(p.id)
      expect((await failure(S.getPage(p.id))).status).toBe(404)
      expect((await failure(store(p))).status).toBe(404)
      expect((await failure(S.commitPage(p.id, r.version))).status).toBe(404)
    })

    it('moving a page keeps its draft', async () => {
      const p = await mk(`${root}/dr15`, { title: `搬家草稿${run}` }, 'x\n')
      const r = await store(p, { body: 'y\n' })
      const moved = await S.movePage(p.id, `${root}/dr15b`)
      const got = await S.getPage(p.id)
      expect(got.path).toBe(moved.path)
      expect(got.draft).toMatchObject({ version: r.version, body: 'y\n' })
    })

    it.skipIf(!!REAL)(
      'mock only: the server commits a draft that has been quiet for 2 minutes, but not before; a stale one is left alone; then the old version is refused',
      async () => {
        const p = await mk(`${root}/dr16`, { title: `自动提交${run}` }, 'x\n')
        const r = await store(p, { body: 'auto\n' })
        clock += 119_000
        expect((await S.getPage(p.id)).body).toBe('x\n')
        clock += 2_000
        const got = await S.getPage(p.id)
        expect(got.body).toBe('auto\n')
        expect(got.rev).not.toBe(p.rev)
        expect('draft' in got).toBe(false)
        // The next store still carries the old version: refused, with the file as it is now.
        const e = await failure(S.putDraft(p.id, { ...draftOf(p, { body: 'more\n' }), prev: r.version }))
        expect(e.draft).toBeNull()
        expect(e.current?.body).toBe('auto\n')

        const q = await mk(`${root}/dr16b`, { title: `过期不提交${run}` }, 'x\n')
        const moved = await S.savePage(q.id, { baseRev: q.rev, meta: q.meta, body: 'else\n' })
        await store(q, { body: 'mine\n' })
        clock += 30 * 60_000
        const stale = await S.getPage(q.id)
        expect(stale.body).toBe('else\n')
        expect(stale.rev).toBe(moved.rev)
        expect(stale.draft).toMatchObject({ body: 'mine\n', stale: true })
      },
    )

    it.skipIf(!!REAL)(
      'mock only: the server removes a stale draft that says what the file says (a leftover), and leaves a stale one that differs',
      async () => {
        const p = await mk(`${root}/dr16c`, { title: `留存${run}` }, '起点\n')
        const moved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '一样的字\n' })
        const same = await store(p, { body: '一样的字\n' })
        expect(same.stale).toBe(true)
        const q = await mk(`${root}/dr16d`, { title: `不同${run}` }, '起点\n')
        await S.savePage(q.id, { baseRev: q.rev, meta: q.meta, body: '别处的字\n' })
        await store(q, { body: '我的字\n' })
        clock += 30 * 60_000
        expect('draft' in (await S.getPage(p.id))).toBe(false)
        expect((await S.getPage(p.id)).rev).toBe(moved.rev)
        expect((await S.getPage(q.id)).draft).toMatchObject({ body: '我的字\n', stale: true })
      },
    )

    it.skipIf(!!REAL)(
      'mock only: a draft stored again and again is committed 10 minutes after it was first stored',
      async () => {
        const p = await mk(`${root}/dr17`, { title: `十分钟${run}` }, 'x\n')
        let prev = ''
        for (let i = 0; i < 6; i++) {
          prev = (await store(p, { body: `v${i}\n` }, prev)).version
          clock += 100_000
        }
        // Never quiet for 2 minutes, yet 10 have passed.
        expect((await S.getPage(p.id)).body).toBe('v5\n')
      },
    )
  })

  describe('files', () => {
    const enc = encodeURIComponent
    const hash8 = (data: Uint8Array) => sha256Hex(data).slice(0, 8)
    const bytesOf = (s: string) => new TextEncoder().encode(s)
    // Built by Node's own Response, not by jsdom: a real `fetch` only knows how to send its own kind of Blob.
    const blobOf = (data: Uint8Array) => new Response(data as BodyInit).blob()
    const post = (pageId: string, name: string | null, data: Uint8Array, type = 'application/octet-stream') =>
      admin.fetch(`/api/spaces/${spaceId}/pages/${pageId}/assets${name === null ? '' : `?name=${enc(name)}`}`, {
        method: 'POST',
        headers: { 'Content-Type': type },
        body: data as BodyInit,
      })
    const get = (path: string) => admin.fetch(fileUrl(spaceId, path))
    let page: Page
    beforeAll(async () => {
      page = await mk(`${root}/files`, { title: `附件${run}` }, 'x\n')
    })

    it('meta reports the upload limit in bytes', async () => {
      const m = await api.meta()
      expect(Number.isInteger(m.maxUpload)).toBe(true)
      expect(m.maxUpload).toBeGreaterThan(0)
    })

    it('stores a file under assets/<page id>/ with the content hash in its name, and answers 201', async () => {
      const data = bytesOf(`bytes-${run}`)
      const r = await post(page.id, `street${run}.JPG`, data)
      expect(r.status).toBe(201)
      const body = await r.json()
      expect(body.name).toBe(`street${run}-${hash8(data)}.jpg`)
      expect(body.path).toBe(`/assets/${page.id}/${body.name}`)
      expect(body.size).toBe(data.length)
    })

    it('the client uploads a Blob and returns the same shape', async () => {
      const data = bytesOf(`client-${run}`)
      const a = await S.uploadAsset(page.id, await blobOf(data), `合同${run}.pdf`)
      expect(a).toEqual({
        path: `/assets/${page.id}/合同${run}-${hash8(data)}.pdf`,
        name: `合同${run}-${hash8(data)}.pdf`,
        size: data.length,
      })
    })

    it('the same bytes under the same name again is 200 with the same body', async () => {
      const data = bytesOf(`again-${run}`)
      const first = await post(page.id, `again${run}.txt`, data)
      expect(first.status).toBe(201)
      const second = await post(page.id, `again${run}.txt`, data)
      expect(second.status).toBe(200)
      expect(await second.json()).toEqual(await first.json())
    })

    it('other bytes under the same name are a new file', async () => {
      const a = await (await post(page.id, `same${run}.txt`, bytesOf('one'))).json()
      const r = await post(page.id, `same${run}.txt`, bytesOf('two'))
      expect(r.status).toBe(201)
      expect((await r.json()).path).not.toBe(a.path)
    })

    it('only the last segment of the name is used, and characters file systems refuse do not survive', async () => {
      const data = bytesOf(`seg-${run}`)
      const r = await (await post(page.id, `a/b/seg${run}.txt`, data)).json()
      expect(r.name).toBe(`seg${run}-${hash8(data)}.txt`)
      const odd = await (await post(page.id, `we?ird*na:me${run}.txt`, bytesOf(`odd-${run}`))).json()
      expect(odd.name).not.toMatch(/[?*:/\\]/)
      expect(odd.name).toMatch(/-[0-9a-f]{8}\.txt$/)
      expect((await get(odd.path)).status).toBe(200)
    })

    it('errors: 404 for an unknown page, 400 for no bytes or no name, 415 for another content type', async () => {
      const unknown = await post('01ZZZZZZZZ', 'a.txt', bytesOf('x'))
      expect([unknown.status, (await unknown.json()).error]).toEqual([404, 'not_found'])
      const empty = await post(page.id, 'a.txt', new Uint8Array())
      expect([empty.status, (await empty.json()).error]).toEqual([400, 'invalid'])
      const nameless = await post(page.id, null, bytesOf('x'))
      expect([nameless.status, (await nameless.json()).error]).toEqual([400, 'invalid'])
      const json = await post(page.id, 'a.txt', bytesOf('{}'), 'application/json')
      expect([json.status, (await json.json()).error]).toEqual([415, 'invalid'])
      const text = await post(page.id, 'a.txt', bytesOf('x'), 'text/plain')
      expect(text.status).toBe(415)
    })

    it('a file over the limit is 413 file_too_large, with the limit and the name', async () => {
      const { maxUpload } = await api.meta()
      // A server started with a huge limit would be sent too much to prove this.
      if (maxUpload > 64 * 1024 * 1024) return
      const r = await post(page.id, `big${run}.bin`, new Uint8Array(maxUpload + 1))
      const refused = await r.json()
      expect([r.status, refused.error]).toEqual([413, 'file_too_large'])
      expect(refused.limit).toBe(maxUpload)
      const exact = await post(page.id, `edge${run}.bin`, new Uint8Array(maxUpload).fill(7))
      expect(exact.status).toBe(201)
    })

    it('serves the bytes back, with the headers that make a file safe to show', async () => {
      const data = bytesOf(`plain text ${run}`)
      const up = await (await post(page.id, `note${run}.txt`, data)).json()
      const r = await get(up.path)
      expect(r.status).toBe(200)
      expect(Array.from(new Uint8Array(await r.arrayBuffer()))).toEqual(Array.from(data))
      expect(r.headers.get('X-Content-Type-Options')).toBe('nosniff')
      expect(r.headers.get('Content-Security-Policy')).toMatch(/sandbox/)
      expect(r.headers.get('Cache-Control')).toMatch(/private/)
      expect(r.headers.get('Cache-Control')).toMatch(/no-cache/)
      expect(r.headers.get('Last-Modified')).toBeTruthy()
      expect(r.headers.get('Content-Type')).toMatch(/^text\/plain/)
      expect(r.headers.get('Content-Disposition')).toMatch(/^inline/)
    })

    it('an image is inline with its type; an unknown extension is an attachment of octet-stream', async () => {
      const png = await (await post(page.id, `pic${run}.png`, bytesOf(`png-${run}`))).json()
      const r = await get(png.path)
      expect(r.headers.get('Content-Type')).toBe('image/png')
      expect(r.headers.get('Content-Disposition')).toMatch(/^inline/)
      const bin = await (await post(page.id, `blob${run}.xyz`, bytesOf(`xyz-${run}`))).json()
      const b = await get(bin.path)
      expect(b.headers.get('Content-Type')).toBe('application/octet-stream')
      expect(b.headers.get('Content-Disposition')).toMatch(/^attachment/)
    })

    it('every segment of the path is percent-encoded, so names with spaces and Chinese characters work', async () => {
      const up = await (await post(page.id, `我的 照片${run}.jpg`, bytesOf(`cjk-${run}`))).json()
      expect(fileUrl(spaceId, up.path)).toBe(`/api/spaces/${spaceId}/files/assets/${page.id}/${enc(up.name)}`)
      const r = await get(up.path)
      expect(r.status).toBe(200)
    })

    it('404 not_found for a file that is not there, a page, and a segment starting with a dot', async () => {
      for (const path of [
        `/assets/${page.id}/missing${run}.png`,
        `/${page.path}`,
        '/.git/config',
        `/assets/.hidden${run}/x.png`,
        `/assets/${page.id}/.x${run}.png`,
      ]) {
        const r = await get(path)
        expect([path, r.status, (await r.json()).error]).toEqual([path, 404, 'not_found'])
      }
    })
  })

  describe('import', () => {
    const enc = encodeURIComponent
    const IMPORT = () => `/api/spaces/${spaceId}/import`
    // Built by Node's own Response, not by jsdom: a real `fetch` only knows how to send its own kind of Blob.
    const blobOf = (text: string) => new Response(text).blob()
    const send = (query: string, body: string, type = 'application/octet-stream') =>
      admin.fetch(`${IMPORT()}${query}`, { method: 'POST', headers: { 'Content-Type': type }, body })
    const pagesIn = async (folder: string) => (await S.listPages({ folder })).items
    let n = 0
    const fresh = () => `${root}/import${++n}`

    it('meta reports the import limit in bytes', async () => {
      const m = await api.meta()
      expect(Number.isInteger(m.maxImport)).toBe(true)
      expect(m.maxImport).toBeGreaterThan(0)
    })

    it('a single Markdown file becomes a page: the title is its heading, which leaves the body', async () => {
      const folder = fresh()
      const report = await S.importMarkdown(await blobOf('# Imported heading\n\nFirst paragraph.\n'), {
        folder,
        name: 'notes.md',
      })
      expect(report.pages).toBe(1)
      expect(Array.isArray(report.skipped)).toBe(true)
      const items = await pagesIn(folder)
      expect(items).toHaveLength(1)
      const page = await S.getPage(items[0].id)
      expect(page.meta.title).toBe('Imported heading')
      expect(page.body).toContain('First paragraph.')
      expect(page.body).not.toContain('Imported heading')
    })

    it('without a heading or a header title, the title is the file name without its extension', async () => {
      const folder = fresh()
      await S.importMarkdown(await blobOf('Just text.\n'), { folder, name: 'plain-note.markdown' })
      const [item] = await pagesIn(folder)
      expect((await S.getPage(item.id)).meta.title).toBe('plain-note')
    })

    it('a header is kept key by key and the page gets a new id', async () => {
      const folder = fresh()
      await S.importMarkdown(
        await blobOf('---\nid: not-my-id\ntitle: From the header\nauthor: Ada\n---\n\nBody text.\n'),
        { folder, name: 'h.md' },
      )
      const [item] = await pagesIn(folder)
      const page = await S.getPage(item.id)
      expect(page.id).not.toBe('not-my-id')
      expect(page.meta.id).toBe(page.id)
      expect(page.meta.title).toBe('From the header')
      expect(page.meta.author).toBe('Ada')
      expect(page.body).toContain('Body text.')
    })

    it('importing the same file twice does not replace the first page', async () => {
      const folder = fresh()
      await S.importMarkdown(await blobOf('# Same\n\nOne.\n'), { folder, name: 'same.md' })
      await S.importMarkdown(await blobOf('# Same\n\nOne.\n'), { folder, name: 'same.md' })
      const items = await pagesIn(folder)
      expect(items).toHaveLength(2)
      expect(new Set(ids(items)).size).toBe(2)
    })

    it('400 for a single file without name, and for a name that is not Markdown', async () => {
      const folder = enc(fresh())
      const noName = await send(`?folder=${folder}`, '# x\n')
      expect(noName.status).toBe(400)
      expect((await noName.json()).error).toBe('invalid')
      expect((await send(`?folder=${folder}&name=notes.txt`, '# x\n')).status).toBe(400)
    })

    it('400 for an empty body', async () => {
      expect((await send(`?folder=${enc(fresh())}&name=a.md`, '')).status).toBe(400)
    })

    it('400 for a folder that is not a valid path', async () => {
      const r = await send(`?folder=${enc('.hidden')}&name=a.md`, '# x\n')
      expect(r.status).toBe(400)
      expect((await r.json()).error).toBe('invalid')
    })

    it('415 for a JSON content type', async () => {
      const r = await send(`?folder=${enc(fresh())}&name=a.md`, '# x\n', 'application/json')
      expect(r.status).toBe(415)
      expect((await r.json()).error).toBe('invalid')
    })
  })

  describe('history', () => {
    const HEX40 = /^[0-9a-f]{40}$/
    const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/
    // Against a real server the commit comes a few seconds after the write.
    const eventually = async <T>(fn: () => Promise<T>, done: (v: T) => boolean, ms = 25_000): Promise<T> => {
      const end = Date.now() + (REAL ? ms : 0)
      for (;;) {
        const v = await fn()
        if (done(v) || Date.now() >= end) return v
        await new Promise((r) => setTimeout(r, 400))
      }
    }
    const allHistory = async (id: string, limit: number) => {
      const out: Awaited<ReturnType<SpaceApi['history']>>['items'] = []
      let cursor = ''
      for (let guard = 0; guard < 200; guard++) {
        const r = await S.history(id, { limit, cursor: cursor || undefined })
        out.push(...r.items)
        cursor = r.next
        if (!cursor) return out
      }
      throw new Error('cursor never ended')
    }
    // A page with two contents, and its history once the second one is in.
    const twice = async (folder: string, title: string) => {
      const p = await mk(`${root}/${folder}`, { title, tags: [`h${run}`] }, '第一版的文字。\n')
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '第二版的文字，多了一些。\n' })
      const items =
        (await eventually(
          () => allHistory(p.id, 50),
          (l) => l[0]?.rev === saved.rev,
        )) ?? []
      return { p, saved, items }
    }

    it('lists the versions newest first, each with commit, time, author, path, rev and change', async () => {
      const { p, saved, items } = await twice('h1', `历史${run}`)
      expect(items.length).toBeGreaterThanOrEqual(1)
      for (const it of items) {
        expect(it.commit).toMatch(HEX40)
        expect(it.time).toMatch(RFC3339)
        expect(typeof it.author).toBe('string')
        expect(it.author).not.toBe('')
        expect(it.path.endsWith('.md')).toBe(true)
        expect(['add', 'edit', 'move', 'delete']).toContain(it.change)
        expect(it.rev).toMatch(HEX40)
      }
      // The newest is what the file is now; the oldest is where the page began.
      expect(items[0]).toMatchObject({ rev: saved.rev, path: saved.path })
      expect(items[items.length - 1].change).toBe('add')
      for (let i = 1; i < items.length; i++)
        expect(Date.parse(items[i - 1].time)).toBeGreaterThanOrEqual(Date.parse(items[i].time))
      expect(new Set(items.map((i) => i.commit)).size).toBe(items.length)
      expect((await S.history(p.id)).items.length).toBe(items.length)
    })

    it('a version is the page as it was: header and body, with the rev of that file', async () => {
      const { p, saved, items } = await twice('h2', `版本内容${run}`)
      const newest = await S.version(p.id, items[0].commit)
      expect(newest).toMatchObject({
        commit: items[0].commit,
        rev: saved.rev,
        path: saved.path,
        author: items[0].author,
      })
      expect(newest.time).toMatch(RFC3339)
      expect(newest.body).toBe('第二版的文字，多了一些。\n')
      expect(newest.meta).toMatchObject({ id: p.id, title: `版本内容${run}`, tags: [`h${run}`] })
      if (items.length > 1) {
        const oldest = await S.version(p.id, items[items.length - 1].commit)
        expect(oldest.body).toBe('第一版的文字。\n')
        expect(oldest.rev).toBe(p.rev)
      }
    })

    it('a commit that is not a version of this page is 404, even if it is a version of another page', async () => {
      const a = await twice('h3a', `甲${run}`)
      const b = await twice('h3b', `乙${run}`)
      const unknown = await failure(S.version(a.p.id, 'f'.repeat(40)))
      expect([unknown.status, unknown.code]).toEqual([404, 'not_found'])
      const other = await failure(S.version(a.p.id, b.items[0].commit))
      expect([other.status, other.code]).toEqual([404, 'not_found'])
      expect((await S.version(b.p.id, b.items[0].commit)).commit).toBe(b.items[0].commit)
    })

    it('history and versions of a page that does not exist are 404', async () => {
      for (const id of ['01ZZZZZZZZ', 'nope']) {
        const h = await failure(S.history(id))
        expect([h.status, h.code]).toEqual([404, 'not_found'])
        const v = await failure(S.version(id, 'a'.repeat(40)))
        expect([v.status, v.code]).toEqual([404, 'not_found'])
      }
    })

    it('pages with a cursor: one at a time gives the same versions as all at once, ending with an empty next', async () => {
      const { p, items } = await twice('h4', `分页${run}`)
      const one = await allHistory(p.id, 1)
      expect(one.map((i) => i.commit)).toEqual(items.map((i) => i.commit))
      const first = await S.history(p.id, { limit: 1 })
      expect(first.items).toHaveLength(1)
      expect(first.next === '').toBe(items.length === 1)
    })

    it('limit must be 1 to 100 and a cursor must be one the server gave: 400 invalid', async () => {
      const p = await mk(`${root}/h5`, { title: `参数${run}` })
      for (const q of ['limit=0', 'limit=101', 'limit=x', 'cursor=not-a-cursor']) {
        const r = await call(admin, 'GET', `${SP_PAGES}/${p.id}/history?${q}`)
        expect([r.status, r.json?.error], q).toEqual([400, 'invalid'])
      }
      expect((await call(admin, 'GET', `${SP_PAGES}/${p.id}/history?limit=100`)).status).toBe(200)
    })

    it('a draft is not a version; the version appears when the draft is committed', async () => {
      const p = await mk(`${root}/h6`, { title: `草稿版本${run}` }, '甲\n')
      const before = await eventually(
        () => allHistory(p.id, 50),
        (l) => l.length >= 1,
      )
      const r = await S.putDraft(p.id, { prev: '', baseRev: p.rev, meta: p.meta, body: '乙\n' })
      expect((await allHistory(p.id, 50)).map((i) => i.commit)).toEqual(before.map((i) => i.commit))
      const committed = await S.commitPage(p.id, r.version)
      const after = await eventually(
        () => allHistory(p.id, 50),
        (l) => l[0]?.rev === committed.rev,
      )
      expect(after[0].rev).toBe(committed.rev)
      expect((await S.version(p.id, after[0].commit)).body).toBe('乙\n')
    })

    it('renaming through the title keeps the history of the page, with the new path on the newest version', async () => {
      const p = await mk(`${root}/h7`, { title: `旧名${run}` }, '内容\n')
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, title: `新名${run}` }, body: '内容\n' })
      const items = await eventually(
        () => allHistory(p.id, 50),
        (l) => l[0]?.path === saved.path,
      )
      expect(items[0].path).toBe(`${root}/h7/新名${run}.md`)
      expect(items[items.length - 1].change).toBe('add')
      expect(items.length).toBeGreaterThanOrEqual(1)
    })
  })

  const url = (host: string, tail = 'notes.git') => `https://${host}/${tail}`

  describe.skipIf(!accounts)(
    'remote repository (accounts server only: a space is made for it; on a real server only what needs no remote is asserted)',
    () => {
      let rid = ''
      let R: SpaceApi
      beforeAll(async () => {
        rid = (await api.createSpace(`远端${run}`.slice(0, 40))).id
        R = spaceApi(rid)
      })
      it('a new space is local: `sync` says so and there is no remote', async () => {
        const mine = (await api.spaces()).spaces.find((x) => x.id === rid)
        expect(mine?.sync).toBe('local')
        const e = await failure(R.getRemote())
        expect([e.status, e.code]).toEqual([404, 'not_found'])
      })

      it('the SSH key is made once and returned again; rotating makes another while the space is local', async () => {
        const k = await R.remoteKey()
        expect(k.publicKey).toMatch(/^ssh-\S+ \S+/)
        expect(k.fingerprint).toMatch(/^SHA256:\S+/)
        expect(await R.remoteKey()).toEqual(k)
        const rotated = await R.remoteKey(true)
        expect(rotated.publicKey).not.toBe(k.publicKey)
        expect(rotated.fingerprint).not.toBe(k.fingerprint)
        expect(await R.remoteKey()).toEqual(rotated)
      })

      it('an address that is not https or SSH, or carries a password, is 400 invalid', async () => {
        const cred = { type: 'token' as const, token: 'x' }
        for (const bad of [
          'ftp://github.com/a/b.git',
          'github.com/a/b.git',
          'https://user:secret@github.com/a/b.git',
          'https://github.com',
          '',
          'git@github.com',
          'file:///tmp/x',
        ]) {
          const e = await failure(R.connectRemote({ url: bad, credential: cred }))
          expect([e.status, e.code], bad).toEqual([400, 'invalid'])
        }
        // Still local.
        expect((await failure(R.getRemote())).status).toBe(404)
      })

      it.skipIf(HOSTS_OPEN)(
        'a host the administrator did not allow is 403 forbidden, and is not taken for a refused origin',
        async () => {
          const e = await failure(
            R.connectRemote({ url: url('git.not-allowed.example'), credential: { type: 'token', token: 'x' } }),
          )
          expect([e.status, e.code]).toEqual([403, 'forbidden'])
          expect(e.message).not.toMatch(/allow-origin/)
          const ssh = await failure(
            R.connectRemote({ url: 'git@git.not-allowed.example:a/b.git', credential: { type: 'ssh' } }),
          )
          expect([ssh.status, ssh.code]).toEqual([403, 'forbidden'])
        },
      )
    },
  )

  describe.skipIf(!accounts || !!REAL)(
    'mock only (a real server has no remote to talk to): connecting, states and unavailable writes',
    () => {
      const fresh = async (name: string) => {
        const sp = await api.createSpace(`${name}${run}`.slice(0, 40))
        const R = spaceApi(sp.id)
        ids.set(R, sp.id)
        return { id: sp.id, R }
      }
      const token = { type: 'token' as const, username: 'u', token: 'secret-token-1' }
      const list = async (id: string) => (await api.spaces()).spaces.find((x) => x.id === id)!
      const ids = new Map<SpaceApi, string>()
      const spaceIdOf = (R: SpaceApi) => ids.get(R)!
      const titles = async (R: SpaceApi) =>
        (await R.listPages({ recursive: true, folder: '' })).items.map((i) => i.title)

      it('connecting to a remote with no such branch pushes the space: 200 with the remote, state ok, a sync time, and the token never comes back', async () => {
        const { id, R } = await fresh('推送')
        const r = await R.connectRemote({ url: url('github.com'), credential: token })
        expect(r).toMatchObject({
          url: url('github.com'),
          branch: 'main',
          state: 'ok',
          error: '',
          rewritten: null,
          credential: { type: 'token', username: 'u' },
        })
        expect(r.lastSync).toMatch(/^\d{4}-\d{2}-\d{2}T/)
        expect(JSON.stringify(r)).not.toContain('secret-token-1')
        expect(await R.getRemote()).toEqual(r)
        expect((await list(id)).sync).toBe('ok')
      })

      it('the branch defaults to main and can be named', async () => {
        const { R } = await fresh('分支')
        expect((await R.connectRemote({ url: url('github.com'), branch: 'notes', credential: token })).branch).toBe(
          'notes',
        )
        expect((await R.connectRemote({ url: url('gitlab.com'), credential: token })).branch).toBe('main')
      })

      it("a pristine space takes the remote's content", async () => {
        const { R } = await fresh('接收')
        expect(await titles(R)).toEqual([])
        await R.connectRemote({ url: url('github.com', 'ahead.git'), credential: token })
        expect((await titles(R)).length).toBeGreaterThan(0)
      })

      it('a space with pages: a remote that is ahead moves it forward, one that is behind changes nothing here', async () => {
        const a = await fresh('前进')
        await a.R.createPage({ meta: { title: '本地页面' } })
        await a.R.connectRemote({ url: url('github.com', 'ahead.git'), credential: token })
        expect(await titles(a.R)).toContain('本地页面')
        expect((await titles(a.R)).length).toBeGreaterThan(1)
        const b = await fresh('落后')
        await b.R.createPage({ meta: { title: '本地页面' } })
        await b.R.connectRemote({ url: url('github.com', 'behind.git'), credential: token })
        expect(await titles(b.R)).toEqual(['本地页面'])
      })

      it('both sides with content of their own is 409 conflict, and the space stays local', async () => {
        const { id, R } = await fresh('分叉')
        await R.createPage({ meta: { title: '我的' } })
        const e = await failure(R.connectRemote({ url: url('github.com', 'diverged.git'), credential: token }))
        expect([e.status, e.code]).toEqual([409, 'conflict'])
        expect(e.message).not.toBe('')
        expect((await failure(R.getRemote())).status).toBe(404)
        expect((await list(id)).sync).toBe('local')
      })

      it('a remote that cannot be reached, or refuses the credential, is 503 unavailable with a message, and nothing changes', async () => {
        const { id, R } = await fresh('连不上')
        for (const word of ['unreachable', 'refused']) {
          const e = await failure(R.connectRemote({ url: url('github.com', `${word}.git`), credential: token }))
          expect([e.status, e.code], word).toEqual([503, 'unavailable'])
          expect(e.message).not.toBe('')
        }
        expect((await failure(R.getRemote())).status).toBe(404)
        expect((await list(id)).sync).toBe('local')
      })

      it('a token left out keeps the stored one; with none stored it is 400; the user name defaults', async () => {
        const { R } = await fresh('令牌')
        const none = await failure(
          R.connectRemote({ url: url('github.com'), credential: { type: 'token', username: 'u' } }),
        )
        expect([none.status, none.code]).toEqual([400, 'invalid'])
        const first = await R.connectRemote({ url: url('github.com'), credential: { type: 'token', token: 'tok-1' } })
        expect(first.credential).toEqual({ type: 'token', username: 'x-access-token' })
        expect(server!.storedToken(spaceIdOf(R))).toBe('tok-1')
        const again = await R.connectRemote({
          url: url('github.com', 'other.git'),
          credential: { type: 'token', username: 'me' },
        })
        expect(again.credential).toEqual({ type: 'token', username: 'me' })
        expect(server!.storedToken(spaceIdOf(R))).toBe('tok-1')
        await R.connectRemote({ url: url('github.com', 'other.git'), credential: { type: 'token', token: 'tok-2' } })
        expect(server!.storedToken(spaceIdOf(R))).toBe('tok-2')
      })

      it('SSH: the key from /remote/key is the credential, the host key is shown, and the address must be an SSH one', async () => {
        const { R } = await fresh('密钥')
        const key = await R.remoteKey()
        const r = await R.connectRemote({ url: 'git@github.com:me/notes.git', credential: { type: 'ssh' } })
        expect(r.credential).toEqual({ type: 'ssh', publicKey: key.publicKey, fingerprint: key.fingerprint })
        expect(r.hostKey).toMatch(/^SHA256:/)
        for (const [u, c] of [
          [url('github.com'), { type: 'ssh' }],
          ['git@github.com:me/x.git', token],
        ] as const) {
          const e = await failure(R.connectRemote({ url: u, credential: c }))
          expect([e.status, e.code]).toEqual([400, 'invalid'])
        }
        const e = await failure(R.remoteKey(true))
        expect([e.status, e.code]).toEqual([409, 'conflict'])
        expect(
          (await R.connectRemote({ url: 'ssh://git@gitlab.com:2222/me/notes.git', credential: { type: 'ssh' } })).state,
        ).toBe('ok')
      })

      it('a space connected without making a key first still gets one', async () => {
        const { R } = await fresh('自动密钥')
        const r = await R.connectRemote({ url: 'git@codeberg.org:me/notes.git', credential: { type: 'ssh' } })
        expect(r.credential.type === 'ssh' && r.credential.publicKey).toMatch(/^ssh-/)
      })

      it('while the remote is unreachable, writes are 503 unavailable and change nothing; reads and drafts go on; the draft is written once it is back', async () => {
        const { id, R } = await fresh('离线')
        const p = await R.createPage({ meta: { title: '页' }, body: '旧\n' })
        await R.connectRemote({ url: url('github.com'), credential: token })
        server!.setRemote(id, 'offline', { error: '连接超时' })
        const remote = await R.getRemote()
        expect(remote).toMatchObject({ state: 'offline', error: '连接超时' })
        expect((await list(id)).sync).toBe('offline')
        const refused = async (fn: () => Promise<unknown>) => {
          const e = await failure(fn())
          expect([e.status, e.code]).toEqual([503, 'unavailable'])
          expect(e.message).not.toBe('')
        }
        await refused(() => R.createPage({ meta: { title: '新' } }))
        await refused(() => R.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: '改\n' }))
        await refused(() => R.movePage(p.id, '别处'))
        // A draft is stored; a commit is refused and keeps it.
        const d = await R.putDraft(p.id, { prev: '', baseRev: p.rev, meta: p.meta, body: '草稿里的\n' })
        await refused(() => R.commitPage(p.id, d.version))
        expect((await R.getPage(p.id)).draft?.version).toBe(d.version)
        await refused(() => R.deletePage(p.id))
        expect((await R.getPage(p.id)).body).toBe('旧\n')
        expect(await titles(R)).toEqual(['页'])
        // The server's own commit waits for the remote.
        clock += 20 * 60_000
        server!.sweep()
        expect((await R.getPage(p.id)).body).toBe('旧\n')
        server!.remoteReachable(id, true)
        const back = await R.syncRemote()
        expect(back).toMatchObject({ state: 'ok', error: '' })
        expect((await list(id)).sync).toBe('ok')
        server!.sweep()
        expect((await R.getPage(p.id)).body).toBe('草稿里的\n')
      })

      it('a sync while the remote is still out of reach answers 200 with state offline', async () => {
        const { id, R } = await fresh('仍离线')
        await R.connectRemote({ url: url('github.com'), credential: token })
        server!.setRemote(id, 'offline')
        expect((await R.syncRemote()).state).toBe('offline')
      })

      it('a rewritten remote pauses writes until its new history is accepted with the commit shown; a wrong commit is 409', async () => {
        const { id, R } = await fresh('改写')
        const p = await R.createPage({ meta: { title: '页' } })
        await R.connectRemote({ url: url('github.com'), credential: token })
        const e0 = await failure(R.acceptRemote('a'.repeat(40)))
        expect([e0.status, e0.code]).toEqual([409, 'conflict'])
        server!.setRemote(id, 'rewritten')
        const r = await R.getRemote()
        expect(r.state).toBe('rewritten')
        expect(r.rewritten).toEqual({
          lastGood: expect.stringMatching(/^[0-9a-f]{40}$/),
          remote: expect.stringMatching(/^[0-9a-f]{40}$/),
        })
        expect((await list(id)).sync).toBe('rewritten')
        expect((await R.getPage(p.id)).id).toBe(p.id)
        const e = await failure(R.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: 'x\n' }))
        expect([e.status, e.code]).toEqual([503, 'unavailable'])
        // Syncing does not clear it.
        expect((await R.syncRemote()).state).toBe('rewritten')
        const wrong = await failure(R.acceptRemote('b'.repeat(40)))
        expect([wrong.status, wrong.code]).toEqual([409, 'conflict'])
        const accepted = await R.acceptRemote(r.rewritten!.remote)
        expect(accepted).toMatchObject({ state: 'ok', rewritten: null, error: '' })
        expect((await R.savePage(p.id, { baseRev: p.rev, meta: p.meta, body: 'x\n' })).body).toBe('x\n')
      })

      it('state `error` carries the text to show', async () => {
        const { id, R } = await fresh('出错')
        await R.connectRemote({ url: url('github.com'), credential: token })
        server!.setRemote(id, 'error', { error: '凭据被拒绝' })
        expect(await R.getRemote()).toMatchObject({ state: 'error', error: '凭据被拒绝' })
        expect((await list(id)).sync).toBe('error')
      })

      it('disconnecting is 204: the space is local again with all its pages, and the stored token is gone', async () => {
        const { id, R } = await fresh('断开')
        await R.createPage({ meta: { title: '留着' } })
        await R.connectRemote({ url: url('github.com'), credential: token })
        await R.disconnectRemote()
        expect((await failure(R.getRemote())).status).toBe(404)
        expect((await list(id)).sync).toBe('local')
        expect(await titles(R)).toEqual(['留着'])
        expect(server!.storedToken(id)).toBeUndefined()
        expect((await failure(R.disconnectRemote())).status).toBe(404)
        expect((await failure(R.syncRemote())).status).toBe(404)
      })

      it('a space of someone else is 404 for the remote endpoints too', async () => {
        const r = await call(admin, 'GET', '/api/spaces/01ZZZZZZZZ/remote')
        expect([r.status, r.json.error]).toEqual([404, 'not_found'])
      })

      it('a host the administrator allows with `*` takes any', async () => {
        const open = createMockServer({
          empty: true,
          mode: 'accounts',
          now: () => clock,
          oidc: null,
          remote: { hosts: ['*'] },
        })
        const b = newBrowser(mockFetch(open, { jar: false }))
        await as(b, async () => {
          await api.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
          const sp = await api.createSpace('任意主机')
          expect(
            (await spaceApi(sp.id).connectRemote({ url: url('git.anywhere.example'), credential: token })).state,
          ).toBe('ok')
        })
      })

      it('the mock can start a space in a state (for the app to be seen by hand)', async () => {
        const m = createMockServer({
          empty: true,
          mode: 'accounts',
          now: () => clock,
          oidc: null,
          remote: { start: 'offline' },
        })
        const b = newBrowser(mockFetch(m, { jar: false }))
        await as(b, async () => {
          await api.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
          const first = (await api.spaces()).spaces[0]
          expect(first.sync).toBe('offline')
          expect(await spaceApi(first.id).getRemote()).toMatchObject({ state: 'offline' })
        })
      })
    },
  )

  describe.skipIf(!accounts || !!REAL)(
    'mock only (skipped on a real server: no provider, no clean first run, and the rate limiter would lock us out): single sign-on, setup, rate limits',
    () => {
      let sso: MockServer
      let ssoFetch: typeof fetch
      const ssoBrowser = () => newBrowser(ssoFetch)
      const start = async (b: Browser, body: Record<string, unknown>) =>
        (await call(b, 'POST', '/api/auth/oidc/start', body)).json as { url: string }
      // What the provider's redirect does: the browser follows the url.
      const callback = (b: Browser, url: string) => call(b, 'GET', url)

      beforeAll(() => {
        sso = createMockServer({
          empty: true,
          mode: 'accounts',
          admin: false,
          now: () => clock,
          oidc: { name: 'Company SSO', sub: 'sub-1' },
          setupToken: 'tok',
        })
        ssoFetch = mockFetch(sso, { jar: false })
      })

      it('without a provider, start is 404', async () => {
        const r = await call(admin, 'POST', '/api/auth/oidc/start', { intent: 'login' })
        expect([r.status, r.json.error]).toEqual([404, 'not_found'])
      })

      it('session is `setup: true` while there is no user, and names the provider', async () => {
        const s = (await call(ssoBrowser(), 'GET', '/api/session')).json
        expect(s).toMatchObject({
          mode: 'accounts',
          user: null,
          setup: true,
          methods: { password: true, oidc: { name: 'Company SSO' } },
        })
      })

      it('setup: a wrong token is 403 forbidden, a bad username or password 400; then 201 with a session and a personal space; after that 409 exists', async () => {
        const b = ssoBrowser()
        const wrong = await call(b, 'POST', '/api/setup', {
          token: 'nope',
          username: 'boss',
          password: 'a-long-password',
        })
        expect([wrong.status, wrong.json.error]).toEqual([403, 'forbidden'])
        expect(
          (await call(b, 'POST', '/api/setup', { token: 'tok', username: 'B', password: 'a-long-password' })).status,
        ).toBe(400)
        expect(
          (await call(b, 'POST', '/api/setup', { token: 'tok', username: 'boss', password: 'short' })).status,
        ).toBe(400)
        const ok = await call(b, 'POST', '/api/setup', { token: 'tok', username: 'Boss', password: 'a-long-password' })
        expect(ok.status).toBe(201)
        expect(ok.json.user).toMatchObject({ username: 'boss', name: 'boss', role: 'admin' })
        expect(((await call(b, 'GET', '/api/spaces')).json.spaces as SpaceInfo[])[0].personal).toBe(true)
        expect((await call(ssoBrowser(), 'GET', '/api/session')).json.setup).toBe(false)
        const again = await call(ssoBrowser(), 'POST', '/api/setup', {
          token: 'tok',
          username: 'second',
          password: 'a-long-password',
        })
        expect([again.status, again.json.error]).toEqual([409, 'exists'])
      })

      it('login through the provider: an identity nobody has is redirected to /login?sso_error=no_account; a linked one signs in and goes to the return path', async () => {
        const b = ssoBrowser()
        const first = await start(b, { intent: 'login', return: '/s/abc' })
        const denied = await callback(b, first.url)
        expect([denied.status, denied.headers.get('Location')]).toEqual([303, '/login?sso_error=no_account'])

        // Link it from a signed-in account, then sign in with it.
        const me = ssoBrowser()
        expect((await call(me, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })).status).toBe(
          200,
        )
        const link = await start(me, { intent: 'link', return: '/settings' })
        const linked = await callback(me, link.url)
        expect([linked.status, linked.headers.get('Location')]).toEqual([303, '/settings'])
        const acct = (await call(me, 'GET', '/api/account')).json
        expect(acct.identities).toEqual([
          expect.objectContaining({ provider: 'Company SSO', label: expect.any(String) }),
        ])

        const b2 = ssoBrowser()
        const again = await start(b2, { intent: 'login', return: '/s/abc' })
        const ok = await callback(b2, again.url)
        expect([ok.status, ok.headers.get('Location')]).toEqual([303, '/s/abc'])
        expect((await call(b2, 'GET', '/api/session')).json.user.username).toBe('boss')
      })

      it('the return path must be a plain path inside the app: anything else becomes /', async () => {
        const me = ssoBrowser()
        await call(me, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })
        for (const bad of ['//evil.example', 'https://evil.example/x', '/a\\b', '', 'relative']) {
          const b = ssoBrowser()
          const s = await start(b, { intent: 'login', return: bad })
          const r = await callback(b, s.url)
          expect(r.headers.get('Location'), bad).toBe('/')
        }
      })

      it('an attempt is used once, belongs to the browser that started it, and expires after 10 minutes: /login?sso_error=expired', async () => {
        const b = ssoBrowser()
        const s = await start(b, { intent: 'login' })
        expect((await callback(b, s.url)).headers.get('Location')).toBe('/')
        expect((await callback(b, s.url)).headers.get('Location')).toBe('/login?sso_error=expired')
        const other = ssoBrowser()
        const s2 = await start(b, { intent: 'login' })
        expect((await callback(other, s2.url)).headers.get('Location')).toBe('/login?sso_error=expired')
        const s3 = await start(b, { intent: 'login' })
        clock += 11 * 60_000
        expect((await callback(b, s3.url)).headers.get('Location')).toBe('/login?sso_error=expired')
      })

      it('the provider refusing is idp_error; link failures go to /settings?sso_error=', async () => {
        const b = ssoBrowser()
        const s = await start(b, { intent: 'login' })
        expect((await callback(b, `${s.url}&error=access_denied`)).headers.get('Location')).toBe(
          '/login?sso_error=idp_error',
        )
        const me = ssoBrowser()
        await call(me, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })
        const l = await start(me, { intent: 'link' })
        expect((await callback(me, `${l.url}&error=access_denied`)).headers.get('Location')).toBe(
          '/settings?sso_error=idp_error',
        )
      })

      it('link needs a session; an identity another user has is identity_in_use; invite and setup tokens are checked', async () => {
        const anon = ssoBrowser()
        const noSession = await call(anon, 'POST', '/api/auth/oidc/start', { intent: 'link' })
        expect(noSession.status).toBe(401)

        const boss = ssoBrowser()
        await call(boss, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })
        const inv = (await call(boss, 'POST', '/api/invites', { role: 'member' })).json
        // `boss` already has sub-1, so an invite through the same identity cannot create a second account.
        const b = ssoBrowser()
        const s = await start(b, { intent: 'invite', token: inv.token })
        expect((await callback(b, s.url)).headers.get('Location')).toBe('/login?sso_error=identity_in_use')
        const bad = await start(ssoBrowser(), { intent: 'invite', token: 'nope' })
        expect(bad.url).toContain('/api/auth/oidc/callback')
        const b2 = ssoBrowser()
        const s2 = await start(b2, { intent: 'invite', token: 'nope' })
        expect((await callback(b2, s2.url)).headers.get('Location')).toBe('/login?sso_error=invite_invalid')
        const b3 = ssoBrowser()
        const s3 = await start(b3, { intent: 'setup', token: 'tok' })
        expect((await callback(b3, s3.url)).headers.get('Location')).toBe('/login?sso_error=setup_invalid')
      })

      it('a disabled user signing in through the provider is /login?sso_error=disabled; the last identity of a user with no password cannot be removed (409)', async () => {
        const boss = ssoBrowser()
        await call(boss, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })
        const sso2 = createMockServer({
          empty: true,
          mode: 'accounts',
          now: () => clock,
          oidc: { name: 'Company SSO', autoCreate: true, sub: 'sub-9' },
          admin: { username: 'root', password: 'a-long-password' },
        })
        const f2 = mockFetch(sso2, { jar: false })
        const mine = newBrowser(f2)
        const s = (await call(mine, 'POST', '/api/auth/oidc/start', { intent: 'login' })).json
        // autoCreate: a member account appears and is signed in.
        expect((await callback(mine, s.url)).headers.get('Location')).toBe('/')
        const acct = (await call(mine, 'GET', '/api/account')).json
        expect(acct.hasPassword).toBe(false)
        expect(acct.user.role).toBe('member')
        const only = acct.identities[0].id
        const refused = await call(mine, 'DELETE', `/api/account/identities/${only}`)
        expect([refused.status, refused.json.error]).toEqual([409, 'conflict'])
        // Set a password (no `current`), after which it can go.
        expect((await call(mine, 'POST', '/api/account/password', { new: 'a-long-password' })).status).toBe(204)
        expect((await call(mine, 'DELETE', `/api/account/identities/${only}`)).status).toBe(204)
        expect((await call(mine, 'GET', '/api/account')).json.identities).toEqual([])

        const rootB = newBrowser(f2)
        await call(rootB, 'POST', '/api/login', { username: 'root', password: 'a-long-password' })
        const users = (await call(rootB, 'GET', '/api/users')).json.users as User[]
        const member = users.find((u) => u.role === 'member')!
        // Re-link, disable, and sign in through the provider.
        const l = (await call(mine, 'POST', '/api/auth/oidc/start', { intent: 'link' })).json
        await callback(mine, l.url)
        await call(rootB, 'PATCH', `/api/users/${member.id}`, { disabled: true })
        const again = newBrowser(f2)
        const s2 = (await call(again, 'POST', '/api/auth/oidc/start', { intent: 'login' })).json
        expect((await callback(again, s2.url)).headers.get('Location')).toBe('/login?sso_error=disabled')
      })

      it('too many wrong passwords against one name is 429 rate_limited with Retry-After, even for the right password, until the wait is over', async () => {
        const b = ssoBrowser()
        for (let i = 0; i < 5; i++)
          expect(
            (
              await call(b, 'POST', '/api/login', { username: 'boss', password: 'wrong-wrong' }, JSON_HEADERS, {
                expect429: true,
              })
            ).status,
          ).toBe(401)
        const r = await call(b, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' }, JSON_HEADERS, {
          expect429: true,
        })
        expect([r.status, r.json.error]).toEqual([429, 'rate_limited'])
        expect(Number(r.headers.get('Retry-After'))).toBeGreaterThan(0)
        const e = await as(b, () => failure(api.login({ username: 'boss', password: 'a-long-password' })))
        expect(e.retryAfter).toBeGreaterThan(0)
        clock += 61_000
        expect((await call(b, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })).status).toBe(
          200,
        )
      })
    },
  )
  // The sign-in providers, passkeys, captcha, security settings and audit log (milestone 2.8).
  describe.skipIf(!accounts || !!REAL)(
    'mock only (skipped on a real server: it starts from known settings): providers, passkeys, captcha, security settings and the audit log',
    () => {
      let s28: MockServer
      let f28: typeof fetch
      const b28 = () => newBrowser(f28)
      const inAs = async (user = MOCK_ADMIN.username, password = MOCK_ADMIN.password) => {
        const b = b28()
        expect((await call(b, 'POST', '/api/login', { username: user, password })).status).toBe(200)
        return b
      }
      const company = {
        slug: 'company',
        kind: 'oidc',
        name: 'Company SSO',
        icon: 'preset:microsoft',
        oidc: { issuer: 'https://login.example/v2.0', clientId: 'id', clientSecret: 'x' },
      }

      beforeAll(() => {
        s28 = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: null })
        f28 = mockFetch(s28, { jar: false })
      })

      it("the sign-in page's providers: none at first (local_only), then the enabled ones in order with their icon, and what the mode says", async () => {
        const root = await inAs()
        const none = await call(b28(), 'GET', '/api/auth/providers')
        expect(none.headers.get('Cache-Control')).toBe('no-store')
        expect(none.json).toEqual({
          providers: [],
          loginMode: 'local_only',
          local: true,
          sso: false,
          passkey: true,
          register: false,
          forgot: false,
        })
        const made = await call(root, 'POST', '/api/admin/auth/providers', company)
        expect(made.status).toBe(201)
        expect(made.json).toMatchObject({
          slug: 'company',
          kind: 'oidc',
          enabled: true,
          people: 0,
          status: { ok: true },
          oidc: { hasSecret: true, redirectUrl: 'https://notes.example.com/api/auth/oidc/company/callback' },
        })
        expect(made.json.oidc).not.toHaveProperty('clientSecret')
        const list = (await call(b28(), 'GET', '/api/auth/providers')).json
        expect(list).toEqual({
          providers: [{ slug: 'company', kind: 'oidc', name: 'Company SSO', icon: 'preset:microsoft' }],
          loginMode: 'dual',
          local: true,
          sso: true,
          passkey: true,
          register: false,
          forgot: false,
        })
        await call(root, 'PUT', '/api/admin/security', { login: { mode: 'sso_redirect' } })
        expect((await call(b28(), 'GET', '/api/auth/providers')).json).toMatchObject({
          loginMode: 'sso_redirect',
          local: false,
          sso: true,
        })
        await call(root, 'PUT', '/api/admin/security', { login: { mode: 'dual' } })
      })

      it("a provider's start: GET is a 303 to the provider, a disabled or unknown one goes to /login?sso_error=provider_unavailable, POST gives {url}", async () => {
        const go = await call(b28(), 'GET', '/api/auth/oidc/company/start?return=%2Fs%2Fabc')
        expect(go.status).toBe(303)
        expect(go.headers.get('Location')).toContain('/api/auth/oidc/company/callback')
        expect(go.headers.get('Set-Cookie')).toBeTruthy()
        const unknown = await call(b28(), 'GET', '/api/auth/oidc/nobody/start')
        expect([unknown.status, unknown.headers.get('Location')]).toEqual([
          303,
          '/login?sso_error=provider_unavailable',
        ])
        expect((await call(b28(), 'POST', '/api/auth/oidc/nobody/start', { intent: 'login' })).status).toBe(404)
        expect((await call(b28(), 'POST', '/api/auth/oidc/company/start', { intent: 'login' })).json.url).toContain(
          '/api/auth/oidc/company/callback?state=',
        )
      })

      it('the provider refusals have their own codes', async () => {
        const root = await inAs()
        const refused = async (body: Record<string, unknown>) => {
          const r = await call(root, 'POST', '/api/admin/auth/providers', body)
          return [r.status, r.json.error]
        }
        expect(await refused({ ...company })).toEqual([409, 'exists'])
        expect(await refused({ ...company, slug: 'second' })).toEqual([409, 'provider_issuer_in_use'])
        expect(
          await refused({ ...company, slug: 'http', oidc: { issuer: 'http://x.example', clientId: 'c' } }),
        ).toEqual([400, 'oidc_issuer_invalid'])
        expect(
          await refused({ slug: 'gh', kind: 'github', name: 'GitHub', autoCreate: true, github: { clientId: 'c' } }),
        ).toEqual([400, 'auto_create_needs_org'])
        expect(
          await refused({
            slug: 'saml',
            kind: 'saml',
            name: 'S',
            saml: { metadataUrl: 'https://x.example/bad-metadata' },
          }),
        ).toEqual([400, 'saml_metadata_invalid'])
        expect(
          await refused({
            slug: 'Bad Slug',
            kind: 'oidc',
            name: 'S',
            oidc: { issuer: 'https://y.example', clientId: 'c' },
          }),
        ).toEqual([400, 'invalid'])
      })

      it('an unreachable provider is stored switched off with status.ok false; refresh looks again', async () => {
        const root = await inAs()
        const r = await call(root, 'POST', '/api/admin/auth/providers', {
          slug: 'dead',
          kind: 'oidc',
          name: 'Dead',
          oidc: { issuer: 'https://unreachable.example', clientId: 'c' },
        })
        expect([r.status, r.json.enabled, r.json.status.ok]).toEqual([201, false, false])
        const again = await call(root, 'POST', '/api/admin/auth/providers/dead/refresh')
        expect([again.status, again.json.status.ok]).toEqual([200, false])
        expect((await call(root, 'DELETE', '/api/admin/auth/providers/dead')).status).toBe(204)
      })

      it("deleting a provider that is somebody's only way in is 409 provider_in_use with the count, until force=1", async () => {
        const root = await inAs()
        await call(root, 'PATCH', '/api/admin/auth/providers/company', { autoCreate: true })
        const b = b28()
        const st = (await call(b, 'POST', '/api/auth/oidc/company/start', { intent: 'login' })).json
        const cb = await call(b, 'GET', st.url)
        expect(cb.status).toBe(303)
        const refused = await call(root, 'DELETE', '/api/admin/auth/providers/company')
        expect([refused.status, refused.json.error, refused.json.accounts]).toEqual([409, 'provider_in_use', 1])
        const acct = (await call(b, 'GET', '/api/account')).json
        expect(acct.identities).toEqual([
          expect.objectContaining({ provider: 'Company SSO', providerSlug: 'company', kind: 'oidc' }),
        ])
        expect((await call(root, 'DELETE', '/api/admin/auth/providers/company?force=1')).status).toBe(204)
        // The identity stays, with no provider.
        expect((await call(b, 'GET', '/api/account')).json.identities[0]).toMatchObject({ providerSlug: '', kind: '' })
      })

      it('security: the defaults, a change that is kept, and each refusal; nothing is half saved', async () => {
        const root = await inAs()
        const got = (await call(root, 'GET', '/api/admin/security')).json
        expect(got).toMatchObject({
          login: { mode: 'dual', ssoOnly: false },
          passkeys: { enabled: true, available: true, requireRoles: [] },
          captcha: { provider: 'image', hasSecret: false },
          lockout: { enabled: false, minutes: 15, scope: 'ip_account' },
          limits: { perMinute: 10, maxFailures: 10, windowMinutes: 15, sessionIdleDays: 30, sessionMaxDays: 90 },
          notices: [],
        })
        const bad = async (body: unknown) => {
          const r = await call(root, 'PUT', '/api/admin/security', body)
          return [r.status, r.json.error]
        }
        expect(await bad({ login: { mode: 'nope' } })).toEqual([400, 'invalid'])
        expect(await bad({ limits: { sessionIdleDays: 400 } })).toEqual([400, 'invalid'])
        expect(await bad({ limits: { sessionIdleDays: 50, sessionMaxDays: 40 } })).toEqual([400, 'invalid'])
        expect(await bad({ captcha: { provider: 'turnstile', contexts: { login: { on: true } } } })).toEqual([
          400,
          'captcha_site_key_required',
        ])
        expect(
          await bad({ captcha: { provider: 'turnstile', siteKey: 'k', contexts: { login: { on: true } } } }),
        ).toEqual([400, 'captcha_secret_required'])
        // Refused as a whole: the valid part of the last one was not stored.
        expect(await bad({ lockout: { minutes: 20 }, limits: { perMinute: 0 } })).toEqual([400, 'invalid'])
        expect((await call(root, 'GET', '/api/admin/security')).json.lockout.minutes).toBe(15)
        const ok = await call(root, 'PUT', '/api/admin/security', {
          lockout: { enabled: true, minutes: 20, scope: 'ip' },
          captcha: {
            provider: 'turnstile',
            siteKey: 'k',
            secret: 's',
            contexts: { login: { on: true, when: 'always' } },
          },
        })
        expect(ok.status).toBe(200)
        expect(ok.json).toMatchObject({
          lockout: { enabled: true, minutes: 20, scope: 'ip' },
          captcha: { provider: 'turnstile', siteKey: 'k', hasSecret: true },
        })
        expect(JSON.stringify(ok.json)).not.toContain('"s"')
        // A secret stored for another provider does not count.
        expect(
          await bad({ captcha: { provider: 'hcaptcha', siteKey: 'k2', contexts: { invite: { on: true } } } }),
        ).toEqual([400, 'captcha_secret_required'])
        // Clearing the secret while a context needs it is refused; with the context off it works.
        expect(await bad({ captcha: { secret: '' } })).toEqual([400, 'captcha_secret_required'])
        expect(
          (
            await call(root, 'PUT', '/api/admin/security', {
              captcha: { secret: '', contexts: { login: { on: false } } },
            })
          ).json.captcha.hasSecret,
        ).toBe(false)
        expect(
          (
            await call(root, 'PUT', '/api/admin/security', {
              captcha: { provider: 'image', contexts: { login: { on: false } } },
              lockout: { enabled: false, minutes: 15, scope: 'ip_account' },
            })
          ).status,
        ).toBe(200)
      })

      it("ssoOnly: in effect only while a provider is enabled; then a member's password is refused after it was checked, an administrator's is not", async () => {
        const root = await inAs()
        // With no provider the page offers no button, so refusing passwords would lock the door.
        expect((await call(root, 'PUT', '/api/admin/security', { login: { ssoOnly: true } })).json.login).toMatchObject(
          { ssoOnly: true, ssoOnlyInEffect: false, effectiveMode: 'local_only', ssoAvailable: false },
        )
        await call(root, 'PUT', '/api/admin/security', { login: { ssoOnly: false } })
        expect(
          (
            await call(root, 'POST', '/api/admin/auth/providers', {
              ...company,
              slug: 'acme',
              oidc: { issuer: 'https://acme.example', clientId: 'c' },
            })
          ).status,
        ).toBe(201)
        const member = (await call(root, 'POST', '/api/invites', { role: 'member' })).json
        const joined = await call(b28(), 'POST', '/api/invites/accept', {
          token: member.token,
          username: 'sso-m',
          password: 'a-long-password',
        })
        expect(joined.status).toBe(201)
        expect((await call(root, 'PUT', '/api/admin/security', { login: { ssoOnly: true } })).status).toBe(200)
        const refused = await call(b28(), 'POST', '/api/login', { username: 'sso-m', password: 'a-long-password' })
        expect([refused.status, refused.json.error]).toEqual([403, 'local_login_refused'])
        // A wrong password still gets the ordinary answer: the code says nothing to someone who does not know it.
        expect((await call(b28(), 'POST', '/api/login', { username: 'sso-m', password: 'wrong-wrong' })).status).toBe(
          401,
        )
        expect(
          (await call(b28(), 'POST', '/api/login', { username: MOCK_ADMIN.username, password: MOCK_ADMIN.password }))
            .status,
        ).toBe(200)
        // No invite for a member can be accepted with a password.
        const inv2 = (await call(root, 'POST', '/api/invites', { role: 'member' })).json
        expect(
          (
            await call(b28(), 'POST', '/api/invites/accept', {
              token: inv2.token,
              username: 'sso-m2',
              password: 'a-long-password',
            })
          ).json.error,
        ).toBe('local_login_refused')
        await call(root, 'PUT', '/api/admin/security', { login: { ssoOnly: false } })
      })

      it('captcha: asked for by the context, an image is answered once, a wrong or missing answer is 400 captcha_failed; unknown contexts are 400', async () => {
        const root = await inAs()
        await call(root, 'PUT', '/api/admin/security', {
          captcha: { contexts: { login: { on: true, when: 'always' } } },
        })
        expect((await call(b28(), 'GET', '/api/captcha?ctx=invite')).json).toEqual({ required: false })
        expect((await call(b28(), 'GET', '/api/captcha?ctx=forgot')).json).toEqual({ required: false })
        expect((await call(b28(), 'GET', '/api/captcha?ctx=nope')).status).toBe(400)
        const c = (await call(b28(), 'GET', '/api/captcha?ctx=login&account=x')).json
        expect(c).toMatchObject({
          required: true,
          provider: 'image',
          id: expect.any(String),
          image: expect.stringMatching(/^data:image\//),
        })
        const missing = await call(b28(), 'POST', '/api/login', {
          username: MOCK_ADMIN.username,
          password: MOCK_ADMIN.password,
        })
        expect([missing.status, missing.json.error]).toEqual([400, 'captcha_failed'])
        const wrong = await call(b28(), 'POST', '/api/login', {
          username: MOCK_ADMIN.username,
          password: MOCK_ADMIN.password,
          captcha: { id: c.id, answer: 'zzzz' },
        })
        expect([wrong.status, wrong.json.error]).toEqual([400, 'captcha_failed'])
        // Used once, right or wrong: the same challenge cannot be answered again.
        const reused = await call(b28(), 'POST', '/api/login', {
          username: MOCK_ADMIN.username,
          password: MOCK_ADMIN.password,
          captcha: { id: c.id, answer: '4821' },
        })
        expect(reused.json.error).toBe('captcha_failed')
        const fresh = (await call(b28(), 'GET', '/api/captcha?ctx=login')).json
        expect(
          (
            await call(b28(), 'POST', '/api/login', {
              username: MOCK_ADMIN.username,
              password: MOCK_ADMIN.password,
              captcha: { id: fresh.id, answer: '4821' },
            })
          ).status,
        ).toBe(200)
        await call(root, 'PUT', '/api/admin/security', { captcha: { contexts: { login: { on: false } } } })
      })

      it('after_failures: a name that failed enough asks for a captcha, others do not', async () => {
        const root = await inAs()
        await call(root, 'PUT', '/api/admin/security', {
          captcha: { contexts: { login: { on: true, when: 'after_failures', failures: 2 } } },
        })
        expect((await call(b28(), 'GET', '/api/captcha?ctx=login&account=sso-m')).json.required).toBe(false)
        for (let i = 0; i < 2; i++)
          await call(b28(), 'POST', '/api/login', { username: 'sso-m', password: 'wrong-wrong' })
        expect((await call(b28(), 'GET', '/api/captcha?ctx=login&account=sso-m')).json.required).toBe(true)
        expect((await call(b28(), 'GET', '/api/captcha?ctx=login&account=someone-else')).json.required).toBe(false)
        await call(root, 'PUT', '/api/admin/security', { captcha: { contexts: { login: { on: false } } } })
      })

      it('passkeys: register (named, a credential once), sign in with it, rename, delete; a ceremony is used once; a required person is held until they have one', async () => {
        const root = await inAs()
        const me = await inAs('sso-m', 'a-long-password')
        const begin = await call(me, 'POST', '/api/account/passkeys/begin', { name: 'MacBook' })
        expect(begin.status).toBe(200)
        expect(begin.json.options).toMatchObject({
          challenge: expect.any(String),
          user: { name: 'sso-m' },
          excludeCredentials: [],
        })
        const done = await call(me, 'POST', `/api/account/passkeys/finish?ceremony=${begin.json.ceremony}`, {
          id: 'cred-m',
          type: 'public-key',
          response: {},
        })
        expect(done.status).toBe(201)
        expect(done.json.passkeys).toEqual([expect.objectContaining({ name: 'MacBook', synced: false })])
        expect(
          (await call(me, 'POST', `/api/account/passkeys/finish?ceremony=${begin.json.ceremony}`, { id: 'cred-m2' }))
            .json.error,
        ).toBe('passkey_failed')
        const again = await call(me, 'POST', '/api/account/passkeys/begin', { name: 'Again' })
        expect(again.json.options.excludeCredentials).toHaveLength(1)
        expect(
          (await call(me, 'POST', `/api/account/passkeys/finish?ceremony=${again.json.ceremony}`, { id: 'cred-m' }))
            .json.error,
        ).toBe('exists')
        // Sign in with it: discoverable, then by name.
        const lg = await call(b28(), 'POST', '/api/auth/passkey/begin', {})
        const ok = await call(b28(), 'POST', `/api/auth/passkey/finish?ceremony=${lg.json.ceremony}&return=%2Fs%2Fx`, {
          id: 'cred-m',
        })
        expect([ok.status, ok.json.return, ok.json.user.username]).toEqual([200, '/s/x', 'sso-m'])
        const bad = await call(b28(), 'POST', `/api/auth/passkey/finish?ceremony=${lg.json.ceremony}`, { id: 'cred-m' })
        expect([bad.status, bad.json.error]).toEqual([401, 'passkey_failed'])
        const named = await call(b28(), 'POST', '/api/auth/passkey/begin', { username: 'sso-m' })
        expect(named.json.options.allowCredentials).toEqual([{ type: 'public-key', id: 'cred-m' }])
        // A name nobody has looks the same: one made-up credential.
        expect(
          (await call(b28(), 'POST', '/api/auth/passkey/begin', { username: 'nobody-here' })).json.options
            .allowCredentials,
        ).toHaveLength(1)
        // Required of this person: the password is right, so the answer says passkey_required.
        const uid = (await call(me, 'GET', '/api/account')).json.user.id
        expect(
          (await call(root, 'PATCH', `/api/users/${uid}`, { passkeyRequired: true })).json.user.passkeyRequired,
        ).toBe(true)
        const must = await call(b28(), 'POST', '/api/login', { username: 'sso-m', password: 'a-long-password' })
        expect([must.status, must.json.error]).toEqual([403, 'passkey_required'])
        // Rename and delete; the admin sees them and can delete one.
        const id = done.json.passkeys[0].id
        expect((await call(me, 'PATCH', `/api/account/passkeys/${id}`, { name: 'Phone' })).json.name).toBe('Phone')
        const list = (await call(root, 'GET', `/api/users/${uid}/passkeys`)).json.passkeys
        expect(list.map((p: { name: string }) => p.name)).toEqual(['Phone'])
        expect(
          (await call(root, 'GET', '/api/users')).json.users.find((u: { id: string }) => u.id === uid),
        ).toMatchObject({ passkeys: 1, sso: 0 })
        expect((await call(root, 'DELETE', `/api/users/${uid}/passkeys/${id}`)).status).toBe(204)
        // With none left and one required, the next password sign-in is held: only the passkey endpoints and sign-out work.
        const held = b28()
        expect(
          (await call(held, 'POST', '/api/login', { username: 'sso-m', password: 'a-long-password' })).status,
        ).toBe(200)
        expect((await call(held, 'GET', '/api/session')).json.hold).toBe('passkey')
        expect((await call(held, 'GET', '/api/spaces')).json.error).toBe('passkey_required')
        expect((await call(held, 'GET', '/api/account')).status).toBe(200)
        const hb = await call(held, 'POST', '/api/account/passkeys/begin', { name: 'New' })
        expect(
          (await call(held, 'POST', `/api/account/passkeys/finish?ceremony=${hb.json.ceremony}`, { id: 'cred-new' }))
            .status,
        ).toBe(201)
        expect((await call(held, 'GET', '/api/session')).json.hold).toBe(null)
        expect((await call(held, 'GET', '/api/spaces')).status).toBe(200)
        await call(root, 'PATCH', `/api/users/${uid}`, { passkeyRequired: false })
      })

      it('the audit log: newest first, filtered by kind and user, with a cursor', async () => {
        const root = await inAs()
        const all = (await call(root, 'GET', '/api/admin/audit?limit=5')).json
        expect(all.items).toHaveLength(5)
        expect(all.next).toBeTruthy()
        for (let i = 1; i < all.items.length; i++) expect(all.items[i - 1].id > all.items[i].id).toBe(true)
        const more = (await call(root, 'GET', `/api/admin/audit?limit=5&cursor=${all.next}`)).json
        expect(more.items[0].id).toBe(all.next)
        const kinds = (await call(root, 'GET', '/api/admin/audit?kind=security_changed&limit=200')).json.items
        expect(kinds.length).toBeGreaterThan(0)
        expect(kinds.every((r: { kind: string }) => r.kind === 'security_changed')).toBe(true)
        expect(kinds[0]).toMatchObject({
          actor: expect.any(String),
          detail: { from: expect.any(Object), to: expect.any(Object) },
        })
        // A secret is never in it.
        expect(JSON.stringify(kinds)).not.toContain('"s"')
        expect((await call(root, 'GET', '/api/admin/audit?limit=0')).status).toBe(400)
      })
    },
  )
}
