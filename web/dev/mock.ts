// Dev-only API for UI work, served by Vite on the fixtures (src/api/fixtures). Enabled by `npm run dev:mock`
// (vite --mode mock); never imported by a production build. Pages render, lists page, and forms save and read back;
// the server's behaviour (refusals, drafts that commit themselves, quotas) is the real server's, reached with
// `npm run dev` against `serve`. Data lives in memory and is reset on restart. MOCK_DELAY_MS adds latency to every call.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { seedAdminDemo } from '../src/api/fixtures/data/adminDemo.ts'
import type { FixtureOptions } from '../src/api/fixtures/data/model.ts'
import { INVITE_TOKEN, MOCK_ADMIN, SETUP_TOKEN } from '../src/api/fixtures/data/people.ts'
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
    // The administration console has people, a group, a plan and a space to show.
    data: mode === 'setup' ? undefined : seedAdminDemo,
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
        else
          console.log(
            `[mock] sign in as ${MOCK_ADMIN.username} / ${MOCK_ADMIN.password}; invite link: ${origin}/invite#${INVITE_TOKEN}`,
          )
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
