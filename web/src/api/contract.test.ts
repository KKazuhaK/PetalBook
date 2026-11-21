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
// It creates all its own data (under names made unique per run) and never
// assumes anything else is in the space. Where the mock and the server
// differ, the mock is wrong.

import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { ApiError, api, markSignedIn, spaceApi, type SpaceApi } from './client'
import { mockFetch } from './mock/fetch'
import { createMockServer, MOCK_ADMIN, type MockServer } from './mock/server'
import type { Page, PageSummary, SpaceInfo, User } from './types'

declare const process: { env: Record<string, string | undefined> }
const REAL = process.env.PETALBOOK_API?.replace(/\/+$/, '')
const CONTRACT_USER = process.env.PETALBOOK_CONTRACT_USER
const CONTRACT_PASSWORD = process.env.PETALBOOK_CONTRACT_PASSWORD
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
    for (const id of created.users) await api.updateUser(id, { disabled: true }).catch(() => undefined)
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
      ]
      for (const [method, path, body] of probes) {
        const r = await call(admin, method, path, body ?? (method === 'GET' ? undefined : {}))
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
      const invite = await as(admin, () => api.createInvite({ role: 'member', days: 1 }))
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
      it('an administrator lists users, ordered by created; a member gets 403 forbidden for users and invites', async () => {
        const m = await newMember(uniq('us'))
        const { users } = await as(admin, () => api.users())
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
        const promoted = await as(admin, () => api.updateUser(m.user.id, { role: 'admin' }))
        expect(promoted.user.role).toBe('admin')
        expect((await call(m.browser, 'GET', '/api/users')).status).toBe(200)
        await as(admin, () => api.updateUser(m.user.id, { role: 'member' }))
        expect((await call(m.browser, 'GET', '/api/users')).status).toBe(403)

        const off = await as(admin, () => api.updateUser(m.user.id, { disabled: true }))
        expect(off.user.disabled).toBe(true)
        expect((await call(m.browser, 'GET', '/api/account')).status).toBe(401)
        const again = await call(browser(), 'POST', '/api/login', { username: m.user.username, password: m.password })
        expect([again.status, again.json.error]).toEqual([401, 'unauthenticated'])
        await as(admin, () => api.updateUser(m.user.id, { disabled: false }))
        expect((await call(browser(), 'POST', '/api/login', { username: m.user.username, password: m.password })).status).toBe(200)
      })

      it('an administrator cannot disable themself (409), nor leave the server with no enabled administrator (409)', async () => {
        const me = (await call(admin, 'GET', '/api/session')).json.user as User
        const self = await call(admin, 'PATCH', `/api/users/${me.id}`, { disabled: true })
        expect([self.status, self.json.error]).toEqual([409, 'conflict'])
        const { users } = await as(admin, () => api.users())
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
        const inv = await as(admin, () => api.createInvite({ role: 'member', days: 3 }))
        expect(inv.token.length).toBeGreaterThanOrEqual(16)
        expect(inv).toMatchObject({ role: 'member' })
        expect(Date.parse(inv.expires)).toBeGreaterThan(Date.parse(inv.created))
        const { invites } = await as(admin, () => api.invites())
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
        const inv = await as(admin, () => api.createInvite({ role: 'member', days: 1 }))
        const anon = browser()
        const ok = await call(anon, 'POST', '/api/invites/check', { token: inv.token })
        expect(ok.status).toBe(200)
        expect(ok.json).toEqual({ role: 'member', expires: inv.expires })
        const bad = await call(anon, 'POST', '/api/invites/check', { token: 'not-a-token' })
        expect([bad.status, bad.json.error]).toEqual([404, 'not_found'])
      })

      it('accept is public: 201 with the user and a session, a personal space of their own; the invite works once', async () => {
        const inv = await as(admin, () => api.createInvite({ role: 'member', days: 1 }))
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
        expect((await as(admin, () => api.invites())).invites.map((i) => i.id)).not.toContain(inv.id)
      })

      it('a taken username is 409 exists and the invite stays usable; a short password is 400', async () => {
        const m = await newMember(uniq('tk'))
        const inv = await as(admin, () => api.createInvite({ role: 'member', days: 1 }))
        const taken = await call(browser(), 'POST', '/api/invites/accept', { token: inv.token, username: m.user.username, password: 'a-long-password' })
        expect([taken.status, taken.json.error]).toEqual([409, 'exists'])
        const short = await call(browser(), 'POST', '/api/invites/accept', { token: inv.token, username: uniq('sh'), password: 'short' })
        expect([short.status, short.json.error]).toEqual([400, 'invalid'])
        expect((await call(browser(), 'POST', '/api/invites/check', { token: inv.token })).status).toBe(200)
      })

      it('revoke is 204 and the token stops working', async () => {
        const inv = await as(admin, () => api.createInvite({}))
        await as(admin, () => api.revokeInvite(inv.id))
        expect((await call(browser(), 'POST', '/api/invites/check', { token: inv.token })).status).toBe(404)
        expect((await as(admin, () => api.invites())).invites.map((i) => i.id)).not.toContain(inv.id)
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

      it('a space of someone else is 404 not_found, the same as one that does not exist: pages, folders, drafts, rename, and for an administrator too', async () => {
        const m = await newMember(uniq('is'))
        const mine = ((await call(m.browser, 'GET', '/api/spaces')).json.spaces as SpaceInfo[])[0]
        const own = await call(m.browser, 'POST', `/api/spaces/${mine.id}/pages`, { meta: { title: '只有我能看' }, body: '秘密\n' })
        expect(own.status).toBe(201)
        const mineSpace = `/api/spaces/${mine.id}`
        const theirs = `/api/spaces/${spaceId}`
        const absent = await call(admin, 'GET', `/api/spaces/01ZZZZZZZZ/pages`)
        expect([absent.status, absent.json.error]).toEqual([404, 'not_found'])
        for (const [b, method, path, body] of [
          [admin, 'GET', `${mineSpace}/pages`, undefined],
          [admin, 'GET', `${mineSpace}/pages/${own.json.id}`, undefined],
          [admin, 'PUT', `${mineSpace}/pages/${own.json.id}/draft`, { baseRev: own.json.rev, meta: own.json.meta, body: 'x\n' }],
          [admin, 'POST', `${mineSpace}/pages/${own.json.id}/commit`, {}],
          [admin, 'GET', `${mineSpace}/tree`, undefined],
          [admin, 'POST', `${mineSpace}/folders`, { path: 'x' }],
          [admin, 'PATCH', mineSpace, { name: '抢走' }],
          [m.browser, 'GET', `${theirs}/pages`, undefined],
          [m.browser, 'POST', `${theirs}/pages`, { body: 'x' }],
          [m.browser, 'PATCH', theirs, { name: '抢走' }],
        ] as [Browser, string, string, unknown][]) {
          const r = await call(b, method, path, body ?? (method === 'GET' ? undefined : {}))
          expect([r.status, r.json?.error], `${method} ${path}`).toEqual([404, 'not_found'])
        }
        // Nothing of theirs shows in their lists, either way.
        expect((await walk({ q: '只有我能看', recursive: true, folder: '' })).length).toBe(0)
      })
    })
  })

  describe('create and read', () => {
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
