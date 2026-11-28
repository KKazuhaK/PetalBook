// The fixtures cannot drift from the server: every answer a fixture route gives is compared with the shape the real
// server's answers were recorded with (recorded/shapes.json, written by the contract suite). A key the server does
// not send, a key it sends that the fixture lacks, or another type, fails here. A route with no probe fails too, so
// a new route cannot skip the check. What a test writes by hand with `fx.reply` or `fx.fail` (an exact answer or a
// refusal) is not compared: it says what that test needs, whatever the server does.
// @ts-expect-error TS2591: Node's own module; the web app has no Node typings, and the test runs in Node.
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { misfits, routeOf, type Shape } from '../contractShapes'
import { addNotification, addToken, approveApp, withAuthorizationServer } from './data/area'
import { githubInstallations, githubProvider, leaveRepository } from './data/connections'
import { addGroup, addPerson } from './data/scenes'
import { match } from './kit'
import { ADMIN_UNRECORDED, adminProbes } from './probesAdmin'
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
  const olive = fx.data.auth.users.find((u) => u.username === 'olive')!
  const group = fx.data.admin.groups[0]
  const rev = () => fx.call('GET', P).body.rev as string
  const meta = () => fx.call('GET', P).body.meta as Record<string, unknown>
  return [
    ...adminProbes(fx),
    ['GET', '/api/session'],
    ['GET', '/api/meta'],
    ['GET', '/api/settings'],
    ['GET', '/api/notifications/unread'],
    ['GET', '/api/prefs'],
    ['GET', '/api/spaces/default/daily'],
    ['GET', '/api/spaces/default/checks'],
    ['GET', '/api/auth/providers'],
    ['POST', '/api/auth/discover', { email: 'tang@nowhere.example' }],
    ['GET', '/api/captcha'],
    ['GET', '/api/account'],
    ['GET', '/api/account/links'],
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
    ['POST', '/api/login', { identifier: 'admin', password: 'x' }],
    ['POST', '/api/logout'],
    ['POST', '/api/setup', { token: 't', username: 'first', password: 'a-long-password' }],
    ['POST', '/api/invites/check', { token: 'inv' }],
    ['POST', '/api/invites/accept', { token: 'inv', username: 'dave', password: 'a-long-password' }],
    ['POST', '/api/auth/oidc/company/start', { intent: 'login' }],
    ['POST', '/api/auth/passkey/begin', {}],
    ['POST', '/api/auth/passkey/finish?ceremony=c&return=%2F', { id: 'cred' }],
    ['POST', '/api/account/passkeys/begin', { name: 'Key' }],
    ['POST', '/api/account/passkeys/finish?ceremony=c', { id: 'cred' }],
    ['GET', '/api/register'],
    ['POST', '/api/register', { username: 'tang', email: 'tang@example.test', password: 'a-long-password' }],
    ['POST', '/api/register/confirm', { token: 'tok' }],
    // The waiting page of a sign-up that asked to join (3.6 d5): its request is made here, as a confirmation makes one.
    [
      'POST',
      '/api/register/request',
      () => {
        fx.data.auth.joinWaiting.jtok = {
          state: 'pending',
          tenant: { slug: 'acme', name: 'Acme' },
          username: 'joiner',
          email: 'joiner@acme.example',
          created: '2026-11-20T08:00:00Z',
          expires: '2026-12-20T08:00:00Z',
          decidedAt: null,
        }
        return { token: 'jtok' }
      },
    ],
    ['POST', '/api/register/request/withdraw', { token: 'jtok' }],
    ['POST', '/api/password/forgot', { login: 'admin' }],
    ['POST', '/api/password/reset/check', { token: 'tok' }],
    ['POST', '/api/password/reset', { token: 'tok', password: 'a-long-password' }],
    ['POST', '/api/email/confirm', { token: 'tok' }],
    ['GET', '/api/oauth/consent?req=r1'],
    ['POST', '/api/oauth/consent', { req: 'r1', approve: true }],
    ['DELETE', P],
    ['DELETE', '/api/spaces/default/pages/NOPE'],
    // the platforms a space is stored on
    ['GET', '/api/account/links'],
    ['POST', '/api/account/links/github/start', { purpose: 'repos', return: '/' }],
    ['GET', '/api/account/links/github/owners'],
    ['GET', '/api/account/links/github/repos?installation=5001'],
    ['DELETE', '/api/account/leftovers/github/880100000'],
    ['DELETE', '/api/account/links/link-github/repos'],
    ['GET', '/api/admin/providers'],
    ['GET', '/api/admin/providers/github'],
    ['PUT', '/api/admin/providers/github', { appId: '1', privateKey: 'k', clientId: 'c', clientSecret: 's' }],
    ['POST', '/api/admin/providers/github/check', {}],
    ['POST', '/api/admin/providers/github/manifest', { organization: 'acme' }],
    ['POST', '/api/admin/providers/github/manifest/retry', {}],
    // the GitHub installations of their owners: the person's own and the organisation's
    ['GET', '/api/account/github/installations'],
    ['POST', '/api/account/github/install', { return: '/' }],
    ['GET', '/api/account/github/installations/5101/spaces'],
    ['GET', '/api/account/github/installations/5101/repos'],
    ['DELETE', '/api/account/github/installations/5101'],
    ['GET', '/api/admin/github/installations'],
    ['POST', '/api/admin/github/install', { account: 'kazuhahub', return: '/' }],
    ['GET', '/api/admin/github/installations/5001/spaces'],
    ['GET', '/api/admin/github/installations/5001/repos'],
    ['DELETE', '/api/admin/github/installations/5001'],
    ['DELETE', '/api/admin/providers/github'],
    [
      'POST',
      '/api/admin/providers',
      { id: 'gl', type: 'gitlab', host: 'gl.example', clientId: 'c', clientSecret: 's' },
    ],
    ['GET', '/api/admin/providers/gl'],
    ['PATCH', '/api/admin/providers/gl', { name: 'GL' }],
    ['POST', '/api/admin/providers/gl/check', {}],
    ['DELETE', '/api/admin/providers/gl'],
    // the person's own account (as the administrator, who the probes above may have signed out)
    ['POST', '/api/login', { identifier: 'admin', password: 'x' }],
    ['PATCH', '/api/account', { name: '新名字' }],
    ['POST', '/api/account/password', { current: 'x', new: 'a-long-password' }],
    ['DELETE', '/api/account/links/id1'],
    ['POST', '/api/account/links/company/start', { purpose: 'signin', return: '/settings' }],
    ['PATCH', '/api/account/passkeys/pk1', { name: 'Phone' }],
    ['DELETE', '/api/account/passkeys/pk1'],
    ['POST', '/api/account/emails', { email: 'second@example.test' }],
    ['POST', '/api/account/emails/verify', { email: 'second@example.test' }],
    ['POST', '/api/account/emails/primary', { email: 'second@example.test' }],
    ['DELETE', '/api/account/emails/second%40example.test'],
    ['POST', '/api/account/email/verify', {}],
    ['GET', '/api/account/usage'],
    ['GET', '/api/account/tokens'],
    ['POST', '/api/account/tokens', { name: 'n', spaces: ['default'], access: 'read' }],
    ['DELETE', '/api/account/tokens/token1'],
    ['GET', '/api/account/grants'],
    ['DELETE', '/api/account/grants/grant1'],
    // notifications and preferences
    ['GET', '/api/notifications'],
    ['POST', '/api/notifications/read', { ids: ['x'] }],
    ['POST', '/api/notifications/read-all', {}],
    ['PATCH', '/api/prefs', { set: { a: 1 } }],
    // the people of a space, and the groups in it
    ['GET', '/api/spaces/default/members'],
    ['POST', '/api/spaces/default/members', { username: 'olive', role: 'viewer' }],
    [
      'POST',
      '/api/spaces/default/members/batch',
      { rows: [{ user: 'pam' }, { user: 'pam' }, { user: 'admin' }, { user: 'nobody' }, { user: '' }], dryRun: true },
    ],
    ['GET', '/api/spaces/default/members/effective'],
    ['PATCH', `/api/spaces/default/members/${olive.id}`, { role: 'editor' }],
    ['DELETE', `/api/spaces/default/members/${olive.id}`],
    ['POST', '/api/spaces/default/groups', { groupId: group.id, role: 'viewer' }],
    ['PATCH', `/api/spaces/default/groups/${group.id}`, { role: 'editor' }],
    ['DELETE', `/api/spaces/default/groups/${group.id}`],
    ['GET', '/api/spaces/default/people?q=ol'],
    ['GET', '/api/people?q=ol'],
    ['GET', '/api/groups?q=eng'],
    // the day's text
    ['GET', '/api/spaces/default/daily/settings'],
    ['PUT', '/api/spaces/default/daily/settings', { daily: { folder: '工作' } }],
    ['GET', '/api/spaces/default/daily/prefs'],
    ['PUT', '/api/spaces/default/daily/prefs', { inherit: true, apis: [] }],
    // what the editor and the settings read
    ['GET', `${other.id ? `/api/spaces/default/pages/${other.id}` : P}/links`],
    ['GET', `/api/spaces/default/pages/${other.id}/backlinks`],
    ['POST', '/api/spaces/default/types/task/rename-field', { baseRev: 'x', from: 'status', to: 'state' }],
    ['GET', '/api/spaces/default/templates/diary'],
    // the remote repository
    ['PUT', '/api/spaces/default/remote', { url: 'https://example.test/n.git', credential: { type: 'token' } }],
    ['GET', '/api/spaces/default/remote'],
    ['POST', '/api/spaces/default/remote/key', {}],
    ['POST', '/api/spaces/default/remote/sync', {}],
    ['POST', '/api/spaces/default/remote/accept', { commit: 'c' }],
    ['PUT', '/api/spaces/default/remote/assets', { url: 'https://example.test/a.git', credential: { type: 'token' } }],
    ['GET', '/api/spaces/default/remote/assets'],
    ['POST', '/api/spaces/default/remote/assets/move', {}],
    ['DELETE', '/api/spaces/default/remote/assets'],
    ['DELETE', '/api/spaces/default/remote'],
  ]
}

