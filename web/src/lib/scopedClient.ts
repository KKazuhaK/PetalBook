import { useCallback } from 'react'
import {
  MutationCache,
  QueryCache,
  QueryClient,
  useQueryClient,
  type QueryFunctionContext,
} from '@tanstack/react-query'
import { noteSignal, runInScope } from './adminScope'
import { refreshFeaturesOnRefusal } from './features'

// The QueryClient of a tenant's scope (lib/adminScope): its queries and mutations run in the scope, whatever calls them
// and whenever, so a request's scope is decided by the cache it belongs to.

// The tenant a QueryClient reads in (null: the site's own client).
const tenantOf = new WeakMap<QueryClient, string>()
export const scopeOfClient = (client: QueryClient): string | null => tenantOf.get(client) ?? null

type AnyFn = (...args: never[]) => unknown

// fn as make has it run in a scope: the same function each time it is asked for (so options compare equal), and never
// wrapped twice. One memo for each scoped client, so a function two tenants' clients share runs in each one's own.
function scopeFns() {
  const wrapped = new WeakMap<AnyFn, AnyFn>()
  const isWrapped = new WeakSet<AnyFn>()
  return <F extends AnyFn>(fn: F, make: (fn: F) => F): F => {
    if (isWrapped.has(fn)) return fn
    let w = wrapped.get(fn) as F | undefined
    if (!w) {
      w = make(fn)
      wrapped.set(fn, w)
      isWrapped.add(w)
    }
    return w
  }
}

// A query's context with its signal noted as the scope's when the function reads it. The signal is read lazily (a
// query whose function never reads it is not cancelled when no one watches it any more), so it is not read here.
function scopedContext<C extends object>(tenant: string, ctx: C): C {
  const { signal, ...rest } = Object.getOwnPropertyDescriptors(ctx) as Record<string, PropertyDescriptor>
  const out = Object.defineProperties({}, rest) as C
  if (signal)
    Object.defineProperty(out, 'signal', {
      enumerable: true,
      get: () => {
        const s = (ctx as { signal: AbortSignal }).signal
        noteSignal(s, tenant)
        return s
      },
    })
  return out
}

// The scoped clients made so far: one for each site client, for the one tenant last shown and the person who was
// signed in then (a sign-in clears the site client; the scoped one is made again for the new person). One that is
// replaced is left to the garbage collector with what it held.
const made = new WeakMap<QueryClient, { tenant: string; who: unknown; client: QueryClient }>()

// The QueryClient of tenant's scope for the site's client `main`: the same one while the same person looks at the same
// tenant (the tabs of a tenant's page share it). Its queries and mutations run in the scope; a write that succeeds in
// it, and anything it invalidates, also has the site's client read `invalidateInMain` again (a tenant's overview and
// list show what the scope changed).
export function scopedClient(
  main: QueryClient,
  tenant: string,
  who: unknown,
  invalidateInMain: readonly unknown[],
): QueryClient {
  const prev = made.get(main)
  if (prev && prev.tenant === tenant && prev.who === who) return prev.client
  const refreshMain = () => void main.invalidateQueries({ queryKey: invalidateInMain })
  const client = new QueryClient({
    defaultOptions: main.getDefaultOptions(),
    // A feature switched off while the console is open is noticed from a refusal, as in the site's client.
    queryCache: new QueryCache({ onError: (e) => refreshFeaturesOnRefusal(main, e) }),
    mutationCache: new MutationCache({ onSuccess: refreshMain, onError: (e) => refreshFeaturesOnRefusal(main, e) }),
  })
  tenantOf.set(client, tenant)
  const inScopeFn = scopeFns()

  const defaultQuery = client.defaultQueryOptions.bind(client)
  client.defaultQueryOptions = ((options: Parameters<typeof defaultQuery>[0]) => {
    const o = defaultQuery(options)
    const fn = o.queryFn
    if (typeof fn === 'function')
      o.queryFn = inScopeFn(
        fn as AnyFn,
        (f) =>
          ((ctx: QueryFunctionContext) =>
            runInScope(tenant, () =>
              (f as unknown as (c: QueryFunctionContext) => unknown)(scopedContext(tenant, ctx)),
            )) as unknown as typeof f,
      ) as typeof fn
    return o
  }) as typeof client.defaultQueryOptions

  const defaultMutation = client.defaultMutationOptions.bind(client)
  client.defaultMutationOptions = ((options: Parameters<typeof defaultMutation>[0]) => {
    const o = defaultMutation(options)
    const fn = o.mutationFn
    if (typeof fn === 'function')
      o.mutationFn = inScopeFn(
        fn as AnyFn,
        (f) =>
          ((...args: unknown[]) =>
            runInScope(tenant, () => (f as (...a: unknown[]) => unknown)(...args))) as unknown as typeof f,
      ) as typeof fn
    return o
  }) as typeof client.defaultMutationOptions

  const invalidate = client.invalidateQueries.bind(client)
  client.invalidateQueries = ((...args: Parameters<typeof invalidate>) => {
    refreshMain()
    return invalidate(...args)
  }) as typeof client.invalidateQueries

  made.set(main, { tenant, who, client })
  return client
}

// Runs what a component calls by itself (outside a query or a mutation: a button's write, an effect's read) in the
// scope of the client it is under, as the client's own queries run. A request made after an await inside fn carries
// the scope only when it is given `signal`.
export function useInScope() {
  const tenant = scopeOfClient(useQueryClient())
  return useCallback(
    <T>(fn: () => T, signal?: AbortSignal): T => (tenant === null ? fn() : runInScope(tenant, fn, signal)),
    [tenant],
  )
}
