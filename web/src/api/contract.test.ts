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
// client address (ten free, then growing waits). The suite makes about seven such
// failures a run and waits out any 429 it gets, so run it again straight away
// and it slows down rather than fails; start from a fresh data directory when
// that matters.
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
import type { AdminSpace, ListParams, Member, Page, PageSummary, SpaceInfo, User } from './types'

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
  ? [{ name: CONTRACT_USER && CONTRACT_PASSWORD ? 'real server, accounts mode' : 'real server, single-user mode', mode: CONTRACT_USER && CONTRACT_PASSWORD ? 'accounts' : 'single' }]
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

  const browser = () => newBrowser(base)
  // Run `fn` as another browser (the client reads `globalThis.fetch`).
  const as = async <T,>(b: Browser, fn: () => Promise<T>): Promise<T> => {
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
    const res = await b.fetch(path, { method, headers, body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body), redirect: 'manual' })
    if (res.status === 429 && !opts.expect429 && REAL) {
      const wait = Math.min(Number(res.headers.get('Retry-After')) || 1, 20)
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
      server = createMockServer({ empty: true, mode: target.mode, now: () => clock, oidc: null })
      base = mockFetch(server, { jar: false })
    }
    admin = browser()
    globalThis.fetch = admin.fetch
    if (accounts) {
      const user = REAL ? CONTRACT_USER! : MOCK_ADMIN.username
      const password = REAL ? CONTRACT_PASSWORD! : MOCK_ADMIN.password
      await api.login({ username: user, password })
      spaceId = (await api.spaces()).spaces[0].id
    }
    S = spaceApi(spaceId)
    SP_PAGES = `/api/spaces/${spaceId}/pages`
  })
  afterAll(async () => {
    globalThis.fetch = admin.fetch
    for (const id of created.users) await adminApi.updateUser(id, { disabled: true }).catch(() => undefined)
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

    it('every space carries the caller\'s role and permissions, and whether it is disabled; meta and the list say the same', async () => {
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

    it('spaces lists the caller\'s spaces, the personal one first', async () => {
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
        ['GET', '/api/account'],
        ['GET', '/api/users'],
        ['GET', '/api/invites'],
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
      for (const [method, path] of [['GET', `/api/spaces/${spaceId}/remote`], ['POST', `/api/spaces/${spaceId}/remote/key`], ['POST', `/api/spaces/${spaceId}/remote/sync`]] as const) {
        const r = await call(admin, method, path, method === 'GET' ? undefined : {})
        expect([r.status, r.json?.error], `${method} ${path}`).toEqual([404, 'not_found'])
      }
    })

    it('there is only the one space, and others are 404', async () => {
      const r = await call(admin, 'GET', `/api/spaces/${'01ZZZZZZZZ'}/pages`)
      expect([r.status, r.json?.error]).toEqual([404, 'not_found'])
    })
  })

  describe.skipIf(!accounts)('accounts server only (skipped on a single-user server, where these answer 404): signing in', () => {
    const uniq = (p: string) => `${p}${run}`.slice(0, 30)
    // A member made through an invite, signed in in its own browser.
    const newMember = async (name: string, password = 'a-long-password') => {
      const invite = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
      const b = browser()
      const r = await call(b, 'POST', '/api/invites/accept', { token: invite.token, username: name, name: `名 ${name}`, password })
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
      const r = await call(browser(), 'POST', '/api/setup', { token: 'not-the-token', username: uniq('late'), password: 'a-long-password' })
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
        expect((await call(other, 'POST', '/api/login', { username: m.user.username, password: m.password })).status).toBe(200)
        const wrong = await call(m.browser, 'POST', '/api/account/password', { current: 'not-the-one', new: 'another-password' })
        expect([wrong.status, wrong.json.error]).toEqual([403, 'forbidden'])
        const short = await call(m.browser, 'POST', '/api/account/password', { current: m.password, new: 'short' })
        expect([short.status, short.json.error]).toEqual([400, 'invalid'])
        const ok = await call(m.browser, 'POST', '/api/account/password', { current: m.password, new: 'another-password' })
        expect(ok.status).toBe(204)
        expect((await call(m.browser, 'GET', '/api/account')).status).toBe(200)
        expect((await call(other, 'GET', '/api/account')).status).toBe(401)
        const fresh = browser()
        expect((await call(fresh, 'POST', '/api/login', { username: m.user.username, password: m.password })).status).toBe(401)
        expect((await call(fresh, 'POST', '/api/login', { username: m.user.username, password: 'another-password' })).status).toBe(200)
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
        expect((await as(admin, () => adminApi.users({ q: m.user.name }))).users.map((u) => u.id)).toContain(m.user.id)
        expect((await as(admin, () => adminApi.users({ q: 'zzz-no-such-user-zzz' }))).users).toEqual([])
        const one = (await as(admin, () => adminApi.users({ limit: 1 }))).users
        expect(one.map((u) => u.id)).toEqual([all[0].id])
        for (const bad of ['0', '201', 'x']) {
          const r = await call(admin, 'GET', `/api/users?limit=${bad}`)
          expect([r.status, r.json.error], bad).toEqual([400, 'invalid'])
        }
      })

      it('an administrator lists users, ordered by created; a member gets 403 forbidden for users and invites', async () => {
        const m = await newMember(uniq('us'))
        const { users } = await as(admin, () => adminApi.users())
        expect(users.find((u) => u.id === m.user.id)).toMatchObject({ username: m.user.username, role: 'member', disabled: false })
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
        expect((await call(browser(), 'POST', '/api/login', { username: m.user.username, password: m.password })).status).toBe(200)
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
        expect((await call(admin, 'GET', '/api/session')).json.user).toMatchObject({ id: me.id, role: 'admin', disabled: false })
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
        expect(ok.json).toEqual({ role: 'member', expires: inv.expires })
        const bad = await call(anon, 'POST', '/api/invites/check', { token: 'not-a-token' })
        expect([bad.status, bad.json.error]).toEqual([404, 'not_found'])
      })

      it('accept is public: 201 with the user and a session, a personal space of their own; the invite works once', async () => {
        const inv = await as(admin, () => adminApi.createInvite({ role: 'member', days: 1 }))
        const name = uniq('ac2')
        const b = browser()
        const r = await call(b, 'POST', '/api/invites/accept', { token: inv.token, username: name.toUpperCase(), name: '新人', password: 'a-long-password' })
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
        const taken = await call(browser(), 'POST', '/api/invites/accept', { token: inv.token, username: m.user.username, password: 'a-long-password' })
        expect([taken.status, taken.json.error]).toEqual([409, 'exists'])
        const short = await call(browser(), 'POST', '/api/invites/accept', { token: inv.token, username: uniq('sh'), password: 'short' })
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
        expect((await call(m.browser, 'GET', '/api/meta')).json.spaces.map((s: SpaceInfo) => s.id)).toEqual(spaces.map((s) => s.id))
        // The new space is a working space.
        const page = await call(m.browser, 'POST', `/api/spaces/${made.json.id}/pages`, { meta: { title: '新空间里的页面' }, body: 'x\n' })
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
        const mixed = await call(m.browser, 'PATCH', `/api/spaces/${made.json.id}`, { name: 'Changed', inbox: '.hidden' })
        expect(mixed.status).toBe(400)
        const { spaces } = (await call(m.browser, 'GET', '/api/spaces')).json as { spaces: SpaceInfo[] }
        expect(spaces.find((s) => s.id === made.json.id)).toMatchObject({ name: 'Good', inbox: 'Keep' })
      })

      it('the inbox is only a name: no folder until a page is made in it, and changing it moves nothing', async () => {
        const m = await newMember(uniq('ib3'))
        const sp = await call(m.browser, 'POST', '/api/spaces', { name: 'Quick', inbox: '收件箱' })
        const base = `/api/spaces/${sp.json.id}`
        expect(((await call(m.browser, 'GET', `${base}/tree`)).json.folders as { path: string }[]).map((f) => f.path)).not.toContain('收件箱')
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
        const listed = (await call(m.browser, 'GET', `${base}/pages?folder=${encodeURIComponent('收件箱')}`)).json.items as PageSummary[]
        expect(listed.map((i) => i.id)).toEqual([note.json.id])
      })

      it('a space of someone else is 404 not_found, the same as one that does not exist: pages, folders, drafts, rename', async () => {
        const m = await newMember(uniq('is'))
        const mine = ((await call(m.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[])[0]
        const own = await call(m.browser, 'POST', `/api/spaces/${mine.id}/pages`, { meta: { title: '只有我能看' }, body: '秘密\n' })
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
        // An administrator is no member, but may open any space; it is not in their own list.
        for (const path of [`${mineSpace}/pages`, `${mineSpace}/pages/${own.json.id}`, `${mineSpace}/tree`]) {
          expect((await call(admin, 'GET', path)).status, path).toBe(200)
        }
        expect(((await call(admin, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).map((s) => s.id)).not.toContain(mine.id)
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
    const roleOf = async (b: Browser, space: string) => ((await call(b, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find((s) => s.id === space)

    describe('members and permissions', () => {
      it('adds a member by username with a role; lists owners first with `you`; the role and its permissions show in the member\'s own list', async () => {
        const owner = await newMember(uniq('mo'))
        const guest = await newMember(uniq('mg'))
        const sp = await ownedSpace(owner, 'Shared')
        expect(sp).toMatchObject({ role: 'owner', permissions: ['read', 'write', 'members', 'manage'], disabled: false })
        // Not a member yet: not even found.
        expect((await call(guest.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
        const added = await addMember(owner, sp.id, guest.user.username.toUpperCase(), 'viewer')
        expect(added).toEqual({ userId: guest.user.id, username: guest.user.username, name: guest.user.name, role: 'viewer', you: false })
        const list = (await call(owner.browser, 'GET', `/api/spaces/${sp.id}/members`)).json.members as Member[]
        expect(list.map((m) => [m.userId, m.role, m.you])).toEqual([
          [owner.user.id, 'owner', true],
          [guest.user.id, 'viewer', false],
        ])
        expect(await roleOf(guest.browser, sp.id)).toMatchObject({ role: 'viewer', permissions: ['read'], disabled: false })
        // The role changes at the member's very next request.
        const changed = await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/${guest.user.id}`, { role: 'editor' })
        expect(changed.status).toBe(200)
        expect(changed.json).toMatchObject({ userId: guest.user.id, role: 'editor', you: false })
        expect(await roleOf(guest.browser, sp.id)).toMatchObject({ role: 'editor', permissions: ['read', 'write'] })
        // Removed: the space is gone for them, at once.
        expect((await call(owner.browser, 'DELETE', `/api/spaces/${sp.id}/members/${guest.user.id}`)).status).toBe(204)
        expect((await call(guest.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
        expect(await roleOf(guest.browser, sp.id)).toBeUndefined()
      })

      it('refuses what it should: an unknown user 404 user_not_found, a member already 409 exists, a role that is none 400 invalid, a personal space 409 personal_space', async () => {
        const owner = await newMember(uniq('me'))
        const guest = await newMember(uniq('mf'))
        const sp = await ownedSpace(owner, 'Rules')
        const add = (username: string, role: string, space = sp.id) => call(owner.browser, 'POST', `/api/spaces/${space}/members`, { username, role })
        const unknown = await add(uniq('nobody'), 'viewer')
        expect([unknown.status, unknown.json.error]).toEqual([404, 'user_not_found'])
        await addMember(owner, sp.id, guest.user.username, 'viewer')
        const twice = await add(guest.user.username, 'editor')
        expect([twice.status, twice.json.error]).toEqual([409, 'exists'])
        const own = await add(owner.user.username, 'editor')
        expect([own.status, own.json.error]).toEqual([409, 'exists'])
        const bad = await add(guest.user.username, 'boss', (await ownedSpace(owner, 'Rules 2')).id)
        expect([bad.status, bad.json.error]).toEqual([400, 'invalid'])
        const personal = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find((s) => s.personal)!
        const shared = await add(guest.user.username, 'viewer', personal.id)
        expect([shared.status, shared.json.error]).toEqual([409, 'personal_space'])
        const notMember = await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/01ZZZZZZZZ`, { role: 'viewer' })
        expect([notMember.status, notMember.json.error]).toEqual([404, 'not_found'])
        const badRole = await call(owner.browser, 'PATCH', `/api/spaces/${sp.id}/members/${guest.user.id}`, { role: 'boss' })
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

      it('any member may leave a space, a viewer too; someone else\'s removal needs the members permission', async () => {
        const owner = await newMember(uniq('lv'))
        const viewer = await newMember(uniq('vw'))
        const editor = await newMember(uniq('ed'))
        const sp = await ownedSpace(owner, 'Leaving')
        await addMember(owner, sp.id, viewer.user.username, 'viewer')
        await addMember(owner, sp.id, editor.user.username, 'editor')
        const other = await call(editor.browser, 'DELETE', `/api/spaces/${sp.id}/members/${viewer.user.id}`)
        expect([other.status, other.json.error]).toEqual([403, 'no_permission'])
        expect((await call(viewer.browser, 'DELETE', `/api/spaces/${sp.id}/members/${viewer.user.id}`)).status).toBe(204)
        expect((await call(viewer.browser, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
        expect((await call(editor.browser, 'DELETE', `/api/spaces/${sp.id}/members/${editor.user.id}`)).status).toBe(204)
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
        for (const path of [`${base}/pages`, `${base}/pages/${page.json.id}`, `${base}/tree`, `${base}/tags`, `${base}/members`, `${base}/views?folder=`, `${base}/pages/groups?folder=&key=status`]) {
          expect((await call(viewer.browser, 'GET', path)).status, `viewer GET ${path}`).toBe(200)
        }
        const writes: [string, string, unknown][] = [
          ['POST', `${base}/pages`, { body: 'x' }],
          ['PUT', `${base}/pages/${page.json.id}`, { baseRev: page.json.rev, meta: page.json.meta, body: '改\n' }],
          ['PUT', `${base}/pages/${page.json.id}/draft`, { baseRev: page.json.rev, meta: page.json.meta, body: '改\n', prev: '' }],
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

        const made = await call(editor.browser, 'POST', `${base}/pages`, { meta: { title: '编辑者写的' }, body: 'x\n' })
        expect(made.status).toBe(201)
        expect((await call(editor.browser, 'PUT', `${base}/tags/blue`, { color: 'red' })).status).toBe(200)
        // An editor writes properties of a page, and reads the types; making one needs `manage`.
        const patched = await call(editor.browser, 'PATCH', `${base}/pages/${page.json.id}/props`, { baseRev: page.json.rev, set: { status: 'x' } })
        expect([patched.status, patched.json.meta.status]).toEqual([200, 'x'])
        expect((await call(editor.browser, 'GET', `${base}/types`)).status).toBe(200)
        // Views are a shared arrangement of pages: whoever may write pages may save them.
        const noViews = await call(editor.browser, 'GET', `${base}/views?folder=`)
        const savedViews = await call(editor.browser, 'PUT', `${base}/views`, { folder: '', baseRev: noViews.json.rev, views: [{ name: '全部', layout: 'cards' }] })
        expect([savedViews.status, savedViews.json.views[0].name]).toEqual([200, '全部'])
        expect((await call(viewer.browser, 'GET', `${base}/views?folder=`)).json).toEqual(savedViews.json)
        for (const [method, path, body] of [
          ['PUT', `${base}/types/task`, { title: '任务', fields: [] }],
          ['DELETE', `${base}/types/task`, {}],
          ['PUT', `${base}/folders/type`, { folder: '', type: '' }],
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

    describe('administration', () => {
      const adminPaths: [string, string, unknown?][] = [
        ['GET', '/api/admin/spaces'],
        ['GET', '/api/admin/status'],
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
          const r = await as(admin, () => adminApi.adminSpaces({ q: query || undefined, cursor: cursor || undefined, limit }))
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
        const found = (await listAll(name.toLowerCase()))
        expect(found.map((s) => s.id)).toEqual([sp.id])
        expect(found[0]).toMatchObject({ id: sp.id, name, personal: false, disabled: false, members: 2, sync: 'local', owner: { id: owner.user.id, username: owner.user.username, name: owner.user.name } })
        expect(typeof found[0].created).toBe('string')
        // Not measured yet, or measured: both are shaped alike.
        expect(found[0].sizeBytes === null ? found[0].sizeAt : typeof found[0].sizeAt).toEqual(found[0].sizeBytes === null ? null : 'string')
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
        await call(owner.browser, 'POST', `/api/spaces/${sp.id}/pages`, { meta: { title: '占地方' }, body: '一些字\n' })
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
        expect((await call(guest.browser, 'DELETE', `/api/spaces/${sp.id}/members/${guest.user.id}`)).status).toBe(204)
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
        const transfer = (userId: string, space = sp.id) => call(admin, 'POST', `/api/admin/spaces/${space}/transfer`, { userId })
        const personal = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find((s) => s.personal)!
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

      it('disables a space: it stays in its members\' lists, marked, and nothing opens it for anyone, an administrator included; enabling undoes it', async () => {
        const owner = await newMember(uniq('do'))
        const sp = await ownedSpace(owner, 'Switched off')
        const page = await call(owner.browser, 'POST', `/api/spaces/${sp.id}/pages`, { meta: { title: '还在' }, body: 'x\n' })
        expect(page.status).toBe(201)
        expect((await call(admin, 'POST', `/api/admin/spaces/${sp.id}/disable`, {})).status).toBe(204)
        expect(await roleOf(owner.browser, sp.id)).toMatchObject({ role: 'owner', disabled: true })
        expect((await call(owner.browser, 'GET', '/api/meta')).json.spaces.find((s: SpaceInfo) => s.id === sp.id).disabled).toBe(true)
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
        const personal = ((await call(owner.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[]).find((s) => s.personal)!
        const refused = await call(admin, 'DELETE', `/api/admin/spaces/${personal.id}`)
        expect([refused.status, refused.json.error]).toEqual([409, 'personal_space'])
        const done = await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`)
        expect(done.status).toBe(200)
        expect(done.json.trashed).toMatch(/^trash\//)
        expect(done.json.trashed).toContain(sp.id)
        expect(typeof done.json.cacheTrashed).toBe('string')
        expect(done.json.warnings).toEqual([])
        for (const b of [owner.browser, guest.browser, admin]) expect((await call(b, 'GET', `/api/spaces/${sp.id}/pages`)).status).toBe(404)
        expect(await roleOf(guest.browser, sp.id)).toBeUndefined()
        expect((await listAll('to be deleted')).some((s) => s.id === sp.id)).toBe(false)
        expect((await call(admin, 'DELETE', `/api/admin/spaces/${sp.id}`)).status).toBe(404)
      })

      it('reports the server\'s status', async () => {
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
      const p = await mk(`${root}/titled/deep`, { id: 'CLIENTSENT', created: '1999-01-01', title: `暮雪${run}`, subtitle: '副', tags: [`tg${run}`] }, '正文\n')
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
      expect(folders.map((f) => f.path)).toEqual(expect.arrayContaining([`${root}`, `${root}/titled`, `${root}/titled/deep`]))
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
      const put = (payload: unknown) => fetch(url, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(payload) })
      for (const payload of [{ baseRev: p.rev, meta: p.meta }, { baseRev: p.rev, body: 'y\n' }, { meta: p.meta, body: 'y\n' }, {}]) {
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
      const r = await fetch(`${SP_PAGES}/${p.id}`, { method: 'PUT', headers: { 'Content-Type': 'text/plain' }, body: JSON.stringify({ baseRev: p.rev, meta: p.meta, body: 'z\n' }) })
      expect([400, 415]).toContain(r.status)
      expect(((await r.json()) as { error: string }).error).toBe('invalid')
    })

    it('keeps created and id when left out, removes other absent keys, and returns unknown fields exactly', async () => {
      const p = await mk(`${root}/header`, { title: `头部${run}` }, 'x\n')
      const created = p.meta.created
      const weird = { nested: { a: [1, 2, { b: null }] }, 数字: 42, flag: true, 空: '' }
      const a = await S.savePage(p.id, { baseRev: p.rev, meta: { title: `头部${run}`, series: '冬季', ...weird }, body: 'x\n' })
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
    const bad = ['.hidden', 'a/.b', '..', 'a/..', 'name.', 'name ', 'a/name.', 'assets', 'assets/inside', '长'.repeat(86), 'a//b', '/lead', 'trail/']

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
        const path = name.startsWith('/') || name.startsWith('.') || ['assets', 'assets/inside'].includes(name) || name === '..' || name === 'a/..' || name === 'a//b' ? name : `${root}/${name}`
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
      expect(ids(await walk({ folder: `${root}/fm2/made/new`, recursive: true }))).toEqual(expect.arrayContaining([top.id, deep.id]))
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
      for (let i = 0; i < 7; i++) created.push(await mk(folder, { title: `列${String(i).padStart(2, '0')}${run}`, tags: i % 2 ? [`odd${run}`, `all${run}`] : [`all${run}`] }, `正文${i}\n`))
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
      expect((await walk({ tag: [`all${run}`] }))).toHaveLength(8)
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
      for (let i = 1; i < byCreated.length; i++) expect((byCreated[i - 1].created ?? '') >= (byCreated[i].created ?? '')).toBe(true)
      expect(ids(await walk({ folder, sort: 'created' })).sort()).toEqual(ids(items).sort())
    })

    it('summaries carry the documented fields', async () => {
      const s = (await walk({ folder })).find((p) => p.id === created[1].id)!
      expect(s).toMatchObject({ id: created[1].id, folder, untitled: false, title: `列01${run}`, label: `列01${run}`, tags: [`odd${run}`, `all${run}`] })
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
      expect(ids(await walk({ folder: f, updatedFrom: day, updatedTo: day }))).toEqual(expect.arrayContaining([titled.id]))
      // An empty range is an empty list, not an error.
      expect(await walk({ folder: f, updatedFrom: shift(day, 1), updatedTo: shift(day, -1) })).toEqual([])
    })

    it('createdFrom and createdTo use the header\'s created day', async () => {
      const day = String(titled.meta.created)
      expect(day).toMatch(/^\d{4}-\d{2}-\d{2}$/)
      expect(ids(await walk({ folder: f, createdFrom: day, createdTo: day }))).toEqual(expect.arrayContaining([titled.id, bodied.id]))
      expect(await walk({ folder: f, createdFrom: shift(day, 2) })).toEqual([])
      expect(await walk({ folder: f, createdTo: shift(day, -2) })).toEqual([])
    })

    it('ranges combine with the text, the folder and the tag filters, and with the cursor', async () => {
      const day = dayOf(titled.updated)
      expect(ids(await walk({ q: word, folder: f, updatedFrom: day, updatedTo: day }, 1)).sort()).toEqual([titled.id, bodied.id].sort())
      expect(await walk({ q: word, folder: f, updatedFrom: shift(day, 2) })).toEqual([])
      expect(await walk({ q: word, in: 'title', folder: f, createdTo: shift(String(titled.meta.created), -2) })).toEqual([])
    })

    it('a date that is not a day is 400 invalid', async () => {
      for (const q of ['updatedFrom=yesterday', 'updatedTo=2026-13-40', 'createdFrom=2026-1-1']) {
        const r = await call(admin, 'GET', `${SP_PAGES}?${q}`)
        expect([r.status, r.json?.error]).toEqual([400, 'invalid'])
      }
    })

    it.skipIf(!!REAL)('mock only: a created range picks pages by their own created day (set with a save)', async () => {
      const older = await mk(f, { title: `旧${run}` })
      const saved = await S.savePage(older.id, { baseRev: older.rev, meta: { ...older.meta, created: '2020-03-15' }, body: older.body })
      expect(saved.meta.created).toBe('2020-03-15')
      expect(ids(await walk({ folder: f, createdFrom: '2020-03-15', createdTo: '2020-03-15' }))).toEqual([older.id])
      expect(ids(await walk({ folder: f, createdTo: '2020-03-14' }))).toEqual([])
      expect(ids(await walk({ folder: f, createdTo: '2020-03-16' }))).toEqual([older.id])
      expect(ids(await walk({ folder: f, createdFrom: '2020-03-16', updatedFrom: dayOf(saved.updated) }))).toEqual(expect.arrayContaining([titled.id]))
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
      expect((await failure(S.putType(`x${run}`, { title: '', fields: [{ key: 'tags', kind: 'text' }] }))).code).toBe('invalid')
      expect((await failure(S.putType(`x${run}`, { title: '', fields: [{ key: 'a', kind: 'select', options: [] }] }))).code).toBe('invalid')
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
      expect([A.effectiveType, C.effectiveType, D.effectiveType, E.effectiveType]).toEqual([typeName, typeName, `memo${run}`, ''])
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
      expect(withProps.items[0]).toMatchObject({ title: 'A', rev: A.rev, props: { status: 'done', points: 5, due: '2026-03-01', labels: ['a'] } })
      expect(withProps.items[2].props).toEqual({})
    })

    it('props=1 says which properties it holds only in part: cut text, an object, a list that lost an element', async () => {
      const whole = await mk(`${root}/partialprops`, { title: 'Whole', status: 'done', labels: ['a'] })
      const cut = await mk(`${root}/partialprops`, { title: 'Cut', note: '字'.repeat(301), obj: { a: 1 }, list: ['a', { x: 1 }], fine: 'ok' })
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
      const order = async (sort: ListParams['sort']) => titles(await walk({ folder, recursive: true, sort, props: true }, 1))
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
      const q = async (f: ListParams['filter'], sort: ListParams['sort'] = 'title') => titles(await walk({ folder, recursive: true, sort, filter: f }))
      expect(await q([{ key: 'status', op: 'in', values: ['todo', 'doing'] }])).toBe('B C')
      expect(await q([{ key: 'labels', op: 'in', values: ['b'] }])).toBe('B')
      expect(await q([{ key: 'points', op: 'range', values: [6, null] }])).toBe('B C')
      expect(await q([{ key: 'due', op: 'range', values: ['2026-02-01', '2026-12-31'] }])).toBe('A')
      expect(await q([{ key: 'due', op: 'empty' }])).toBe('C D')
      expect(await q([{ key: 'due', op: 'notEmpty' }])).toBe('A B')
      expect(await q([{ key: 'status', op: 'contains', values: ['DO'] }])).toBe('A B C')
      expect(await q([{ key: 'status', op: 'in', values: ['todo', 'done'] }, { key: 'due', op: 'notEmpty' }], '-prop:points')).toBe('B A')
      for (const bad of [filter([{ key: 'a', op: 'like', values: ['x'] }]), filter([{ key: 'a', op: 'range', values: [1] }]), 'nope']) {
        const r = await call(admin, 'GET', `${SP_PAGES}?folder=${encodeURIComponent(folder)}&filter=${encodeURIComponent(bad)}`)
        expect([r.status, r.json.error], bad).toEqual([400, 'invalid'])
      }
      expect((await call(admin, 'GET', `${SP_PAGES}?props=maybe`)).status).toBe(400)
    })

    it('changes properties of the header only: body and other keys stay, a list is replaced, unset and blank values remove, the listing sees it at once', async () => {
      const p = await S.patchProps(A.id, { baseRev: A.rev, set: { status: 'doing', owner: 'Ann', labels: ['b'] }, unset: ['due'] })
      expect(p.rev).not.toBe(A.rev)
      expect(p.body).toBe(A.body)
      expect(p.meta).toMatchObject({ status: 'doing', owner: 'Ann', labels: ['b'], title: 'A', points: 5 })
      expect('due' in p.meta).toBe(false)
      expect(p.effectiveType).toBe(typeName)
      const seen = (await S.listPages({ folder, props: true, filter: [{ key: 'owner', op: 'in', values: ['Ann'] }] })).items
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

    it('keeps the caller\'s draft when properties change', async () => {
      const page = await S.getPage(A.id)
      await S.putDraft(A.id, { baseRev: page.rev, meta: page.meta, body: '打了一半\n', prev: '' })
      const p = await S.patchProps(A.id, { baseRev: page.rev, set: { status: 'done' } })
      const again = await S.getPage(A.id)
      expect(again.draft).toMatchObject({ body: '打了一半\n', stale: true })
      await S.deleteDraft(A.id)
      A = p
    })

    it('changes a type by its keys: the order, titles and options follow the request, a field left out goes, pages keep their values', async () => {
      const t = await S.putType(typeName, { baseRev: typeRev, title: '任务单', fields: [{ key: 'points', title: '点数' }, { key: 'status', title: '状态', options: ['todo', 'doing', 'done', 'dropped'] }] })
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
    const getViews = async (scope: string) => (await call(admin, 'GET', viewsUrl(scope))).json as { views: Record<string, any>[]; rev: string }
    const putViews = (body: Record<string, unknown>) => call(admin, 'PUT', `/api/spaces/${spaceId}/views`, body)
    const titles = (items: PageSummary[]) => items.map((i) => i.title).join(' ')
    const listUrl = (query: string) => `${SP_PAGES}?folder=${enc(folder)}&${query}`
    const groupsUrl = (query: string) => `${SP_PAGES}/groups?${query}`
    const options = ['draft', 'review', 'final']
    let typeRev = ''

    it('has no views until some are saved; a save makes the ids and the rev, and reads back the same', async () => {
      const t = await S.putType(typeName, { title: '文案', fields: [{ key: 'status', kind: 'select', options }, { key: 'points', kind: 'number' }] })
      typeRev = t.rev
      await S.createFolder(folder)
      await S.bindFolder(folder, typeName)
      const empty = await getViews(`folder=${enc(folder)}`)
      expect(empty.views).toEqual([])
      expect(empty.rev).toMatch(/^[0-9a-f]{40}$/)
      const board = { name: '按状态', layout: 'board', group: 'status', sort: '-prop:points', filter: [{ key: 'points', op: 'range', values: [1, null] }] }
      const table = { name: '表格', layout: 'table', hidden: ['points'], color: 'teal' }
      const saved = await putViews({ folder, baseRev: empty.rev, views: [board, table] })
      expect(saved.status).toBe(200)
      const [a, b] = saved.json.views
      expect(a).toMatchObject({ name: '按状态', layout: 'board', group: 'status', sort: '-prop:points', hidden: [], filter: [{ key: 'points', op: 'range', values: [1, null] }] })
      expect(b).toMatchObject({ name: '表格', layout: 'table', group: '', sort: '', filter: [], hidden: ['points'], color: 'teal' })
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
      const next = await putViews({ folder, baseRev: cur.rev, views: [b, { ...a, name: '看板' }, { name: '连读', layout: 'read' }] })
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
        ['a filter that is none', { folder, baseRev: cur.rev, views: [{ ...ok, filter: [{ key: 'a', op: 'like' }] }] }, 400, 'invalid'],
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
      expect((await putViews({ folder, baseRev: (await getViews(`folder=${enc(folder)}`)).rev, views: [ok] })).status).toBe(200)
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
      const top = await putViews({ folder: from, baseRev: (await getViews(`folder=${enc(from)}`)).rev, views: [{ name: '上', layout: 'list' }] })
      const low = await putViews({ folder: `${from}/sub`, baseRev: (await getViews(`folder=${enc(`${from}/sub`)}`)).rev, views: [{ name: '下', layout: 'board', group: 'status' }] })
      await S.moveFolder(from, `${root}/vmoved`)
      expect(await getViews(`folder=${enc(`${root}/vmoved`)}`)).toEqual(top.json)
      expect((await getViews(`folder=${enc(`${root}/vmoved/sub`)}`)).views[0]).toMatchObject({ id: low.json.views[0].id, name: '下', group: 'status' })
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
      const saved = await S.putViews({ folder: place }, { baseRev: none.rev, views: [{ name: '一', layout: 'list', sort: '', group: '', filter: [], hidden: [] }, { name: '二', layout: 'board', sort: '', group: 'status', filter: [{ key: 'status', op: 'notIn', values: ['draft'] }], hidden: [], cardFields: ['points'] }] })
      expect(saved.views.map((v) => v.name)).toEqual(['一', '二'])
      expect(saved.views[1]).toMatchObject({ group: 'status', cardFields: ['points'], filter: [{ key: 'status', op: 'notIn', values: ['draft'] }] })
      expect(await S.views({ folder: place })).toEqual(saved)
      // A save from the rev before is refused, and the error carries the views as they are.
      const stale = await S.putViews({ folder: place }, { baseRev: none.rev, views: [] }).then(() => null, (e: unknown) => e as ApiError)
      expect([stale?.status, stale?.code]).toEqual([409, 'conflict'])
      expect((stale?.current as unknown as { views: unknown[]; rev: string }).rev).toBe(saved.rev)
      expect((stale?.current as unknown as { views: unknown[] }).views).toHaveLength(2)
      // A view of a tag is kept under the tag.
      const tag = `typedtag${run}`
      const tagViews = await S.putViews({ tag }, { baseRev: (await S.views({ tag })).rev, views: [{ name: '读', layout: 'read', sort: '', group: '', filter: [], hidden: [] }] })
      expect((await S.views({ tag })).views[0].id).toBe(tagViews.views[0].id)
      // The groups of a folder: how many pages hold each value.
      await mk(place, { title: 'g1', status: 'draft' }, '')
      await mk(place, { title: 'g2', status: 'draft' }, '')
      await mk(place, { title: 'g3' }, '')
      expect(await S.groups({ folder: place, key: 'status' })).toEqual({ groups: [{ value: 'draft', count: 2 }], empty: 1, truncated: false })
      expect((await S.groups({ tag: [tag], key: 'status' })).groups).toEqual([])
    })

    describe('a board over a select field', () => {
      const pages: Record<string, Page> = {}
      const filter = (f: unknown[]) => `filter=${enc(JSON.stringify(f))}`
      it('lists each column on its own: the options, the other values, the empty', async () => {
        for (const [title, meta] of [['甲', { status: 'final', points: 3 }], ['乙', { status: 'draft', points: 9 }], ['丙', { status: 'review' }], ['丁', { status: 'legacy' }], ['戊', {}], ['己', { status: 'draft' }], ['庚', { status: ['draft', 'x'] }]] as [string, Record<string, unknown>][]) {
          pages[title] = await mk(folder, { title, ...meta }, `正文 ${title}\n`)
        }
        const col = async (f: unknown[]) => titles((await call(admin, 'GET', listUrl(`${filter(f)}&sort=title&limit=20`))).json.items)
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
        const some = (await call(admin, 'GET', groupsUrl(`folder=${enc(folder)}&key=status&${filter([{ key: 'points', op: 'notEmpty' }])}`))).json
        expect(some.groups).toEqual([{ value: 'draft', count: 1 }, { value: 'final', count: 1 }])
        expect(some.empty).toBe(0)
        expect((await call(admin, 'GET', groupsUrl(`folder=${enc(folder)}&key=nothing${run}`))).json).toMatchObject({ groups: [], empty: 7 })
        for (const q of ['folder=a', 'key=status', `folder=${enc(folder)}&key=s&recursive=maybe`, `folder=${enc(folder)}&key=s&filter=nope`]) {
          const r = await call(admin, 'GET', groupsUrl(q))
          expect([r.status, r.json.error], q).toEqual([400, 'invalid'])
        }
      })

      it('sorts by the options of the field when the folder has it as a select: options, other values, none; both ways, by cursor', async () => {
        const walkTitles = async (sort: string, limit: number) => titles(await walk({ folder, sort: sort as never }, limit))
        // draft: 乙 己 庚 (a list sorts by its first value); review; final; legacy; none.
        for (const limit of [1, 2, 20]) {
          expect(await walkTitles('prop:status', limit)).toBe('乙 己 庚 丙 甲 丁 戊')
          expect(await walkTitles('-prop:status', limit)).toBe('丁 甲 丙 庚 己 乙 戊')
        }
        // A field the type does not give as a select sorts by its text; so does a listing without the folder.
        expect(await walkTitles('prop:points', 20)).toBe('甲 乙 丙 丁 戊 己 庚')
        const noFolder = (await call(admin, 'GET', `${SP_PAGES}?${filter([{ key: 'status', op: 'in', values: options }])}&sort=prop:status&limit=50`)).json.items as PageSummary[]
        expect(titles(noFolder.filter((i) => i.folder === folder))).toBe('乙 己 庚 甲 丙')
        // A cursor of the order of the options is no cursor of another sort.
        const first = (await call(admin, 'GET', listUrl('sort=prop:status&limit=1'))).json
        const other = await call(admin, 'GET', listUrl(`sort=-prop:status&limit=1&cursor=${enc(first.next)}`))
        expect([other.status, other.json.error]).toEqual([400, 'invalid'])
      })

      it('an option reordered in the type changes the order at once', async () => {
        const t = await S.putType(typeName, { baseRev: typeRev, title: '文案', fields: [{ key: 'status', options: ['final', 'review', 'draft'] }, { key: 'points' }] })
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
      for (let i = 0; i < 22; i++) await mk(`${folder}/long`, { title: `长${String(i).padStart(2, '0')}` }, `第 ${i} 篇\n`)
      const first = (await call(admin, 'GET', `${SP_PAGES}?folder=${enc(`${folder}/long`)}&sort=title&body=1`)).json
      expect(first.items).toHaveLength(20)
      const rest = (await call(admin, 'GET', `${SP_PAGES}?folder=${enc(`${folder}/long`)}&sort=title&body=1&cursor=${enc(first.next)}`)).json
      expect(rest.items.map((i: any) => i.body)).toEqual(['第 20 篇\n', '第 21 篇\n'])
      expect(rest.next).toBe('')
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
      const src = await mk(`${f}/a`, { title: `出链${run}` }, `先看 ${linkTo(`${f}/a`, target, '甲')}，再看[无](没有这一篇.md)，还有[外](https://example.com/x.md)。\n`)
      const r = await S.links(src.id)
      expect(r.items.map((l) => l.text)).toEqual(['甲', '无'])
      expect(r.items[0]).toMatchObject({ text: '甲', id: target.id, path: target.path, broken: false })
      expect(r.items[0].target).toMatchObject({ id: target.id, path: target.path, title: `被链到${run}`, untitled: false })
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
      await S.savePage(target.id, { baseRev: target.rev, meta: { ...target.meta, title: `新名${run}` }, body: target.body })
      const l = (await S.links(src.id)).items[0]
      expect(l.broken).toBe(false)
      expect(l.target?.title).toBe(`新名${run}`)
    })

    it('says which pages link here, with the paragraph and the total, never the page itself', async () => {
      const made = await mk(`${f}/back`, { title: `被引用${run}` }, 'x\n')
      const target = await S.savePage(made.id, { baseRev: made.rev, meta: made.meta, body: `自己链自己：${linkTo(`${f}/back`, made, '我')}\n` })
      const one = await mk(`${f}/back/甲`, { title: `引用甲${run}` }, `前一段。\n\n这一段链到${linkTo(`${f}/back/甲`, target, '目标')}，写了两次${linkTo(`${f}/back/甲`, target, '又一次')}。\n\n后一段。\n`)
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
      for (let i = 0; i < 22; i++) made.push((await mk(`${f}/many`, { title: `引用${String(i).padStart(2, '0')}${run}` }, `见${linkTo(`${f}/many`, target)}\n`)).id)
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

  describe('drafts', () => {
    const draftOf = (p: Page, over: { title?: string; body?: string; baseRev?: string } = {}) => ({
      baseRev: over.baseRev ?? p.rev,
      meta: { ...p.meta, ...(over.title === undefined ? {} : { title: over.title }) },
      body: over.body ?? p.body,
    })
    // Store a draft, replacing version `prev` ("" when there is none).
    const store = (p: Page, over: Parameters<typeof draftOf>[1] = {}, prev = '') => S.putDraft(p.id, { ...draftOf(p, over), prev })

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
      expect(got.draft).toEqual({ version: receipt.version, baseRev: p.rev, meta: { ...p.meta, title: `草稿新题${run}` }, body: `仅在草稿里的${run}\n`, updated: receipt.updated, stale: false })
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
      const noPrev = await fetch(`${SP_PAGES}/${q.id}/draft`, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(draftOf(q, { body: 'x\n' })) })
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
      for (const payload of [{ baseRev: p.rev, meta: p.meta }, { baseRev: p.rev, body: 'y\n' }, { meta: p.meta, body: 'y\n' }, {}]) {
        const r = await fetch(url, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(payload) })
        expect(r.status, JSON.stringify(payload)).toBe(400)
        expect(((await r.json()) as { error: string }).error).toBe('invalid')
      }
      const mismatch = await failure(S.putDraft(p.id, { baseRev: p.rev, meta: { ...p.meta, id: '01AAAAAAAA' }, body: 'y\n', prev: '' }))
      expect([mismatch.status, mismatch.code]).toEqual([400, 'invalid'])
      const missing = await failure(S.putDraft('01ZZZZZZZZ', { baseRev: p.rev, meta: {}, body: '', prev: '' }))
      expect([missing.status, missing.code]).toEqual([404, 'not_found'])
      const plain = await fetch(url, { method: 'PUT', headers: { 'Content-Type': 'text/plain' }, body: JSON.stringify({ ...draftOf(p), prev: '' }) })
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
      expect(Object.keys(committed.meta).filter((k) => k !== 'id' && k !== 'created')).toEqual(Object.keys(saved.meta).filter((k) => k !== 'id' && k !== 'created'))
      expect(committed.body).toBe(saved.body)
      const got = await S.getPage(a.id)
      expect(got).toEqual(committed)
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
      expect(again.current).toEqual(done)
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
      expect(e.current).toEqual(done)
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
      const again = await S.putDraft(p.id, { baseRev: moved.rev, meta: moved.meta, body: '我的草稿\n', prev: stale.version })
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

    it.skipIf(!!REAL)('mock only: the server commits a draft that has been quiet for 2 minutes, but not before; a stale one is left alone; then the old version is refused', async () => {
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
    })

    it.skipIf(!!REAL)('mock only: the server removes a stale draft that says what the file says (a leftover), and leaves a stale one that differs', async () => {
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
    })

    it.skipIf(!!REAL)('mock only: a draft stored again and again is committed 10 minutes after it was first stored', async () => {
      const p = await mk(`${root}/dr17`, { title: `十分钟${run}` }, 'x\n')
      let prev = ''
      for (let i = 0; i < 6; i++) {
        prev = (await store(p, { body: `v${i}\n` }, prev)).version
        clock += 100_000
      }
      // Never quiet for 2 minutes, yet 10 have passed.
      expect((await S.getPage(p.id)).body).toBe('v5\n')
    })
  })

  describe('files', () => {
    const enc = encodeURIComponent
    const hash8 = (data: Uint8Array) => sha256Hex(data).slice(0, 8)
    const bytesOf = (s: string) => new TextEncoder().encode(s)
    // Built by Node's own Response, not by jsdom: a real `fetch` only knows how to send its own kind of Blob.
    const blobOf = (data: Uint8Array) => new Response(data as BodyInit).blob()
    const post = (pageId: string, name: string | null, data: Uint8Array, type = 'application/octet-stream') =>
      admin.fetch(`/api/spaces/${spaceId}/pages/${pageId}/assets${name === null ? '' : `?name=${enc(name)}`}`, { method: 'POST', headers: { 'Content-Type': type }, body: data as BodyInit })
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
      expect(a).toEqual({ path: `/assets/${page.id}/合同${run}-${hash8(data)}.pdf`, name: `合同${run}-${hash8(data)}.pdf`, size: data.length })
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

    it('a file over maxUpload is 413 invalid', async () => {
      const { maxUpload } = await api.meta()
      // A server started with a huge limit would be sent too much to prove this.
      if (maxUpload > 64 * 1024 * 1024) return
      const r = await post(page.id, `big${run}.bin`, new Uint8Array(maxUpload + 1))
      expect([r.status, (await r.json()).error]).toEqual([413, 'invalid'])
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
      for (const path of [`/assets/${page.id}/missing${run}.png`, `/${page.path}`, '/.git/config', `/assets/.hidden${run}/x.png`, `/assets/${page.id}/.x${run}.png`]) {
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
      const report = await S.importMarkdown(await blobOf('# Imported heading\n\nFirst paragraph.\n'), { folder, name: 'notes.md' })
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
      await S.importMarkdown(await blobOf('---\nid: not-my-id\ntitle: From the header\nauthor: Ada\n---\n\nBody text.\n'), { folder, name: 'h.md' })
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
    const eventually = async <T,>(fn: () => Promise<T>, done: (v: T) => boolean, ms = 25_000): Promise<T> => {
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
      const items = (await eventually(() => allHistory(p.id, 50), (l) => l[0]?.rev === saved.rev)) ?? []
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
      for (let i = 1; i < items.length; i++) expect(Date.parse(items[i - 1].time)).toBeGreaterThanOrEqual(Date.parse(items[i].time))
      expect(new Set(items.map((i) => i.commit)).size).toBe(items.length)
      expect((await S.history(p.id)).items.length).toBe(items.length)
    })

    it('a version is the page as it was: header and body, with the rev of that file', async () => {
      const { p, saved, items } = await twice('h2', `版本内容${run}`)
      const newest = await S.version(p.id, items[0].commit)
      expect(newest).toMatchObject({ commit: items[0].commit, rev: saved.rev, path: saved.path, author: items[0].author })
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
      const before = await eventually(() => allHistory(p.id, 50), (l) => l.length >= 1)
      const r = await S.putDraft(p.id, { prev: '', baseRev: p.rev, meta: p.meta, body: '乙\n' })
      expect((await allHistory(p.id, 50)).map((i) => i.commit)).toEqual(before.map((i) => i.commit))
      const committed = await S.commitPage(p.id, r.version)
      const after = await eventually(() => allHistory(p.id, 50), (l) => l[0]?.rev === committed.rev)
      expect(after[0].rev).toBe(committed.rev)
      expect((await S.version(p.id, after[0].commit)).body).toBe('乙\n')
    })

    it('renaming through the title keeps the history of the page, with the new path on the newest version', async () => {
      const p = await mk(`${root}/h7`, { title: `旧名${run}` }, '内容\n')
      const saved = await S.savePage(p.id, { baseRev: p.rev, meta: { ...p.meta, title: `新名${run}` }, body: '内容\n' })
      const items = await eventually(() => allHistory(p.id, 50), (l) => l[0]?.path === saved.path)
      expect(items[0].path).toBe(`${root}/h7/新名${run}.md`)
      expect(items[items.length - 1].change).toBe('add')
      expect(items.length).toBeGreaterThanOrEqual(1)
    })
  })

  const url = (host: string, tail = 'notes.git') => `https://${host}/${tail}`

  describe.skipIf(!accounts)('remote repository (accounts server only: a space is made for it; on a real server only what needs no remote is asserted)', () => {
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
      for (const bad of ['ftp://github.com/a/b.git', 'github.com/a/b.git', 'https://user:secret@github.com/a/b.git', 'https://github.com', '', 'git@github.com', 'file:///tmp/x']) {
        const e = await failure(R.connectRemote({ url: bad, credential: cred }))
        expect([e.status, e.code], bad).toEqual([400, 'invalid'])
      }
      // Still local.
      expect((await failure(R.getRemote())).status).toBe(404)
    })

    it.skipIf(HOSTS_OPEN)('a host the administrator did not allow is 403 forbidden, and is not taken for a refused origin', async () => {
      const e = await failure(R.connectRemote({ url: url('git.not-allowed.example'), credential: { type: 'token', token: 'x' } }))
      expect([e.status, e.code]).toEqual([403, 'forbidden'])
      expect(e.message).not.toMatch(/allow-origin/)
      const ssh = await failure(R.connectRemote({ url: 'git@git.not-allowed.example:a/b.git', credential: { type: 'ssh' } }))
      expect([ssh.status, ssh.code]).toEqual([403, 'forbidden'])
    })
  })

  describe.skipIf(!accounts || !!REAL)('mock only (a real server has no remote to talk to): connecting, states and unavailable writes', () => {
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
    const titles = async (R: SpaceApi) => (await R.listPages({ recursive: true, folder: '' })).items.map((i) => i.title)

    it('connecting to a remote with no such branch pushes the space: 200 with the remote, state ok, a sync time, and the token never comes back', async () => {
      const { id, R } = await fresh('推送')
      const r = await R.connectRemote({ url: url('github.com'), credential: token })
      expect(r).toMatchObject({ url: url('github.com'), branch: 'main', state: 'ok', error: '', rewritten: null, credential: { type: 'token', username: 'u' } })
      expect(r.lastSync).toMatch(/^\d{4}-\d{2}-\d{2}T/)
      expect(JSON.stringify(r)).not.toContain('secret-token-1')
      expect(await R.getRemote()).toEqual(r)
      expect((await list(id)).sync).toBe('ok')
    })

    it('the branch defaults to main and can be named', async () => {
      const { R } = await fresh('分支')
      expect((await R.connectRemote({ url: url('github.com'), branch: 'notes', credential: token })).branch).toBe('notes')
      expect((await R.connectRemote({ url: url('gitlab.com'), credential: token })).branch).toBe('main')
    })

    it('a pristine space takes the remote\'s content', async () => {
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
      const none = await failure(R.connectRemote({ url: url('github.com'), credential: { type: 'token', username: 'u' } }))
      expect([none.status, none.code]).toEqual([400, 'invalid'])
      const first = await R.connectRemote({ url: url('github.com'), credential: { type: 'token', token: 'tok-1' } })
      expect(first.credential).toEqual({ type: 'token', username: 'x-access-token' })
      expect(server!.storedToken(spaceIdOf(R))).toBe('tok-1')
      const again = await R.connectRemote({ url: url('github.com', 'other.git'), credential: { type: 'token', username: 'me' } })
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
      for (const [u, c] of [[url('github.com'), { type: 'ssh' }], ['git@github.com:me/x.git', token]] as const) {
        const e = await failure(R.connectRemote({ url: u, credential: c }))
        expect([e.status, e.code]).toEqual([400, 'invalid'])
      }
      const e = await failure(R.remoteKey(true))
      expect([e.status, e.code]).toEqual([409, 'conflict'])
      expect((await R.connectRemote({ url: 'ssh://git@gitlab.com:2222/me/notes.git', credential: { type: 'ssh' } })).state).toBe('ok')
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
      expect(r.rewritten).toEqual({ lastGood: expect.stringMatching(/^[0-9a-f]{40}$/), remote: expect.stringMatching(/^[0-9a-f]{40}$/) })
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
      const open = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: null, remote: { hosts: ['*'] } })
      const b = newBrowser(mockFetch(open, { jar: false }))
      await as(b, async () => {
        await api.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
        const sp = await api.createSpace('任意主机')
        expect((await spaceApi(sp.id).connectRemote({ url: url('git.anywhere.example'), credential: token })).state).toBe('ok')
      })
    })

    it('the mock can start a space in a state (for the app to be seen by hand)', async () => {
      const m = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: null, remote: { start: 'offline' } })
      const b = newBrowser(mockFetch(m, { jar: false }))
      await as(b, async () => {
        await api.login({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password })
        const first = (await api.spaces()).spaces[0]
        expect(first.sync).toBe('offline')
        expect(await spaceApi(first.id).getRemote()).toMatchObject({ state: 'offline' })
      })
    })
  })

  describe.skipIf(!accounts || !!REAL)('mock only (skipped on a real server: no provider, no clean first run, and the rate limiter would lock us out): single sign-on, setup, rate limits', () => {
    let sso: MockServer
    let ssoFetch: typeof fetch
    const ssoBrowser = () => newBrowser(ssoFetch)
    const start = async (b: Browser, body: Record<string, unknown>) => (await call(b, 'POST', '/api/auth/oidc/start', body)).json as { url: string }
    // What the provider's redirect does: the browser follows the url.
    const callback = (b: Browser, url: string) => call(b, 'GET', url)

    beforeAll(() => {
      sso = createMockServer({ empty: true, mode: 'accounts', admin: false, now: () => clock, oidc: { name: 'Company SSO', sub: 'sub-1' }, setupToken: 'tok' })
      ssoFetch = mockFetch(sso, { jar: false })
    })

    it('without a provider, start is 404', async () => {
      const r = await call(admin, 'POST', '/api/auth/oidc/start', { intent: 'login' })
      expect([r.status, r.json.error]).toEqual([404, 'not_found'])
    })

    it('session is `setup: true` while there is no user, and names the provider', async () => {
      const s = (await call(ssoBrowser(), 'GET', '/api/session')).json
      expect(s).toMatchObject({ mode: 'accounts', user: null, setup: true, methods: { password: true, oidc: { name: 'Company SSO' } } })
    })

    it('setup: a wrong token is 403 forbidden, a bad username or password 400; then 201 with a session and a personal space; after that 409 exists', async () => {
      const b = ssoBrowser()
      const wrong = await call(b, 'POST', '/api/setup', { token: 'nope', username: 'boss', password: 'a-long-password' })
      expect([wrong.status, wrong.json.error]).toEqual([403, 'forbidden'])
      expect((await call(b, 'POST', '/api/setup', { token: 'tok', username: 'B', password: 'a-long-password' })).status).toBe(400)
      expect((await call(b, 'POST', '/api/setup', { token: 'tok', username: 'boss', password: 'short' })).status).toBe(400)
      const ok = await call(b, 'POST', '/api/setup', { token: 'tok', username: 'Boss', password: 'a-long-password' })
      expect(ok.status).toBe(201)
      expect(ok.json.user).toMatchObject({ username: 'boss', name: 'boss', role: 'admin' })
      expect(((await call(b, 'GET', '/api/spaces')).json.spaces as SpaceInfo[])[0].personal).toBe(true)
      expect((await call(ssoBrowser(), 'GET', '/api/session')).json.setup).toBe(false)
      const again = await call(ssoBrowser(), 'POST', '/api/setup', { token: 'tok', username: 'second', password: 'a-long-password' })
      expect([again.status, again.json.error]).toEqual([409, 'exists'])
    })

    it('login through the provider: an identity nobody has is redirected to /login?error=no_account; a linked one signs in and goes to the return path', async () => {
      const b = ssoBrowser()
      const first = await start(b, { intent: 'login', return: '/s/abc' })
      const denied = await callback(b, first.url)
      expect([denied.status, denied.headers.get('Location')]).toEqual([303, '/login?error=no_account'])

      // Link it from a signed-in account, then sign in with it.
      const me = ssoBrowser()
      expect((await call(me, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })).status).toBe(200)
      const link = await start(me, { intent: 'link', return: '/settings' })
      const linked = await callback(me, link.url)
      expect([linked.status, linked.headers.get('Location')]).toEqual([303, '/settings'])
      const acct = (await call(me, 'GET', '/api/account')).json
      expect(acct.identities).toEqual([expect.objectContaining({ provider: 'Company SSO', label: expect.any(String) })])

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

    it('an attempt is used once, belongs to the browser that started it, and expires after 10 minutes: /login?error=expired', async () => {
      const b = ssoBrowser()
      const s = await start(b, { intent: 'login' })
      expect((await callback(b, s.url)).headers.get('Location')).toBe('/')
      expect((await callback(b, s.url)).headers.get('Location')).toBe('/login?error=expired')
      const other = ssoBrowser()
      const s2 = await start(b, { intent: 'login' })
      expect((await callback(other, s2.url)).headers.get('Location')).toBe('/login?error=expired')
      const s3 = await start(b, { intent: 'login' })
      clock += 11 * 60_000
      expect((await callback(b, s3.url)).headers.get('Location')).toBe('/login?error=expired')
    })

    it('the provider refusing is idp_error; link failures go to /settings?error=', async () => {
      const b = ssoBrowser()
      const s = await start(b, { intent: 'login' })
      expect((await callback(b, `${s.url}&error=access_denied`)).headers.get('Location')).toBe('/login?error=idp_error')
      const me = ssoBrowser()
      await call(me, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })
      const l = await start(me, { intent: 'link' })
      expect((await callback(me, `${l.url}&error=access_denied`)).headers.get('Location')).toBe('/settings?error=idp_error')
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
      expect((await callback(b, s.url)).headers.get('Location')).toBe('/login?error=identity_in_use')
      const bad = await start(ssoBrowser(), { intent: 'invite', token: 'nope' })
      expect(bad.url).toContain('/api/auth/oidc/callback')
      const b2 = ssoBrowser()
      const s2 = await start(b2, { intent: 'invite', token: 'nope' })
      expect((await callback(b2, s2.url)).headers.get('Location')).toBe('/login?error=invite_invalid')
      const b3 = ssoBrowser()
      const s3 = await start(b3, { intent: 'setup', token: 'tok' })
      expect((await callback(b3, s3.url)).headers.get('Location')).toBe('/login?error=setup_invalid')
    })

    it('a disabled user signing in through the provider is /login?error=disabled; the last identity of a user with no password cannot be removed (409)', async () => {
      const boss = ssoBrowser()
      await call(boss, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })
      const sso2 = createMockServer({ empty: true, mode: 'accounts', now: () => clock, oidc: { name: 'Company SSO', autoCreate: true, sub: 'sub-9' }, admin: { username: 'root', password: 'a-long-password' } })
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
      expect((await callback(again, s2.url)).headers.get('Location')).toBe('/login?error=disabled')
    })

    it('too many wrong passwords against one name is 429 rate_limited with Retry-After, even for the right password, until the wait is over', async () => {
      const b = ssoBrowser()
      for (let i = 0; i < 5; i++) expect((await call(b, 'POST', '/api/login', { username: 'boss', password: 'wrong-wrong' }, JSON_HEADERS, { expect429: true })).status).toBe(401)
      const r = await call(b, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' }, JSON_HEADERS, { expect429: true })
      expect([r.status, r.json.error]).toEqual([429, 'rate_limited'])
      expect(Number(r.headers.get('Retry-After'))).toBeGreaterThan(0)
      const e = await as(b, () => failure(api.login({ username: 'boss', password: 'a-long-password' })))
      expect(e.retryAfter).toBeGreaterThan(0)
      clock += 61_000
      expect((await call(b, 'POST', '/api/login', { username: 'boss', password: 'a-long-password' })).status).toBe(200)
    })
  })
}
