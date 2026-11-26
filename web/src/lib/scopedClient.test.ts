import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { MutationObserver, QueryClient } from '@tanstack/react-query'
import { request } from '@/api/client'
import { keys } from '@/api/hooks'
import { adminApi } from '@/api/admin'
import { scopeOfClient, scopedClient } from './scopedClient'

// A request's scope is decided by the cache it belongs to: the tenant's client sends the header from its queries and
// mutations, whenever they run (a retry, a later page); the site's client never does, even while the tenant's is busy.

let sent: { url: string; scope: string | null }[] = []
const real = globalThis.fetch
let hold: Promise<void> | null = null
beforeEach(() => {
  sent = []
  hold = null
  globalThis.fetch = async (input, init) => {
    sent.push({ url: String(input), scope: new Headers(init?.headers).get('petalbook-scope') })
    if (hold) await hold
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
})
afterEach(() => {
  globalThis.fetch = real
})

const fresh = () => new QueryClient({ defaultOptions: { queries: { retry: false } } })

describe('scopedClient', () => {
  it("sends the header from the tenant's queries only, while both are in flight", async () => {
    const main = fresh()
    const scoped = scopedClient(main, 'T1', 'u1', keys.tenants)
    expect(scopeOfClient(scoped)).toBe('T1')
    expect(scopeOfClient(main)).toBeNull()
    let release!: () => void
    hold = new Promise((r) => (release = r))
    const a = scoped.fetchQuery({ queryKey: ['users'], queryFn: ({ signal }) => request('/users', { signal }) })
    const b = main.fetchQuery({ queryKey: ['status'], queryFn: ({ signal }) => request('/admin/status', { signal }) })
    release()
    await Promise.all([a, b])
    expect(sent).toEqual([
      { url: '/api/users', scope: 'tenant:T1' },
      { url: '/api/admin/status', scope: null },
    ])
  })

  it('keeps the scope on a retry and on a request made after an await with the signal', async () => {
    const main = fresh()
    const scoped = scopedClient(main, 'T2', 'u1', keys.tenants)
    let tries = 0
    await scoped.fetchQuery({
      queryKey: ['pages'],
      retry: 1,
      retryDelay: 0,
      queryFn: async ({ signal }) => {
        await request('/admin/a', { signal })
        await Promise.resolve()
        await request('/admin/b', { signal })
        if (++tries === 1) throw new Error('again')
        return 1
      },
    })
    // A site query made between the tries carries nothing.
    await main.fetchQuery({ queryKey: ['x'], queryFn: ({ signal }) => request('/admin/c', { signal }) })
    expect(sent.filter((r) => r.url !== '/api/admin/c').every((r) => r.scope === 'tenant:T2')).toBe(true)
    expect(sent.filter((r) => r.url !== '/api/admin/c')).toHaveLength(4)
    expect(sent.find((r) => r.url === '/api/admin/c')?.scope).toBeNull()
  })

  it("runs mutations in the scope and has the site's client read the tenants again", async () => {
    const main = fresh()
    await main.fetchQuery({ queryKey: [...keys.tenants, 'overview', 'T3'], queryFn: () => 1, staleTime: Infinity })
    const scoped = scopedClient(main, 'T3', 'u1', keys.tenants)
    const m = new MutationObserver(scoped, { mutationFn: () => request('/users/x', { method: 'PATCH' }) })
    await m.mutate()
    expect(sent).toEqual([{ url: '/api/users/x', scope: 'tenant:T3' }])
    expect(main.getQueryState([...keys.tenants, 'overview', 'T3'])?.isInvalidated).toBe(true)
  })

  it('is one client for a tenant and a person, and another for another', () => {
    const main = fresh()
    const a = scopedClient(main, 'T4', 'u1', keys.tenants)
    expect(scopedClient(main, 'T4', 'u1', keys.tenants)).toBe(a)
    expect(scopedClient(main, 'T5', 'u1', keys.tenants)).not.toBe(a)
    expect(scopedClient(main, 'T5', 'u2', keys.tenants)).not.toBe(a)
  })
})

describe('the audit download', () => {
  it("names the scope in its address in a tenant's scope, and nothing in the site's", () => {
    expect(adminApi.auditDownloadUrl({ kind: 'login' }, 'T6')).toBe(
      '/api/admin/audit/download?kind=login&scope=tenant%3AT6',
    )
    expect(adminApi.auditDownloadUrl({ kind: 'login' })).toBe('/api/admin/audit/download?kind=login')
  })
})
