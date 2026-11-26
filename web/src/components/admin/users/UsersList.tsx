import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { Ban, CircleCheck, Ellipsis, Eye, Search, UserPlus, Trash2 } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { useUsersPage } from '@/api/admin'
import { keys, useSession } from '@/api/hooks'
import type { ConfirmAction, User, UserSort, UsersParams } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DataTable, type Column } from '@/components/ui/data-table'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { BulkBar } from '@/components/admin/manage/BulkBar'
import { BulkDialog } from '@/components/admin/manage/BulkDialog'
import { DeletePersonDialog } from '@/components/admin/manage/DeletePersonDialog'
import { absoluteTime } from '@/lib/time'
import { adminPath, adminUserPath } from '@/lib/urls'
import { useCursorPages } from '@/lib/useCursorPages'
import { useDebounced } from '@/lib/useDebounced'
import { acrossSite, adminKindOf, isAdminAccount } from '@/lib/access'
import { useTablePrefs } from '@/lib/tablePrefs'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { tg } from '@/i18n/manage'
import { AdminPageActions } from '../AdminPageHeader'
import { Avatar } from './Avatar'
import { useUserChange } from './useUserChange'

const INACTIVE_DAYS = 90
const ACTIVE_DAYS = 30
const SORTS: readonly UserSort[] = ['created', 'name', 'lastActive']
const isSort = (v: string): v is UserSort => (SORTS as readonly string[]).includes(v)

