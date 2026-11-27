import { useState, useRef } from 'react'
import { scrollFadeClass, useScrollFade } from '@/components/ui/scroll-fade'
import { Link, Navigate } from 'react-router'
import { ArrowLeft, Ban, CircleCheck, Copy, Ellipsis, Eye, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { useUserRow } from '@/api/admin'
import { ApiError } from '@/api/client'
import { useSession } from '@/api/hooks'
import { can, isAdminAccount } from '@/lib/access'
import { useAcrossSite, useAdminPaths } from '../TenantScope'
import { ErrorState, PageFallback } from '@/components/common'
import { FormMessage } from '@/components/auth/Field'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { DeletePersonDialog } from '@/components/admin/manage/DeletePersonDialog'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { AdminPageHeader } from '../AdminPageHeader'
import { Avatar } from './Avatar'
import { PersonAudit } from './PersonAudit'
import { PersonProfile } from './PersonProfile'
import { PersonSecurity } from './PersonSecurity'
import { PersonSessions } from './PersonSessions'
import { PersonSpaces } from './PersonSpaces'
import { PersonUsage } from './PersonUsage'
import { useFeature } from '@/lib/features'
import { useUserChange } from './useUserChange'
import { ViewAsDialog } from './ViewAsDialog'

export const PERSON_TABS = ['profile', 'security', 'sessions', 'spaces', 'usage', 'audit'] as const
export type PersonTab = (typeof PERSON_TABS)[number]
const isTab = (v: unknown): v is PersonTab => PERSON_TABS.some((x) => x === v)

// One person, on a page of their own (their audit rows are long: not a dialog): who they are, how they sign in, where
// they are signed in, where they belong, what they hold, and what the log says about them. The sessions are for the
// administrators of the whole server only.
export function PersonPage({ id, tab }: { id: string; tab?: string }) {
  const { path: adminPath, userPath: adminUserPath } = useAdminPaths()
  const fadeRef = useRef<HTMLElement>(null)
  useScrollFade(fadeRef)
  const session = useSession()
  const me = session.data?.user
  const site = useAcrossSite()
  // The log is a tenant administrator's only while the site gives it (features.tenantAudit).
  const auditFeature = useFeature('features.tenantAudit')
  const auditOff = auditFeature === false && !site
  const row = useUserRow(id)
  const { change, busy, error, messageRef } = useUserChange()
  const [deleting, setDeleting] = useState(false)
  const [viewingAs, setViewingAs] = useState(false)
  const u = row.data?.user
  useDocumentTitle(`${u?.name ?? tc.person.back} · ${tc.title} · ${t.appName}`)

  // Until the signed-in person is known the page waits: nobody is taken for a site administrator (the Sessions tab
  // would flash), and a link to that tab is not sent away before it is known.
  if (session.isPending) return <PageFallback />
  const tabs = PERSON_TABS.filter((x) => (x !== 'sessions' || site) && (x !== 'audit' || !auditOff))
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
  const roleText = isAdminAccount(u) ? t.roles.admin : t.roles.member
  const tenant = u.tenant && u.tenant.slug !== 'default' ? u.tenant.name : ''

  return (
    <div className="min-w-0 space-y-5" data-slot="person-page" data-person={u.id}>
      {back}
      <AdminPageHeader
        title={u.name}
        description={tc.person.summary(u.username, u.email, tenant, roleText)}
        leading={<Avatar name={u.name} size="lg" />}
        meta={
          <>
            {self && <span className="text-xs text-muted-foreground">{tc.users.you}</span>}
            {u.disabled && <Badge variant="secondary">{t.users.disabled}</Badge>}
          </>
        }
        actions={
          <>
            {/* Opens the app as this person sees it, read only and audited, in a tab of its own. Not oneself, and
                only for a holder of the key; the server also refuses an account that holds site permissions to an
                administrator who may not change those. */}
            {!self && can(me, 'tenant.users.viewAs') && (
              <Button
                type="button"
                variant="outline"
                className="h-10"
                disabled={u.disabled}
                onClick={() => setViewingAs(true)}
                aria-label={t.fmt.labelled(tc.person.viewAs, u.name)}
              >
                <Eye aria-hidden="true" />
                {tc.person.viewAs}
              </Button>
            )}
            {!self && (
              <Button
                type="button"
                variant="outline"
                className="h-10"
                disabled={busy === u.id}
                onClick={() => void toggle()}
                aria-label={t.fmt.labelled(u.disabled ? t.users.enable : t.users.disable, u.name)}
              >
                {u.disabled ? <CircleCheck aria-hidden="true" /> : <Ban aria-hidden="true" />}
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
                  <Copy />
                  {tc.users.copyUsername}
                </DropdownMenuItem>
                {!self && (
                  <DropdownMenuItem className="min-h-10 text-destructive" onClick={() => setDeleting(true)}>
                    <Trash2 />
                    {tc.users.delete}
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      >
        <FormMessage focusRef={messageRef}>{error ? error.message : ''}</FormMessage>
        <nav ref={fadeRef} aria-label={tc.person.tabsLabel} className={`${scrollFadeClass} -mx-4 px-4 md:mx-0 md:px-0`}>
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
          {active === 'security' && (
            <PersonSecurity u={u} busy={busy === u.id} save={(p) => change(u, p)} site={site} />
          )}
          {active === 'sessions' && <PersonSessions u={u} />}
          {active === 'spaces' && <PersonSpaces u={u} />}
          {active === 'usage' && <PersonUsage u={u} />}
          {active === 'audit' && <PersonAudit u={u} />}
        </div>
        {viewingAs && <ViewAsDialog user={u.id} name={u.name} onClose={() => setViewingAs(false)} />}
        {deleting && <DeletePersonDialog id={u.id} username={u.username} onClose={() => setDeleting(false)} />}
      </AdminPageHeader>
    </div>
  )
}
