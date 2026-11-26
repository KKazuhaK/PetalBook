// The fixtures cannot drift from the server: every answer a fixture route gives is compared with the shape the real
// server's answers were recorded with (recorded/shapes.json, written by the contract suite). A key the server does
// not send, a key it sends that the fixture lacks, or another type, fails here. A route with no probe fails too, so
// a new route cannot skip the check. What a test writes by hand with `fx.reply` or `fx.fail` (an exact answer or a
// refusal) is not compared: it says what that test needs, whatever the server does.
// @ts-expect-error TS2591: Node's own module; the web app has no Node typings, and the test runs in Node.
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { misfits, routeOf, type Shape } from '../contractShapes'
import { match } from './kit'
import { createFixtureServer, type FixtureServer } from './server'

const recorded = JSON.parse(readFileSync('src/api/fixtures/recorded/shapes.json', 'utf8')) as Record<string, Shape>

// Where the recorded shape of a route may be: the real run used the space's own id or the word "default".
function shapeFor(method: string, path: string, status: number): Shape | undefined {
  const parts = routeOf(path).split('/')
  const last = parts.length - 1
  const tries = [
    parts,
    parts.map((p, i) => (i === 3 ? '{}' : p)),
    parts.map((p, i) => (i === last ? '{}' : p)),
    parts.map((p, i) => (i === 3 || i === last ? '{}' : p)),
  ]
  for (const t of tries) {
    const shape = recorded[`${method} ${t.join('/')} ${status}`]
    if (shape !== undefined) return shape
  }
  return undefined
}

// A map by name or id (what a run made up) is recorded without its keys: compare nothing inside it.
function pruned(shape: Shape, v: unknown): [Shape, unknown] {
  if (Array.isArray(shape)) {
    if (!Array.isArray(v) || !shape.length) return [shape, v]
    const out = v.map((e) => pruned(shape[0], e))
    return [[out[0]?.[0] ?? shape[0]], out.map((o) => o[1])]
  }
  if (typeof shape === 'object' && v !== null && typeof v === 'object' && !Array.isArray(v) && !('$or' in shape)) {
    if (Object.keys(shape).some((k) => k.includes('{run}') || k.includes('{id}'))) return [{}, {}]
    const s: Record<string, Shape> = {}
    const o: Record<string, unknown> = {}
    for (const [k, sub] of Object.entries(shape)) {
      const name = k.endsWith('?') ? k.slice(0, -1) : k
      if (!(name in v)) {
        s[k] = sub
        continue
      }
      const value = (v as Record<string, unknown>)[name]
      // A page's header and its properties are whatever the person wrote: every run has other keys.
      const open = (name === 'meta' || name === 'props') && typeof value === 'object' && value !== null
      const [ps, pv] = open ? [{}, {}] : pruned(sub, value)
      s[k] = ps
      o[name] = pv
    }
    for (const k of Object.keys(v)) if (!(k in o)) o[k] = (v as Record<string, unknown>)[k]
    return [s, o]
  }
  return [shape, v]
}

// The body is made when the call is, since an earlier call changes the rev.
type Call = [method: string, path: string, body?: unknown | (() => unknown)]

