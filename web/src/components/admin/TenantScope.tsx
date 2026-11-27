import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { Link } from 'react-router'
import { QueryClientProvider, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { LogOut, UserCog } from 'lucide-react'
import { keys, useSession } from '@/api/hooks'
import { acrossSite } from '@/lib/access'
import { scopedClient } from '@/lib/scopedClient'
import { adminPath, adminTenantPath, adminUserPath, type AdminSection } from '@/lib/urls'
import { Button } from '@/components/ui/button'
import { tt } from '@/i18n/tenants'

// The console's acting scope (docs/design/3-6-c.md section 5): the whole site (null), or one tenant, in which a site
// administrator acts as that tenant's administrator. Everything under a TenantScope reads and writes through the
// tenant's own QueryClient, whose queries and mutations carry the scope header (lib/adminScope), shows no site-only
// page, builds its links inside the scope, and keeps its own copy of what the server answered, so a list read in one
// scope is never shown in another. What is above it (the tenant's page around its scoped tabs, the console's frame)
// reads through the site's client and never carries the header.

const ScopeContext = createContext<string | null>(null)

// The site's own client, which a tenant's scope sits under: what the console's frame reads without the scope (the
// selector's list of scopes).
const MainClientContext = createContext<QueryClient | null>(null)
export function useMainClient(): QueryClient {
  const here = useQueryClient()
  return useContext(MainClientContext) ?? here
}

// The tenant whose scope the page is in, null for the whole site.
export const useScope = (): string | null => useContext(ScopeContext)

// The console's addresses, inside the scope the page is in.
export function useAdminPaths() {
  const scope = useScope()
  return useMemo(
    () => ({
      scope,
      path: (section?: AdminSection, tab?: string) => adminPath(section, tab, scope),
      userPath: (id: string, tab?: string) => adminUserPath(id, tab, scope),
    }),
    [scope],
  )
}

// Whether the console acts across the whole site: a site administrator, and not inside a tenant's scope.
export function useAcrossSite(): boolean {
  const scope = useScope()
  return acrossSite(useSession().data?.user) && scope === null
}

// Shows its children in a tenant's scope (or, with null, the whole site's): under the tenant's QueryClient, one for
// each tenant (the tabs of its page share it). A write made in it has the site's client read the tenants again.
export function TenantScope({ tenant, children }: { tenant: string | null; children: ReactNode }) {
  const main = useQueryClient()
  const who = useSession().data?.user?.id ?? null
  const client = useMemo(() => {
    if (tenant === null) return main
    const c = scopedClient(main, tenant, who, keys.tenants)
    const session = main.getQueryData(keys.session)
    if (session && c.getQueryData(keys.session) === undefined) c.setQueryData(keys.session, session)
    return c
  }, [main, tenant, who])
  return (
    <MainClientContext.Provider value={main}>
      <ScopeContext.Provider value={tenant}>
        {client === main ? children : <QueryClientProvider client={client}>{children}</QueryClientProvider>}
      </ScopeContext.Provider>
    </MainClientContext.Provider>
  )
}

// The one way into a tenant's console as its administrator. Entering asks for no reason (it opens no content, and every
// write in it is audited with the capacity site_admin); if the owner decides it should, the dialog goes here and
// nowhere else.
export function ManageAsButton({
  tenant,
  size,
  variant,
}: {
  tenant: string
  size?: 'sm' | 'default'
  variant?: 'default' | 'outline'
}) {
  return (
    <Button
      render={<Link to={adminPath('users', 'all', tenant)} />}
      nativeButton={false}
      size={size}
      variant={variant}
      className={size === 'sm' ? 'h-10 lg:h-8' : undefined}
    >
      <UserCog aria-hidden="true" />
      {tt.scope.manageAs}
    </Button>
  )
}

// The bar that says the console is in a tenant's scope and leads out of it, back to the tenant's page. It is the first
// thing in the page, above the top bar and the sidebar, and stays while the page scrolls.
export function ScopeBanner({ tenantId, name }: { tenantId: string; name: string | undefined }) {
  return (
    <div
      role="region"
      aria-label={tt.scope.bannerLabel}
      data-slot="scope-banner"
      className="sticky top-0 z-40 flex h-(--banner-h) shrink-0 items-center gap-2 border-b border-brand/30 bg-brand/10 pt-[env(safe-area-inset-top)] pr-[max(0.75rem,env(safe-area-inset-right))] pl-[max(0.75rem,env(safe-area-inset-left))] text-sm"
    >
      <UserCog className="size-4 shrink-0 text-brand" aria-hidden="true" />
      <p className="min-w-0 flex-1 truncate">{name ? tt.scope.managing(name) : tt.scope.managingLoading}</p>
      <Button
        render={<Link to={adminTenantPath(tenantId)} />}
        nativeButton={false}
        variant="outline"
        size="sm"
        className="h-9 shrink-0 bg-background pointer-coarse:h-11 pointer-coarse:min-w-11"
        aria-label={tt.scope.exit}
      >
        <LogOut aria-hidden="true" />
        <span className="max-sm:sr-only">{tt.scope.exit}</span>
      </Button>
    </div>
  )
}
