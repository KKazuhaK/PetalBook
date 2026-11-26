// The console's acting scope: the whole site, or one tenant. A site administrator managing a tenant acts as that
// tenant's administrator (the request header PetalBook-Scope: tenant:<id>, docs/design/3-6-c.md section 5). The scope
// is in the address, so it is per browser tab and survives a reload: `/admin/t/<id>/...` is the console in the tenant's
// scope. The members, spaces and audit tabs of a tenant's own page read in the same scope. The component that shows a
// page in a scope (TenantScope) tells this module while it renders, before anything under it asks the server; the API
// client reads it here and nowhere else.

export const SCOPE_HEADER = 'PetalBook-Scope'

let current: string | null = null

// Set by the console as it renders: the tenant whose scope the page is in, null for the whole site.
export function setScopeNow(tenant: string | null): void {
  current = tenant
}

// What the page being shown is in the scope of.
export const scopeNow = (): string | null => current

// The endpoints that are administration (the server answers the header on these only; anywhere else it is 400).
export function takesScope(apiPath: string): boolean {
  return /^\/(admin|users|invites)(\/|\?|$)/.test(apiPath)
}

// The header a request carries now, if any.
export function scopeHeaderFor(apiPath: string): Record<string, string> {
  const tenant = takesScope(apiPath) ? current : null
  return tenant ? { [SCOPE_HEADER]: `tenant:${tenant}` } : {}
}