// Every fixture route, called the way a page calls it, on data that has something in it.
function probes(fx: FixtureServer): Call[] {
  const space = fx.data.spaces[0]
  const page = space.pages.find((p) => p.folder === '工作') ?? space.pages[0]
  const other = space.pages.find((p) => p.id !== page.id && p.folder === page.folder) ?? space.pages[1]
  const P = `/api/spaces/default/pages/${page.id}`
  const rev = () => fx.call('GET', P).body.rev as string
  const meta = () => fx.call('GET', P).body.meta as Record<string, unknown>
  return [
    ['GET', '/api/session'],
    ['GET', '/api/meta'],
    ['GET', '/api/settings'],
    ['GET', '/api/notifications/unread'],
    ['GET', '/api/prefs'],
    ['GET', '/api/spaces/default/daily'],
    ['GET', '/api/spaces/default/checks'],
    ['GET', '/api/auth/providers'],
    ['GET', '/api/captcha'],
    ['GET', '/api/account'],
    ['GET', '/api/spaces'],
    ['POST', '/api/spaces', { name: '另一个空间' }],
    ['PATCH', '/api/spaces/default', { name: '我的笔记' }],
    // pages
    ['GET', '/api/spaces/default/pages'],
    ['GET', '/api/spaces/default/pages?folder=%E5%B7%A5%E4%BD%9C&props=1'],
    ['GET', '/api/spaces/default/pages?body=1&limit=3&sort=title'],
    ['GET', '/api/spaces/default/pages/groups?key=status&folder=%E5%B7%A5%E4%BD%9C'],
    ['POST', '/api/spaces/default/pages', { folder: '工作', meta: { title: '新的一页', tags: ['a'] }, body: 'x\n' }],
    ['GET', P],
    ['PUT', P, () => ({ baseRev: rev(), meta: meta(), body: '改过的正文\n' })],
    ['PATCH', `${P}/props`, () => ({ baseRev: rev(), set: { status: 'doing' } })],
    ['POST', `${P}/move`, { folder: '工作' }],
    ['PUT', `${P}/draft`, () => ({ prev: '', baseRev: rev(), meta: meta(), body: '草稿\n' })],
    ['GET', P],
    ['POST', `${P}/commit`, () => ({ version: fx.data.spaces[0].drafts[page.id]?.version })],
    ['PUT', `${P}/draft`, () => ({ prev: '', baseRev: rev(), meta: meta(), body: '又一份草稿\n' })],
    ['DELETE', `${P}/draft`],
    ['POST', `${P}/assets?name=a.png`],
    ['POST', '/api/spaces/default/pages/batch', { op: 'move', ids: [other.id], folder: '工作' }],
    // folders, tags, appearance
    ['GET', '/api/spaces/default/tree'],
    ['POST', '/api/spaces/default/folders', { path: '新文件夹' }],
    ['POST', '/api/spaces/default/folders/move', { from: '新文件夹', to: '改名文件夹' }],
    ['PUT', '/api/spaces/default/folders/type', { folder: '工作', type: 'task' }],
    ['PUT', '/api/spaces/default/folders/style', { folder: '工作', icon: 'flower', color: 'pink' }],
    ['DELETE', '/api/spaces/default/folders?path=%E6%94%B9%E5%90%8D%E6%96%87%E4%BB%B6%E5%A4%B9'],
    ['GET', '/api/spaces/default/appearance'],
    ['GET', '/api/spaces/default/sources'],
    ['PUT', '/api/spaces/default/sources/presets', { presets: ['书'] }],
    ['GET', '/api/spaces/default/tags'],
    ['PUT', '/api/spaces/default/tags/%E9%9A%8F%E7%AC%94', { color: 'rose' }],
    // types, templates, favourites, views
    ['GET', '/api/spaces/default/types'],
    [
      'PUT',
      '/api/spaces/default/types/task',
      { title: '任务', fields: [{ key: 'status', kind: 'select', options: ['todo', 'done'] }] },
    ],
    ['DELETE', '/api/spaces/default/types/memo'],
    ['GET', '/api/spaces/default/templates'],
    [
      'PUT',
      '/api/spaces/default/templates/%E6%97%A5%E8%AE%B0',
      { baseRev: '', meta: { title: '日记' }, body: '今天\n' },
    ],
    ['PUT', '/api/spaces/default/templates/defaults', { folder: '工作', template: '日记' }],
    ['DELETE', '/api/spaces/default/templates/%E6%97%A5%E8%AE%B0?baseRev=x'],
    ['GET', '/api/spaces/default/favorites'],
    ['POST', '/api/spaces/default/favorites', { kind: 'page', target: page.id }],
    ['PUT', '/api/spaces/default/favorites/order', { items: [{ kind: 'page', target: page.id }] }],
    ['DELETE', `/api/spaces/default/favorites?kind=page&target=${page.id}`],
    ['GET', '/api/spaces/default/views?folder=%E5%B7%A5%E4%BD%9C'],
    [
      'PUT',
      '/api/spaces/default/views',
      () => ({
        folder: '工作',
        baseRev: fx.call('GET', '/api/spaces/default/views?folder=%E5%B7%A5%E4%BD%9C').body.rev,
        views: [{ name: '全部', layout: 'list' }],
      }),
    ],
    // the offline feed
    ['GET', '/api/sync/heads?space=default'],
    ['GET', '/api/spaces/default/sync/catalog'],
    ['GET', `/api/spaces/default/sync/pages?ids=${page.id}`],
    [
      'GET',
      `/api/spaces/default/changes?since=${fx.call('GET', '/api/spaces/default/sync/catalog?limit=1').body.changes}`,
    ],
    ['POST', '/api/spaces/default/reindex'],
    // signing in
    ['POST', '/api/login', { username: 'admin', password: 'x' }],
    ['POST', '/api/logout'],
    ['POST', '/api/setup', { token: 't', username: 'first', password: 'a-long-password' }],
    ['POST', '/api/invites/check', { token: 'inv' }],
    ['POST', '/api/invites/accept', { token: 'inv', username: 'dave', password: 'a-long-password' }],
    ['POST', '/api/auth/oidc/company/start', { intent: 'login' }],
    ['POST', '/api/auth/oidc/start', { intent: 'login' }],
    ['POST', '/api/auth/passkey/begin', {}],
    ['POST', '/api/auth/passkey/finish?ceremony=c&return=%2F', { id: 'cred' }],
    ['POST', '/api/account/passkeys/begin', { name: 'Key' }],
    ['POST', '/api/account/passkeys/finish?ceremony=c', { id: 'cred' }],
    ['GET', '/api/register'],
    ['POST', '/api/register', { username: 'tang', email: 'tang@example.test', password: 'a-long-password' }],
    ['POST', '/api/register/confirm', { token: 'tok' }],
    ['POST', '/api/password/forgot', { login: 'admin' }],
    ['POST', '/api/password/reset/check', { token: 'tok' }],
    ['POST', '/api/password/reset', { token: 'tok', password: 'a-long-password' }],
    ['POST', '/api/email/confirm', { token: 'tok' }],
    ['GET', '/api/oauth/consent?req=r1'],
    ['POST', '/api/oauth/consent', { req: 'r1', approve: true }],
    ['DELETE', P],
    ['DELETE', '/api/spaces/default/pages/NOPE'],
  ]
}

