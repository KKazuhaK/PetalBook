// Dev-only mock of docs/api.md, served by Vite. Enabled by `npm run dev:mock`
// (vite --mode mock); never imported by a production build. Data lives in
// memory and is reset on restart. MOCK_DELAY_MS adds latency to every call.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import type { RemoteState } from '../src/api/types.ts'
import {
  createMockServer,
  MOCK_ADMIN,
  MOCK_INVITE_TOKEN,
  MOCK_SETUP_TOKEN,
  type MockOptions,
} from '../src/api/mock/server.ts'

// The body as bytes: an uploaded file must reach the mock unchanged. JSON
// requests read the same bytes as text.
function readBody(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks)))
    req.on('error', reject)
  })
}

// MOCK_MODE picks what the mock plays:
//   single (default)  one space, no sign-in, as `serve --space`
//   accounts          sign-in with the seeded administrator, an invite and a fake single sign-on provider
//   setup             accounts mode with no user yet: the first-run page
// In accounts mode MOCK_REMOTE=ok|offline|rewritten|error starts the first
// space following a remote in that state. Whatever the state, it can be changed
// while the server runs:
//   curl -X POST -H 'Content-Type: application/json' -d '{"state":"offline"}' \
//        http://localhost:PORT/api/_mock/remote
// Connecting a space to an address that contains "diverged", "unreachable",
// "refused", "ahead" or "behind" plays that outcome (see MockRemoteOptions).
function optionsFromEnv(): MockOptions {
  const mode = process.env.MOCK_MODE ?? 'single'
  if (mode === 'single') return {}
  const oidc = { name: '示例单点登录' }
  // A few providers to look at: the first (`sso`) answers the pre-2.8 endpoints, the others are of the other kinds.
  const providers: MockOptions['providers'] = [
    {
      slug: 'campus',
      kind: 'saml',
      name: 'Campus login',
      order: 1,
      saml: { metadataUrl: 'https://idp.example/metadata' },
    },
    {
      slug: 'github',
      kind: 'github',
      name: 'GitHub',
      icon: 'preset:github',
      order: 2,
      github: { clientId: 'dev', organizations: ['KazuhaHub'] },
    },
  ]
  if (mode === 'setup') return { mode: 'accounts', admin: false, oidc, providers }
  if (mode !== 'accounts') throw new Error(`MOCK_MODE must be single, accounts or setup, not ${mode}`)
  const start = process.env.MOCK_REMOTE
  if (start && !['ok', 'offline', 'rewritten', 'error'].includes(start))
    throw new Error(`MOCK_REMOTE must be ok, offline, rewritten or error, not ${start}`)
  // MOCK_OAUTH=1 plays a server with an authorization server: /api/_mock/oauth opens the consent page.
  return {
    mode: 'accounts',
    oidc,
    providers,
    oauth: process.env.MOCK_OAUTH === '1',
    invite: { token: MOCK_INVITE_TOKEN },
    github: { configured: true },
    ...(start ? { remote: { start: start as RemoteState } } : {}),
  }
}

// Accounts mode starts with something to read in the notification centre: a person who adds the administrator to a
// space of theirs, and a file in the administrator's own space that is no page (the problem the check page lists).
function seedNotifications(server: ReturnType<typeof createMockServer>) {
  const call = (method: string, path: string, body: unknown, cookie: string | null) => {
    const r = server.handle(method, path, JSON.stringify(body), 'application/json', cookie)
    return { body: r.body as Record<string, any>, cookie: r.headers?.['Set-Cookie']?.split(';')[0] ?? null }
  }
  const admin = call(
    'POST',
    '/api/login',
    { username: MOCK_ADMIN.username, password: MOCK_ADMIN.password },
    null,
  ).cookie
  const invite = call('POST', '/api/invites', { role: 'member' }, admin).body.token
  const olive = call(
    'POST',
    '/api/invites/accept',
    { token: invite, username: 'olive', name: '陈默', password: 'a-long-password' },
    null,
  )
  const space = call('POST', '/api/spaces', { name: '团队文档' }, olive.cookie).body.id
  call('POST', `/api/spaces/${space}/members`, { username: MOCK_ADMIN.username, role: 'editor' }, olive.cookie)
  const mine = (call('GET', '/api/spaces', undefined, admin).body.spaces as { id: string; personal: boolean }[]).find(
    (x) => x.personal,
  )
  if (mine)
    server.addOrphan(mine.id, {
      path: 'stray.md',
      kind: 'no_id',
      meta: { title: '没有 id 的文件' },
      body: '一个不是页面的文件\n',
    })
}

