import { useState } from 'react'
import { Link, Navigate } from 'react-router'
import { ArrowLeft, Ellipsis } from 'lucide-react'
import { toast } from 'sonner'
import { useUserRow } from '@/api/admin'
import { ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import { ErrorState, PageFallback } from '@/components/common'
import { FormMessage } from '@/components/auth/Field'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { DeletePersonDialog } from '@/components/admin/manage/DeletePersonDialog'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { adminPath, adminUserPath } from '@/lib/urls'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { Avatar } from './Avatar'
import { PersonAudit } from './PersonAudit'
import { PersonProfile } from './PersonProfile'
import { PersonSecurity } from './PersonSecurity'
import { PersonSessions } from './PersonSessions'
import { PersonSpaces } from './PersonSpaces'
import { PersonUsage } from './PersonUsage'
import { useUserChange } from './useUserChange'

export const PERSON_TABS = ['profile', 'security', 'sessions', 'spaces', 'usage', 'audit'] as const
export type PersonTab = (typeof PERSON_TABS)[number]
const isTab = (v: unknown): v is PersonTab => PERSON_TABS.some((x) => x === v)

// One person, on a page of their own (their audit rows are long: not a dialog): who they are, how they sign in, where
// they are signed in, where they belong, what they hold, and what the log says about them. The sessions are for the
// administrators of the whole server only.
export function PersonPage({ id, tab }: { id: string; tab?: string }) {
  const me = useSession().data?.user
  const site = me?.siteAdmin !== false
  const row = useUserRow(id)
  const { change, busy, error, messageRef } = useUserChange()
  const [deleting, setDeleting] = useState(false)
  const u = row.data?.user
  useDocumentTitle(`${u?.name ?? tc.person.back} · ${tc.title} · ${t.appName}`)

  const tabs = PERSON_TABS.filter((x) => x !== 'sessions' || site)
  if (tab !== undefined && !isTab(tab)) return <Navigate to={adminUserPath(id)} replace />
  const active: PersonTab = tab && tabs.includes(tab) ? tab : 'profile'
  if (tab === 'sessions' && !site) return <Navigate to={adminUserPath(id)} replace />

  const back = (
    <Link
      to={adminPath('users', 'all')}
      className="inline-flex min-h-10 items-center gap-1.5 text-sm text-muted-foreground underline-offset-4 outline-none hover:text-foreground hover:underline focus-visible:underline"
    >
      <ArrowLeft className="size-4" aria-hidden="true" />
      {tc.person.back}
    </Link>
  )
  if (row.isPending) return <PageFallback />
  if (row.isError || !u)
    return (
      <div className="space-y-4">
        {back}
        {row.error instanceof ApiError && row.error.status === 404 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{tc.person.notFound}</p>
        ) : (
          <ErrorState message={tc.person.loadFailed} onRetry={() => void row.refetch()} />
        )}
      </div>
    )

  const self = u.id === me?.id
  const toggle = async () => {
    const done = await change(u, { disabled: !u.disabled })
    if (done) toast.success(done.disabled ? t.users.disable : t.users.enable)
  }
  const roleText = u.role === 'admin' ? t.roles.admin : t.roles.member
  const tenant = u.tenant && u.tenant.slug !== 'default' ? u.tenant.name : ''

  return (
    <div className="min-w-0 space-y-5" data-slot="person-page" data-person={u.id}>
      {back}
      <header className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-3">
        <Avatar name={u.name} size="lg" />
        <div className="min-w-0 flex-1 basis-56">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h2 className="truncate text-xl font-semibold">{u.name}</h2>
            {self && <span className="text-xs text-muted-foreground">{tc.users.you}</span>}
            {u.disabled && <Badge variant="secondary">{t.users.disabled}</Badge>}
          </div>
          <p className="truncate text-sm text-muted-foreground" data-slot="person-summary">
            {tc.person.summary(u.username, u.email, tenant, roleText)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            className="h-10"
            disabled
            title={tc.person.viewAsSoon}
            aria-describedby="view-as-note"
          >
            {tc.person.viewAs}
          </Button>
          <span id="view-as-note" className="sr-only">
            {tc.person.viewAsSoon}
          </span>
          {!self && (
            <Button
              type="button"
              variant="outline"
              className="h-10"
              disabled={busy === u.id}
              onClick={() => void toggle()}
              aria-label={t.fmt.labelled(u.disabled ? t.users.enable : t.users.disable, u.name)}
            >
              {u.disabled ? t.users.enable : t.users.disable}
            </Button>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label={t.fmt.labelled(tc.person.more, u.name)}
              className="inline-flex size-10 items-center justify-center rounded-lg border text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-muted"
            >
              <Ellipsis className="size-4" aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-44">
              <DropdownMenuItem
                className="min-h-10"
                onClick={() => {
                  void navigator.clipboard?.writeText(u.username).catch(() => undefined)
                }}
              >
                {tc.users.copyUsername}
              </DropdownMenuItem>
              {!self && (
                <DropdownMenuItem className="min-h-10 text-destructive" onClick={() => setDeleting(true)}>
                  {tc.users.delete}
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </header>
      <FormMessage focusRef={messageRef}>{error ? error.message : ''}</FormMessage>
      <nav aria-label={tc.person.tabsLabel} className="-mx-4 overflow-x-auto px-4 md:mx-0 md:px-0">
        <ul className="flex min-w-max gap-1 border-b">
          {tabs.map((x) => (
            <li key={x}>
              <Link
                to={adminUserPath(u.id, x === 'profile' ? undefined : x)}
                aria-current={x === active ? 'page' : undefined}
                className={`relative flex h-11 items-center px-3 text-sm whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 ${x === active ? 'font-medium text-foreground after:absolute after:inset-x-2 after:bottom-0 after:h-0.5 after:rounded-full after:bg-brand' : 'text-muted-foreground hover:text-foreground'}`}
              >
                {tc.person.tabs[x]}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <div className="min-w-0 animate-in duration-150 fade-in-0" key={active}>
        {active === 'profile' && <PersonProfile u={u} busy={busy === u.id} save={(p) => change(u, p)} site={site} />}
        {active === 'security' && <PersonSecurity u={u} busy={busy === u.id} save={(p) => change(u, p)} site={site} />}
        {active === 'sessions' && <PersonSessions u={u} />}
        {active === 'spaces' && <PersonSpaces u={u} />}
        {active === 'usage' && <PersonUsage u={u} />}
        {active === 'audit' && <PersonAudit u={u} />}
      </div>
      {deleting && <DeletePersonDialog id={u.id} username={u.username} onClose={() => setDeleting(false)} />}
    </div>
  )
}