// What the real server's answer is for a call that the contract suite cannot make (it needs a provider, a mail
// server or an authorization client): nothing is recorded, so nothing is compared. Each says why.
const UNRECORDED: Record<string, string> = {
  'GET /api/captcha 200': 'the server answers it only when a captcha is on',
  'POST /api/auth/oidc/company/start 200': 'needs a provider',
  'POST /api/auth/oidc/{}/start 200': 'needs a provider',
  'POST /api/auth/oidc/start 200': 'needs a provider',
  'POST /api/auth/passkey/begin 200': 'needs a passkey',
  'POST /api/auth/passkey/finish 200': 'needs a passkey',
  'POST /api/account/passkeys/begin 200': 'needs a passkey',
  'POST /api/account/passkeys/finish 200': 'needs a passkey',
  'POST /api/register 202': 'needs mail',
  'POST /api/register/confirm 200': 'needs mail',
  'POST /api/password/forgot 202': 'needs mail',
  'POST /api/password/reset/check 200': 'needs mail',
  'GET /api/oauth/consent 200': 'needs an authorization client',
  'POST /api/oauth/consent 200': 'needs an authorization client',
  'POST /api/setup 200': 'needs a server with no account',
}

// A probe is meant to succeed. One that answers 404 is a route that is missing or a probe that is wrong, unless it
// is here with the reason the 404 is right.
const NOT_FOUND: Record<string, string> = {
  'DELETE /api/spaces/default/pages/NOPE': 'a page that does not exist',
}

describe('the fixtures against the recorded shapes', () => {
  const fx = createFixtureServer({
    mode: 'accounts',
    signedIn: true,
    data: (d) => {
      d.auth.invite = { token: 'inv', role: 'member' }
      d.auth.consents.r1 = {
        client: { name: 'c', id: 'https://c.example/', host: 'c.example' },
        redirectHost: 'c.example',
        punycode: false,
        scopes: ['read'],
        spaces: [],
        user: { id: 'u', name: 'n' },
      }
      d.spaces[0].types.push({ name: 'memo', title: '备忘', rev: 'r', fields: [] })
    },
  })
  const calls = probes(fx)

  it('every answer has the shape the server was recorded to give', () => {
    const problems: string[] = []
    for (const [method, path, body] of calls) {
      const made = typeof body === 'function' ? (body as () => unknown)() : body
      const r = fx.handle(method, path, made === undefined ? undefined : JSON.stringify(made))
      if (r.status === 404 && `${method} ${path}` in NOT_FOUND) continue
      if (r.status >= 400) {
        problems.push(`${method} ${path}: refused with ${r.status}`)
        continue
      }
      if (r.body === undefined) continue
      const key = `${method} ${routeOf(path)} ${r.status}`
      const shape = shapeFor(method, path, r.status)
      if (shape === undefined) {
        if (!(key in UNRECORDED)) problems.push(`${key}: no recorded shape`)
        continue
      }
      if (key in UNRECORDED) problems.push(`${key}: is recorded now; take it off the unrecorded list`)
      const [s, v] = pruned(shape, JSON.parse(JSON.stringify(r.body)))
      for (const m of misfits(v, s)) problems.push(`${key}: ${m}`)
    }
    expect(problems).toEqual([])
  })

  it('has a probe for every route', () => {
    const unprobed = fx.routes
      .filter(([m, pattern]) => !calls.some(([cm, path]) => cm === m && match(pattern, path.split('?')[0])))
      .map(([m, pattern]) => `${m} ${pattern}`)
    expect(unprobed).toEqual([])
  })
})