// Everyone the administrator may see, a page at a time: the server searches, filters, sorts and cuts the page, and the
// table keeps which columns, how wide and how many to a page in this browser.
export function UsersList() {
  const me = useSession().data?.user
  const navigate = useNavigate()
  const qc = useQueryClient()
  const prefs = useTablePrefs('users')
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const [role, setRole] = useState<'' | NonNullable<UsersParams['role']>>('')
  const [state, setState] = useState<'' | NonNullable<UsersParams['state']>>('')
  const [active, setActive] = useState<'' | 'inactive' | 'recent'>('')
  const [sort, setSort] = useState<{ key: UserSort; order: 'asc' | 'desc' }>({ key: 'created', order: 'asc' })
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState<ConfirmAction | null>(null)
  const [deleting, setDeleting] = useState<User | null>(null)
  const { change, busy, error, messageRef } = useUserChange()

  const params: UsersParams = {
    q: q || undefined,
    role: role || undefined,
    state: state || undefined,
    inactiveDays: active === 'inactive' ? INACTIVE_DAYS : undefined,
    activeDays: active === 'recent' ? ACTIVE_DAYS : undefined,
    sort: sort.key,
    order: sort.order,
    limit: prefs.prefs.pageSize,
  }
  const pages = useCursorPages(JSON.stringify(params))
  const list = useUsersPage({ ...params, cursor: pages.cursor || undefined })
  const rows = list.data?.users ?? []
  const next = list.data?.next ?? ''
  const site = acrossSite(me)

  const columns = useMemo<Column<User>[]>(
    () => [
      {
        id: 'user',
        header: tc.users.cols.user,
        required: true,
        min: 180,
        weight: 3,
        sortKey: 'name',
        cell: (u) => (
          <div className="flex min-w-0 items-center gap-3">
            <Avatar name={u.name} />
            <div className="min-w-0">
              <div className="flex min-w-0 items-center gap-1.5">
                <Link
                  to={adminUserPath(u.id)}
                  className="-my-2.5 truncate py-2.5 font-medium underline-offset-4 outline-none hover:underline focus-visible:underline"
                >
                  {u.name}
                </Link>
                {u.id === me?.id && <span className="shrink-0 text-xs text-muted-foreground">{tc.users.you}</span>}
              </div>
              <div className="truncate text-xs text-muted-foreground">{u.username}</div>
            </div>
          </div>
        ),
      },
      {
        id: 'role',
        header: tc.users.cols.role,
        min: 100,
        weight: 1.2,
        priority: 2,
        cell: (u) => (
          <span className="flex items-center gap-1.5">
            <span className="min-w-0 truncate">{isAdminAccount(u) ? t.roles.admin : t.roles.member}</span>
            {isAdminAccount(u) && u.siteAdmin !== undefined && (
              <Badge variant="outline" data-slot="admin-kind">
                {adminKindOf(u) === 'site' ? tc.users.adminKinds.site : tc.users.adminKinds.tenant}
              </Badge>
            )}
          </span>
        ),
      },
      {
        id: 'tenant',
        header: tc.users.cols.tenant,
        min: 100,
        weight: 1.2,
        priority: 6,
        cell: (u) => (u.tenant ? u.tenant.name : ''),
      },
      {
        id: 'state',
        header: tc.users.cols.state,
        min: 80,
        weight: 1,
        priority: 2,
        cell: (u) =>
          u.disabled ? (
            <Badge variant="secondary">{tc.users.stateDisabled}</Badge>
          ) : (
            <span className="text-muted-foreground">{tc.users.stateEnabled}</span>
          ),
      },
      {
        id: 'email',
        header: tc.users.cols.email,
        min: 150,
        weight: 2,
        priority: 4,
        cell: (u) => u.email,
      },
      {
        id: 'lastActive',
        header: tc.users.cols.lastActive,
        min: 165,
        weight: 1.5,
        priority: 3,
        sortKey: 'lastActive',
        cell: (u) =>
          u.lastActive ? (
            <time dateTime={u.lastActive}>{absoluteTime(u.lastActive)}</time>
          ) : (
            <span className="text-muted-foreground">{tc.users.never}</span>
          ),
      },
      {
        id: 'signIn',
        header: tc.users.cols.signIn,
        min: 130,
        weight: 1.5,
        priority: 6,
        cell: (u) => (
          <span className="text-xs text-muted-foreground" data-slot="user-counts">
            {tc.users.signInCounts(u.passkeys ?? 0, u.sso ?? 0)}
          </span>
        ),
      },
      {
        id: 'created',
        header: tc.users.cols.created,
        min: 165,
        weight: 1.5,
        priority: 7,
        sortKey: 'created',
        cell: (u) => <time dateTime={u.created}>{absoluteTime(u.created)}</time>,
      },
    ],
    [me?.id],
  )
  // The tenant is the same for everyone in a single-tenant server: a column of one value is left to those who ask.
  const shownColumns = useMemo(() => (site ? columns : columns.filter((c) => c.id !== 'tenant')), [columns, site])

  const toggle = async (u: User) => {
    const done = await change(u, { disabled: !u.disabled })
    if (done) toast.success(done.disabled ? tc.users.disable : tc.users.enable)
  }

  return (
    <div className="min-w-0 space-y-4">
      <div className="flex flex-wrap items-center gap-2" role="search">
        <div className="relative w-full max-w-[15rem] min-w-0 max-sm:max-w-none">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={tc.users.searchPlaceholder}
            aria-label={tc.users.search}
            className="h-10 pl-8"
            autoComplete="off"
          />
        </div>
        <div className="w-36 max-sm:w-[calc(50%-0.25rem)]">
          <NativeSelect
            className="h-10"
            aria-label={tc.users.role}
            value={role}
            onChange={(e) => setRole(e.target.value as typeof role)}
          >
            <option value="">{tc.users.roleAll}</option>
            <option value="member">{tc.users.roleMember}</option>
            <option value="admin">{tc.users.roleAdmin}</option>
            {site && <option value="site">{tc.users.roleSite}</option>}
          </NativeSelect>
        </div>
        <div className="w-36 max-sm:w-[calc(50%-0.25rem)]">
          <NativeSelect
            className="h-10"
            aria-label={tc.users.state}
            value={state}
            onChange={(e) => setState(e.target.value as typeof state)}
          >
            <option value="">{tc.users.stateAll}</option>
            <option value="enabled">{tc.users.stateEnabled}</option>
            <option value="disabled">{tc.users.stateDisabled}</option>
          </NativeSelect>
        </div>
        <div className="w-44 max-sm:w-full">
          <NativeSelect
            className="h-10"
            aria-label={tc.users.active}
            value={active}
            onChange={(e) => setActive(e.target.value as typeof active)}
          >
            <option value="">{tc.users.activeAll}</option>
            <option value="recent">{tc.users.activeWithin(ACTIVE_DAYS)}</option>
            <option value="inactive">{tc.users.inactive(INACTIVE_DAYS)}</option>
          </NativeSelect>
        </div>
        <AdminPageActions>
          <Button className="h-10" render={<Link to={adminPath('users', 'invites')} />} nativeButton={false}>
            <UserPlus aria-hidden="true" />
            {tc.users.invite}
          </Button>
        </AdminPageActions>
      </div>
      <FormMessage focusRef={messageRef}>{error ? error.message : ''}</FormMessage>
      <DataTable
        prefs={prefs}
        columns={shownColumns}
        rows={rows}
        rowId={(u) => u.id}
        rowLabel={(u) => u.name}
        rowAttrs={(u) => ({ 'data-user': u.id })}
        loading={list.isPending}
        fetching={list.isFetching && !list.isPending}
        error={list.isError ? t.fmt.labelled(t.users.loadFailed, list.error.message) : undefined}
        onRetry={() => void list.refetch()}
        empty={q || role || state || active ? tc.users.noMatch : tc.users.empty}
        paging={{
          page: pages.page,
          hasPrev: pages.hasPrev,
          hasNext: next !== '',
          onPrev: pages.prev,
          onNext: () => pages.next(next),
        }}
        sort={sort}
        onSort={(s) => isSort(s.key) && setSort({ key: s.key, order: s.order })}
        selection={{ selected: picked, onChange: setPicked }}
        bulk={
          <BulkBar
            count={picked.size}
            actions={['delete_users', 'disable_users', 'enable_users']}
            onAction={setBulk}
            onClear={() => setPicked(new Set())}
          />
        }
        actions={(u) => (
          <>
            <Button
              variant="ghost"
              className="h-10 px-3"
              render={<Link to={adminUserPath(u.id)} aria-label={t.fmt.labelled(tc.table.view, u.name)} />}
              nativeButton={false}
            >
              <Eye aria-hidden="true" />
              {tc.table.view}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger
                aria-label={t.fmt.labelled(tc.table.more, u.name)}
                className="inline-flex size-10 items-center justify-center rounded-md text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-muted"
              >
                <Ellipsis className="size-4" aria-hidden="true" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-44">
                <DropdownMenuItem className="min-h-10" onClick={() => navigate(adminUserPath(u.id))}>
                  <Eye />
                  {tc.table.view}
                </DropdownMenuItem>
                <DropdownMenuItem className="min-h-10" disabled={busy === u.id} onClick={() => void toggle(u)}>
                  {u.disabled ? <CircleCheck /> : <Ban />}
                  {u.disabled ? tc.users.enable : tc.users.disable}
                </DropdownMenuItem>
                {u.id !== me?.id && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem className="min-h-10 text-destructive" onClick={() => setDeleting(u)}>
                      <Trash2 />
                      {tc.users.delete}
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        )}
      />
      {deleting && (
        <DeletePersonDialog id={deleting.id} username={deleting.username} onClose={() => setDeleting(null)} />
      )}
      {bulk && (
        <BulkDialog
          action={bulk}
          label={tg.bulk.actions[bulk]}
          request={{ users: [...picked] }}
          onClose={() => {
            setBulk(null)
            setPicked(new Set())
            void qc.invalidateQueries({ queryKey: keys.users })
          }}
        />
      )}
    </div>
  )
}
