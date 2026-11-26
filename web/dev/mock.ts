// Dev-only API for UI work, served by Vite on the fixtures (src/api/fixtures). Enabled by `npm run dev:mock`
// (vite --mode mock); never imported by a production build. Pages render, lists page, and forms save and read back;
// the server's behaviour (refusals, drafts that commit themselves, quotas) is the real server's, reached with
// `npm run dev` against `serve`. Data lives in memory and is reset on restart. MOCK_DELAY_MS adds latency to every call.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { addNotification, addProblems, approveApp, withAuthorizationServer } from '../src/api/fixtures/data/area.ts'
import { githubProvider } from '../src/api/fixtures/data/connections.ts'
import { sharedSpace, type FixtureData, type FixtureOptions } from '../src/api/fixtures/data/model.ts'
import { INVITE_TOKEN, MOCK_ADMIN, SETUP_TOKEN } from '../src/api/fixtures/data/people.ts'
import { addGroup, addPerson } from '../src/api/fixtures/data/scenes.ts'
import { createFixtureServer } from '../src/api/fixtures/server.ts'

// The body as bytes: an uploaded file must reach the server unchanged. JSON requests read the same bytes as text.
function readBody(req: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks)))
    req.on('error', reject)
  })
}

// MOCK_MODE picks what the fixtures play:
//   single (default)  one space, no sign-in, as `serve --space`
//   accounts          sign-in with the seeded administrator, an invite and two sign-in providers
//   setup             accounts mode with no user yet: the first-run page
// In accounts mode some more can be switched on, each by an environment variable:
//   MOCK_REMOTE=ok|offline|rewritten|error  the first space follows a remote repository in that state
//   MOCK_OAUTH=1     the server has an authorization server: a consent page to look at, and a connected app
//   MOCK_EMAILS=1    mail is on, an account may have several addresses, and the administrator has two
//   MOCK_ORGS=1      mail, tenants, registration and creating one's own organization are on
function optionsFromEnv(): FixtureOptions {
  const mode = process.env.MOCK_MODE ?? 'single'
  if (mode === 'single') return {}
  if (mode !== 'accounts' && mode !== 'setup')
    throw new Error(`MOCK_MODE must be single, accounts or setup, not ${mode}`)
  return {
    mode: 'accounts',
    setup: mode === 'setup',
    providers: [
      { slug: 'company', kind: 'oidc', name: '示例单点登录', order: 0 },
      { slug: 'campus', kind: 'saml', name: 'Campus login', order: 1 },
    ],
    invite: { token: INVITE_TOKEN, role: 'member' },
    data: seedAccounts,
  }
}

const REMOTE_STATES = ['ok', 'offline', 'rewritten', 'error'] as const

// Accounts mode starts with something to look at in the settings: another person who shares a space with the
// administrator (and the notification of it), a group, GitHub set up on the server, and a space with a problem.
function seedAccounts(d: FixtureData) {
  const olive = addPerson(d, 'olive')
  const team = sharedSpace(d, 'editor', '团队')
  d.area.members[team.info.id] = [
    { userId: olive.id, role: 'owner' },
    { userId: d.auth.users[0].id, role: 'editor' },
  ]
  addNotification(d, 'invite', team.info.id, { space: '团队', role: 'editor', by: olive.name })
  addProblems(d, d.spaces[0].info, { no_id: 1 })
  addGroup(d, 'Engineering', [d.auth.users[0].id, olive.id])
  githubProvider(d, 'app')

  const remote = process.env.MOCK_REMOTE
  if (remote) {
    if (!(REMOTE_STATES as readonly string[]).includes(remote))
      throw new Error(`MOCK_REMOTE must be ${REMOTE_STATES.join(', ')}, not ${remote}`)
    const state = remote as (typeof REMOTE_STATES)[number]
    d.spaces[0].info.sync = state
    d.area.remotes[d.spaces[0].info.id] = {
      assets: null,
      remote: {
        url: 'https://example.test/notes.git',
        branch: 'main',
        provider: 'git',
        credential: { type: 'token', username: '' },
        state,
        lastSync: '2026-11-25T10:00:00Z',
        error: state === 'error' ? 'authentication failed' : '',
        errorCode: state === 'error' ? 'auth' : '',
        rewritten: state === 'rewritten' ? { lastGood: 'a1b2c3d', remote: 'e4f5a6b' } : null,
      },
    }
  }
  if (process.env.MOCK_OAUTH === '1') {
    withAuthorizationServer(d)
    approveApp(d, 'Notes helper')
    d.auth.consents.dev = {
      client: { name: 'Notes helper', id: 'https://client.example/', host: 'client.example' },
      redirectHost: 'client.example',
      punycode: false,
      scopes: ['read', 'write'],
      spaces: d.spaces.map((s) => ({
        id: s.info.id,
        name: s.info.name,
        permissions: s.info.permissions,
        personal: s.info.personal,
      })),
      user: { id: d.auth.users[0].id, name: d.auth.users[0].name },
    }
  }
  if (process.env.MOCK_EMAILS === '1') {
    d.auth.forgot = true
    d.features['accounts.multipleEmails'] = true
    Object.assign(d.auth.users[0], { email: 'admin@example.test', emailVerified: true })
    d.area.account.emails.push({
      email: 'work@example.test',
      primary: false,
      verified: false,
      created: '2026-11-01T00:00:00Z',
    })
  }
  if (process.env.MOCK_ORGS === '1') {
    d.auth.forgot = true
    d.auth.register = true
    d.auth.orgCreation = true
    for (const key of ['features.tenancy', 'features.registration', 'features.orgCreation']) d.features[key] = true
  }
}

export function mockApi(): Plugin {
  const options = optionsFromEnv()
  const fx = createFixtureServer(options)
  const delay = Number(process.env.MOCK_DELAY_MS ?? 0)
  return {
    name: 'petalbook-mock-api',
    configureServer(vite) {
      // The fixtures stand in for the server's log: where to start in each mode.
      vite.httpServer?.once('listening', () => {
        const origin = vite.resolvedUrls?.local[0]?.replace(/\/$/, '') ?? ''
        if (options.mode !== 'accounts') return
        if (options.setup) console.log(`[mock] first run: ${origin}/setup#${SETUP_TOKEN}`)
        else {
          console.log(
            `[mock] sign in as ${MOCK_ADMIN.username} / ${MOCK_ADMIN.password}; invite link: ${origin}/invite#${INVITE_TOKEN}`,
          )
          if (process.env.MOCK_OAUTH === '1') console.log(`[mock] consent page: ${origin}/oauth/consent?req=dev`)
        }
      })
      vite.middlewares.use(async (req: IncomingMessage, res: ServerResponse, next) => {
        if (!req.url?.startsWith('/api/')) return next()
        const body = await readBody(req)
        if (delay > 0) await new Promise((r) => setTimeout(r, delay))
        const headers = new Headers()
        for (const [k, v] of Object.entries(req.headers)) if (typeof v === 'string') headers.set(k, v)
        const out = fx.handle(req.method ?? 'GET', req.url, body.toString('utf8'), headers, new Uint8Array(body))
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
