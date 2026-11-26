// The console's acting scope: the whole site, or one tenant. A site administrator managing a tenant acts as that
// tenant's administrator (the request header PetalBook-Scope: tenant:<id>, docs/design/3-6-c.md section 5). The scope
// is in the address, so it is per browser tab and survives a reload: `/admin/t/<id>/...` is the console in the tenant's
// scope. The members, spaces and audit tabs of a tenant's own page read in the same scope.
//
// A request's scope is decided by the cache it belongs to, never by a value the page sets: everything under a
// TenantScope reads through a QueryClient of the tenant's own (lib/scopedClient), whose query and mutation functions
// run in the scope (`runInScope`), so a retry, a refetch on focus or a write made after the page moved on keeps the
// scope it was made in, and a query of the site's client never carries it. What a component calls by itself, outside
// a query or a mutation, it runs with `useInScope` (lib/scopedClient).

export const SCOPE_HEADER = 'PetalBook-Scope'
// The query parameter the server reads as the header on an administration GET: for what the browser fetches by its
// address (the audit's CSV), which cannot carry a header.
export const SCOPE_PARAM = 'scope'

// The scope of the requests a function made in a scope still makes after its first await: they pass on the abort
// signal they were given, and the signal is noted here.
const bySignal = new WeakMap<AbortSignal, string>()
// The scope of the function running now, for the requests it makes before its first await. Set and restored around
// one synchronous call only (runInScope); nothing else writes it.
let running: string | null = null

// Notes that requests made with signal are in tenant's scope.
export function noteSignal(signal: AbortSignal, tenant: string): void {
  bySignal.set(signal, tenant)
}

// Calls fn in tenant's scope: every request it makes at once, and every one made later with `signal`, carries the
// header.
export function runInScope<T>(tenant: string, fn: () => T, signal?: AbortSignal): T {
  if (signal) bySignal.set(signal, tenant)
  const prev = running
  running = tenant
  try {
    return fn()
  } finally {
    running = prev
  }
}

// The scope a request is made in: its signal's, or that of the function making it (null: the person's own).
export function scopeOfRequest(signal?: AbortSignal | null): string | null {
  return (signal ? bySignal.get(signal) : undefined) ?? running
}

// The endpoints that are administration (the server answers the header on these only; anywhere else it is 400).
export function takesScope(apiPath: string): boolean {
  return /^\/(admin|users|invites)(\/|\?|$)/.test(apiPath)
}

// The header a request to apiPath in tenant's scope carries, if any.
export function scopeHeaderFor(apiPath: string, tenant: string | null): Record<string, string> {
  return tenant && takesScope(apiPath) ? { [SCOPE_HEADER]: `tenant:${tenant}` } : {}
}
