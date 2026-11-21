// Dev-only mock of docs/api.md, served by Vite. Enabled by `npm run dev:mock`
// (vite --mode mock); never imported by a production build. Data lives in
// memory and is reset on restart. MOCK_DELAY_MS adds latency to every call.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import { createMockServer } from '../src/api/mock/server.ts'

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    req.on('error', reject)
  })
}

export function mockApi(): Plugin {
  const server = createMockServer()
  const delay = Number(process.env.MOCK_DELAY_MS ?? 0)
  return {
    name: 'petalbook-mock-api',
    configureServer(vite) {
      vite.middlewares.use(async (req: IncomingMessage, res: ServerResponse, next) => {
        if (!req.url?.startsWith('/api/')) return next()
        const body = await readBody(req)
        if (delay > 0) await new Promise((r) => setTimeout(r, delay))
        const out = server.handle(req.method ?? 'GET', req.url, body, req.headers['content-type'])
        res.statusCode = out.status
        if (out.status === 204) return res.end()
        res.setHeader('Content-Type', 'application/json; charset=utf-8')
        res.end(JSON.stringify(out.body))
      })
    },
  }
}