// MOCK_EMAILS=1 (accounts mode) starts with mail on, more than one address per account allowed, and the administrator
// holding four addresses: a verified primary, a verified second one, one that waits for its link, and a claim of an
// address another account holds unverified.
function seedEmails(server: ReturnType<typeof createMockServer>) {
  const login = server.handle(
    'POST',
    '/api/login',
    JSON.stringify({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password }),
    'application/json',
    null,
  )
  const cookie = login.headers?.['Set-Cookie']?.split(';')[0] ?? null
  const call = (method: string, path: string, body: unknown) =>
    server.handle(method, path, JSON.stringify(body), 'application/json', cookie)
  const confirm = (to: string) => {
    const token = server.mail.outbox.filter((m) => m.to === to && m.kind === 'verify').at(-1)?.token
    if (token) call('POST', '/api/email/confirm', { token })
  }
  call('PUT', '/api/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' })
  call('PUT', '/api/admin/settings/accounts.multipleEmails', { value: true })
  call('PATCH', '/api/account', { email: 'admin@example.com' })
  call('POST', '/api/account/email/verify', {})
  confirm('admin@example.com')
  call('POST', '/api/account/emails', { email: 'kazuha.mo@university-of-a-long-name.example.edu' })
  confirm('kazuha.mo@university-of-a-long-name.example.edu')
  call('POST', '/api/account/emails', { email: 'personal@example.org' })
  // A claim: another account holds this address unverified, so it waits for the link.
  const inv = call('POST', '/api/invites', { role: 'member' }).body as { token: string }
  const joined = server.handle(
    'POST',
    '/api/invites/accept',
    JSON.stringify({ token: inv.token, username: 'tang', password: 'a-long-password' }),
    'application/json',
    null,
  )
  server.handle(
    'PATCH',
    '/api/account',
    JSON.stringify({ email: 'shared@example.org' }),
    'application/json',
    joined.headers?.['Set-Cookie']?.split(';')[0] ?? null,
  )
  call('POST', '/api/account/emails', { email: 'shared@example.org' })
}

// MOCK_ORGS=1 (accounts mode) starts with mail, tenants and organisations people create switched on, registration for
// joining open, and one organisation a person made already (to see its badge on the Tenants page).
function seedOrgs(server: ReturnType<typeof createMockServer>) {
  const login = server.handle(
    'POST',
    '/api/login',
    JSON.stringify({ username: MOCK_ADMIN.username, password: MOCK_ADMIN.password }),
    'application/json',
    null,
  )
  const cookie = login.headers?.['Set-Cookie']?.split(';')[0] ?? null
  const call = (method: string, path: string, body: unknown) =>
    server.handle(method, path, JSON.stringify(body), 'application/json', cookie)
  call('PUT', '/api/admin/mail', { enabled: true, host: 'smtp.example.test', from: 'notes@example.test' })
  call('PUT', '/api/admin/settings/features.tenancy', { value: true })
  call('PUT', '/api/admin/settings/features.orgCreation', { value: true })
  call('PUT', '/api/admin/settings/features.registration', { value: true })
  call('POST', '/api/admin/tenants', { slug: 'globex', name: 'Globex Corporation' })
  server.handle(
    'POST',
    '/api/register',
    JSON.stringify({
      username: 'bea',
      name: 'Bea',
      email: 'bea@example.org',
      password: 'a-long-password',
      org: { name: 'Bea Bakery', slug: 'bea-bakery' },
    }),
    'application/json',
    null,
  )
  const link = server.mail.outbox.find((m) => m.kind === 'register_org')?.token
  if (link) server.handle('POST', '/api/register/confirm', JSON.stringify({ token: link }), 'application/json', null)
}

export function mockApi(): Plugin {
  const options = optionsFromEnv()
  const server = createMockServer(options)
  if (options.mode === 'accounts' && options.admin !== false) seedNotifications(server)
  if (options.mode === 'accounts' && options.admin !== false && process.env.MOCK_EMAILS === '1') seedEmails(server)
  if (options.mode === 'accounts' && options.admin !== false && process.env.MOCK_ORGS === '1') seedOrgs(server)
  const delay = Number(process.env.MOCK_DELAY_MS ?? 0)
  return {
    name: 'petalbook-mock-api',
    configureServer(vite) {
      // The mock stands in for the server's log: where to start in each mode.
      vite.httpServer?.once('listening', () => {
        const origin = vite.resolvedUrls?.local[0]?.replace(/\/$/, '') ?? ''
        if (options.mode !== 'accounts') return
        if (options.admin === false) console.log(`[mock] first run: ${origin}/setup#${MOCK_SETUP_TOKEN}`)
        else
          console.log(
            `[mock] sign in as ${MOCK_ADMIN.username} / ${MOCK_ADMIN.password}; invite link: ${origin}/invite#${MOCK_INVITE_TOKEN}`,
          )
      })
      vite.middlewares.use(async (req: IncomingMessage, res: ServerResponse, next) => {
        if (!req.url?.startsWith('/api/')) return next()
        const body = await readBody(req)
        if (delay > 0) await new Promise((r) => setTimeout(r, delay))
        const out = server.handle(
          req.method ?? 'GET',
          req.url,
          body.toString('utf8'),
          req.headers['content-type'],
          req.headers.cookie,
          new Uint8Array(body),
          typeof req.headers['petalbook-scope'] === 'string' ? req.headers['petalbook-scope'] : null,
        )
        res.statusCode = out.status
        for (const [k, v] of Object.entries(out.headers ?? {})) res.setHeader(k, v)
        if (out.bytes) return res.end(Buffer.from(out.bytes))
        if (out.status === 204 || out.status === 303) return res.end()
        res.setHeader('Content-Type', 'application/json; charset=utf-8')
        res.end(JSON.stringify(out.body))
      })
    },
  }
}