// What the real server's answer is for a call that the contract suite cannot make (it needs a provider, a mail
// server or an authorization client): nothing is recorded, so nothing is compared. Each says why.
const UNRECORDED: Record<string, string> = {
  'GET /api/captcha 200': 'the server answers it only when a captcha is on',
  'POST /api/auth/oidc/company/start 200': 'needs a provider',
  'POST /api/auth/oidc/{}/start 200': 'needs a provider',
  'POST /api/account/links/company/start 200': 'needs a provider',
  'POST /api/account/links/{}/start 200': 'needs a provider',
  'POST /api/auth/passkey/begin 200': 'needs a passkey',
  'POST /api/auth/passkey/finish 200': 'needs a passkey',
  'POST /api/account/passkeys/begin 200': 'needs a passkey',
  'POST /api/account/passkeys/finish 200': 'needs a passkey',
  'POST /api/register 202': 'needs mail',
  'POST /api/register/confirm 200': 'needs mail',
  'POST /api/register/request 200': 'needs mail and a domain verified by DNS',
  'POST /api/register/request/withdraw 200': 'needs mail and a domain verified by DNS',
  'POST /api/password/forgot 202': 'needs mail',
  'POST /api/password/reset/check 200': 'needs mail',
  'GET /api/oauth/consent 200': 'needs an authorization client',
  'POST /api/oauth/consent 200': 'needs an authorization client',
  'POST /api/setup 200': 'needs a server with no account',
  ...ADMIN_UNRECORDED,
  'PATCH /api/account/passkeys/{} 200': 'needs a passkey',
  'POST /api/account/emails 202': 'needs mail',
  'POST /api/account/emails/verify 200': 'needs mail',
  'POST /api/account/email/verify 200': 'needs mail',
  'GET /api/account/grants 200': 'needs an authorization server',
  'POST /api/spaces/default/members 200': 'the contract suite adds people through the batch',
  'POST /api/spaces/default/groups 200':
    'needs a group, which the contract suite makes only through the administration',
  'POST /api/spaces/default/types/task/rename-field 200': 'the contract suite does not rename a field yet',
  'PUT /api/spaces/default/remote 200': 'needs a remote repository',
  'GET /api/spaces/default/remote 200': 'needs a remote repository',
  'POST /api/spaces/default/remote/sync 200': 'needs a remote repository',
  'POST /api/spaces/default/remote/accept 200': 'needs a remote repository',
  'PUT /api/spaces/default/remote/assets 200': 'needs a remote repository',
  'GET /api/spaces/default/remote/assets 200': 'needs a remote repository',
  'POST /api/spaces/default/remote/assets/move 200': 'needs a remote repository',
  'POST /api/account/links/github/start 200': 'needs a platform',
  'GET /api/account/links/github/owners 200': 'needs a platform',
  'GET /api/account/links/github/repos 200': 'needs a platform',
  'GET /api/admin/providers 200': 'needs a platform',
  'GET /api/admin/providers/github 200': 'needs a platform',
  'PUT /api/admin/providers/github 200': 'needs a platform',
  'POST /api/admin/providers/github/check 200': 'needs a platform',
  'POST /api/admin/providers/github/manifest 200': 'needs a platform',
  'POST /api/admin/providers/github/manifest/retry 200': 'needs a platform',
  'POST /api/account/github/install 200': 'needs a platform',
  'GET /api/account/github/installations/{}/spaces 200': 'needs a platform',
  'GET /api/account/github/installations/{}/repos 200': 'needs a platform',
  'DELETE /api/account/github/installations/{} 200': 'needs a platform',
  'GET /api/admin/github/installations 200':
    'needs an organisation, which the contract suite administers only through the site',
  'POST /api/admin/github/install 200': 'needs a platform',
  'GET /api/admin/github/installations/{}/spaces 200': 'needs a platform',
  'GET /api/admin/github/installations/{}/repos 200': 'needs a platform',
  'DELETE /api/admin/github/installations/{} 200': 'needs a platform',
  'POST /api/admin/providers 201': 'needs a platform',
  'GET /api/admin/providers/gl 200': 'needs a platform',
  'PATCH /api/admin/providers/gl 200': 'needs a platform',
  'POST /api/admin/providers/gl/check 200': 'needs a platform',
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
      githubProvider(d, 'connected')
      githubInstallations(d)
      // The server lists a plain address first, as a provider that holds nothing: that is the recorded shape of an entry.
      d.area.connections.providers.unshift({
        id: 'git',
        type: 'git',
        host: '',
        name: 'Git',
        owner: 'site',
        capabilities: {
          createRepo: false,
          installations: false,
          connectAccount: false,
          storageTarget: false,
          partialSync: 'unknown',
        },
        configured: true,
        app: null,
        userAuthorization: false,
        connection: null,
        leftovers: [],
      })
      leaveRepository(d, 'tang-gh/poems')
      addPerson(d, 'olive')
      addPerson(d, 'pam')
      d.area.daily.site.default = { kind: 'list' }
      addGroup(d, 'Engineering')
      d.spaces[0].templates.push({ name: 'diary', path: '.petalbook/templates/diary.md', rev: 'r', meta: {}, body: '' })
      d.area.account.passkeys.push({ id: 'pk1', name: 'Key', created: '2026-01-01T00:00:00Z', synced: false })
      d.area.account.links.push({
        id: 'id1',
        providerId: 'company',
        provider: 'Company',
        slug: 'company',
        kind: 'oidc',
        label: 'a@b.c',
        signin: true,
        created: '2026-01-01T00:00:00Z',
      })
      addToken(d, 'one')
      withAuthorizationServer(d)
      approveApp(d, 'App')
      addNotification(d, 'invite', 'default', { space: 'S', role: 'editor', by: 'B' })
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
      const answer = JSON.parse(JSON.stringify(r.body))
      // The contract server has no platform set up: of a provider's App and connection only the null is recorded.
      if (key === 'GET /api/account/links 200')
        for (const p of answer.providers) Object.assign(p, { app: null, connection: null })
      // The contract server's account has a password and nothing else: of the list of sign-in methods only the
      // password row is recorded (the other kinds are checked against the server in the Go tests).
      if (key === 'GET /api/account 200')
        answer.methods = answer.methods.filter((x: { type: string }) => x.type === 'password')
      const [s, v] = pruned(shape, answer)
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
