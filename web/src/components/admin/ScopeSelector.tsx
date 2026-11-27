import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { Building2, ChevronsUpDown, Globe } from 'lucide-react'
import { adminApi, useTenant } from '@/api/admin'
import { keys, useSession } from '@/api/hooks'
import { acrossSite } from '@/lib/access'
import { adminPath, type AdminSection } from '@/lib/urls'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { topBarTextButton } from '@/components/layout/topBarStyles'
import { tt } from '@/i18n/tenants'
import { useMainClient, useScope } from './TenantScope'

// The console's scope selector (docs/design/3-6-c.md section 7): where the console acts, the whole site or one of the
// tenants the person administers. A choice is an address (`/admin/...` or `/admin/t/<id>/...`), so a reload, a link and
// the back button keep it, and the console in the new scope reads through that scope's own cache.

// The places the person may act in, as the server lists them (GET /api/admin/scopes). It is a request of the site's
// own client, never sent in a scope (the header is for the requests of a scope, and this route answers the person's own).
export function useConsoleScopes() {
  const main = useMainClient()
  return useQuery(
    {
      queryKey: keys.consoleScopes,
      queryFn: ({ signal }) => adminApi.scopes(signal),
      staleTime: 60_000,
      select: (d) => d.scopes,
    },
    main,
  )
}

interface Choice {
  // 'site' or a tenant's id.
  value: string
  name: string
  sub: string
  // The scope of the address it leads to (null: the console's own, `/admin`).
  to: string | null
}

// The choices, and which one the console is in; null when there is nothing to choose or show (a site administrator who
// administers no tenant of their own has only the site).
function useChoices(): { choices: Choice[]; current: string } | null {
  const user = useSession().data?.user
  const site = acrossSite(user)
  const own = user?.tenant?.id
  const scope = useScope()
  const listed = useConsoleScopes().data
  // A tenant the site's administrator entered from the tenant list is not in the list: it is shown too, and is the way
  // back.
  const entered = useTenant(scope).data
  if (!listed) return null
  const choices: Choice[] = listed.flatMap((s) => {
    if (s.kind === 'site')
      return [{ value: 'site', name: tt.scope.selector.site, sub: tt.scope.selector.siteSub, to: null }]
    if (!s.id) return []
    // A tenant administrator's own tenant is the console without a scope; only a site administrator names it.
    const to = !site && s.id === own ? null : s.id
    return [{ value: s.id, name: s.name ?? s.id, sub: tt.scope.selector.tenantSub, to }]
  })
  if (scope !== null && !choices.some((c) => c.value === scope))
    choices.push({ value: scope, name: entered?.name ?? scope, sub: tt.scope.selector.siteSub, to: scope })
  if (choices.length === 0 || (choices.length === 1 && choices[0].value === 'site')) return null
  return { choices, current: scope ?? (site ? 'site' : (own ?? '')) }
}

// The selector: a menu to choose from when there is more than one place, else the one tenant's name. `bar` is the strip
// above the page on a tablet and a desktop; `drawer` is the same control at the top of the phone's menu.
export function ScopeSelector({
  variant,
  active,
  onNavigate,
}: {
  variant: 'bar' | 'drawer'
  active: AdminSection | null
  onNavigate?: () => void
}) {
  const state = useChoices()
  const navigate = useNavigate()
  if (!state) return null
  const { choices, current } = state
  const now = choices.find((c) => c.value === current) ?? choices[0]
  const Icon = now.value === 'site' ? Globe : Building2
  const wrap = (children: React.ReactNode) =>
    variant === 'bar' ? (
      <div
        data-slot="scope-bar"
        className="sticky top-(--banner-h) z-20 hidden h-12 shrink-0 items-center gap-2 border-b bg-background/85 pr-8 pl-8 backdrop-blur md:flex"
      >
        {children}
      </div>
    ) : (
      <div data-slot="scope-drawer" className="border-b px-2 py-2">
        {children}
      </div>
    )

  if (choices.length === 1)
    return wrap(
      <div
        role="group"
        aria-label={tt.scope.selector.label}
        title={now.name}
        className="flex h-9 min-w-0 max-w-full items-center gap-2 px-1 text-sm font-medium"
      >
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate">{now.name}</span>
      </div>,
    )

  return wrap(
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={tt.scope.selector.current(now.name)}
        title={now.name}
        className={`${topBarTextButton} w-full max-w-72 min-w-0 justify-between sm:w-auto`}
      >
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 truncate">{now.name}</span>
        <ChevronsUpDown className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72 max-w-[calc(100vw-1.5rem)]">
        <DropdownMenuRadioGroup
          aria-label={tt.scope.selector.label}
          value={now.value}
          onValueChange={(v) => {
            const to = choices.find((c) => c.value === v)
            if (!to || to.value === now.value) return
            onNavigate?.()
            void navigate(adminPath(active ?? undefined, undefined, to.to))
          }}
        >
          {choices.map((c) => (
            <DropdownMenuRadioItem key={c.value} value={c.value} title={c.name} className="min-h-10">
              <span className="grid min-w-0">
                <span data-slot="scope-name" className="truncate">
                  {c.name}
                </span>
                <span className="truncate text-xs text-muted-foreground">{c.sub}</span>
              </span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>,
  )
}
