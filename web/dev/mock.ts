// Dev-only mock of docs/api.md, served by Vite. Enabled by `npm run dev:mock`
// (vite --mode mock); never imported by a production build. Data lives in
// memory and is reset on restart. MOCK_DELAY_MS adds latency to every call.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { createMockServer, MOCK_ADMIN, MOCK_INVITE_TOKEN, MOCK_SETUP_TOKEN, type MockOptions } from '../src/api/mock/server.ts'

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.on('error', reject)
  })
}

// MOCK_MODE picks what the mock plays:
//   single (default)  one space, no sign-in, as `serve --space`
//   accounts          sign-in with the seeded administrator, an invite and a fake single sign-on provider
//   setup             accounts mode with no user yet: the first-run page
function optionsFromEnv(): MockOptions {
  const mode = process.env.MOCK_MODE ?? 'single'
  if (mode === 'single') return {}
  const oidc = { name: '示例单点登录' }
  if (mode === 'setup') return { mode: 'accounts', admin: false, oidc }
  if (mode !== 'accounts') throw new Error(`MOCK_MODE must be single, accounts or setup, not ${mode}`)
  return { mode: 'accounts', oidc, invite: { token: MOCK_INVITE_TOKEN } }
}

export function mockApi(): Plugin {
  const options = optionsFromEnv()
  const server = createMockServer(options)
  const delay = Number(process.env.MOCK_DELAY_MS ?? 0)
  return {
    name: 'petalbook-mock-api',
    configureServer(vite) {
      // The mock stands in for the server's log: where to start in each mode.
      vite.httpServer?.once('listening', () => {
        const origin = vite.resolvedUrls?.local[0]?.replace(/\/$/, '') ?? ''
        if (options.mode !== 'accounts') return
        if (options.admin === false) console.log(`[mock] first run: ${origin}/setup#${MOCK_SETUP_TOKEN}`)
        else console.log(`[mock] sign in as ${MOCK_ADMIN.username} / ${MOCK_ADMIN.password}; invite link: ${origin}/invite#${MOCK_INVITE_TOKEN}`)
      })
      vite.middlewares.use(async (req: IncomingMessage, res: ServerResponse, next) => {
        if (!req.url?.startsWith('/api/')) return next()
        const body = await readBody(req)
        if (delay > 0) await new Promise((r) => setTimeout(r, delay))
        const out = server.handle(req.method ?? 'GET', req.url, body, req.headers['content-type'], req.headers.cookie)
        res.statusCode = out.status
        for (const [k, v] of Object.entries(out.headers ?? {})) res.setHeader(k, v)
        if (out.status === 204 || out.status === 303) return res.end()
        res.setHeader('Content-Type', 'application/json; charset=utf-8')
        res.end(JSON.stringify(out.body))
      })
    },
  }
}
