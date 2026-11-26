import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { Ellipsis, Eye, Search, Trash2 } from 'lucide-react'
import { usePeoplePage } from '@/api/manage'
import type { ConfirmAction, PeopleParams, PeopleSort, StoragePerson } from '@/api/types'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Badge } from '@/components/ui/badge'
import { DataTable, RowAction, type Column } from '@/components/ui/data-table'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { adminUserPath } from '@/lib/urls'
import { useCursorPages } from '@/lib/useCursorPages'
import { useDebounced } from '@/lib/useDebounced'
import { useTablePrefs } from '@/lib/tablePrefs'
import { t } from '@/i18n'
import { tc } from '@/i18n/console'
import { tg } from '@/i18n/manage'
import { Avatar } from '../users/Avatar'
import { BulkBar } from './BulkBar'
import { BulkDialog } from './BulkDialog'
import { DeletePersonDialog } from './DeletePersonDialog'

type Filter = '' | 'dormant' | 'some' | 'none' | 'grace' | 'overdue' | 'inactive' | 'disabled'
const SORTS: readonly PeopleSort[] = ['held', 'bytes', 'lastActive', 'created', 'spaces']
const isSort = (v: string): v is PeopleSort => (SORTS as readonly string[]).includes(v)
const INACTIVE_DAYS = 90

function paramsOf(filter: Filter): Partial<PeopleParams> {
  switch (filter) {
    case 'dormant':
      return { dormant: true }
    case 'some':
    case 'none':
      return { remote: filter }
    case 'grace':
    case 'overdue':
      return { deadline: filter }
    case 'inactive':
      return { inactiveDays: INACTIVE_DAYS }
    case 'disabled':
      return { disabled: true }
    default:
      return {}
  }
}

// Every person and what they hold, a page at a time, in the order asked for (a click on a header). The numbers are
// stored measurements; the person's own page has the rest.
export function PeopleSection() {
  const prefs = useTablePrefs('storage-people')
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const [filter, setFilter] = useState<Filter>('')
  const [sort, setSort] = useState<{ key: PeopleSort; order: 'asc' | 'desc' }>({ key: 'held', order: 'desc' })
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState<ConfirmAction | null>(null)
  const [deleting, setDeleting] = useState<StoragePerson | null>(null)
  const params: PeopleParams = {
    q: q || undefined,
    sort: sort.key,
    order: sort.order,
    limit: prefs.prefs.pageSize,
    ...paramsOf(filter),
  }
  const pages = useCursorPages(JSON.stringify(params))
  const list = usePeoplePage({ ...params, cursor: pages.cursor || undefined })
  const rows = list.data?.items ?? []
  const next = list.data?.next ?? ''

  const columns = useMemo<Column<StoragePerson>[]>(
    () => [
      {
        id: 'name',
        header: tg.people.name,
        required: true,
        min: 180,
        weight: 3,
        cell: (p) => (
          <div className="flex min-w-0 items-center gap-3">
            <Avatar name={p.user.name} />
            <div className="min-w-0">
              <div className="truncate font-medium">{p.user.name}</div>
              <div className="truncate text-xs text-muted-foreground">{p.user.username}</div>
            </div>
          </div>
        ),
      },
      {
        id: 'spaces',
        header: tg.people.spaces,
        min: 80,
        weight: 1,
        priority: 3,
        sortKey: 'spaces',
        align: 'right',
        cell: (p) => p.spaces,
      },
      {
        id: 'usage',
        header: tg.people.usage,
        min: 110,
        weight: 1.3,
        priority: 2,
        sortKey: 'bytes',
        align: 'right',
        cell: (p) => formatBytes(p.usageBytes),
      },
      {
        id: 'held',
        header: tg.people.held,
        min: 120,
        weight: 1.3,
        priority: 2,
        sortKey: 'held',
        align: 'right',
        cell: (p) => formatBytes(p.heldBytes),
      },
      {
        id: 'lastActive',
        header: tg.people.lastActive,
        min: 140,
        weight: 1.5,
        priority: 4,
        sortKey: 'lastActive',
        cell: (p) =>
          p.lastActive ? (
            <time dateTime={p.lastActive}>{absoluteTime(p.lastActive)}</time>
          ) : (
            <span className="text-muted-foreground">{tg.people.never}</span>
          ),
      },
      {
        id: 'created',
        header: tc.users.cols.created,
        min: 140,
        weight: 1.5,
        priority: 6,
        sortKey: 'created',
        cell: (p) => <time dateTime={p.created}>{absoluteTime(p.created)}</time>,
      },
      {
        id: 'status',
        header: tg.people.status,
        min: 110,
        weight: 1.2,
        priority: 5,
        cell: (p) => (
          <div className="flex flex-wrap gap-1">
            {p.dormant && <Badge variant="outline">{tg.people.isDormant}</Badge>}
            {p.user.disabled && <Badge variant="secondary">{tg.people.isDisabled}</Badge>}
          </div>
        ),
      },
    ],
    [],
  )

  return (
    <SettingsSection title={tg.people.title} id="manage-people-heading" description={tg.people.help}>
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full max-w-xs min-w-0">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={tg.people.searchPlaceholder}
            aria-label={tg.people.search}
            className="h-10 pl-8"
            autoComplete="off"
          />
        </div>
        <div className="w-56 max-sm:w-full">
          <NativeSelect
            className="h-10"
            aria-label={tg.people.filter}
            value={filter}
            onChange={(e) => setFilter(e.target.value as Filter)}
          >
            <option value="">{tg.people.all}</option>
            <option value="dormant">{tg.people.dormant}</option>
            <option value="some">{tg.people.withRemote}</option>
            <option value="none">{tg.people.noRemote}</option>
            <option value="grace">{tg.people.grace}</option>
            <option value="overdue">{tg.people.overdue}</option>
            <option value="inactive">{tg.people.inactive(INACTIVE_DAYS)}</option>
            <option value="disabled">{tg.people.disabled}</option>
          </NativeSelect>
        </div>
      </div>
      <DataTable
        prefs={prefs}
        columns={columns}
        rows={rows}
        rowId={(p) => p.user.id}
        rowLabel={(p) => p.user.name}
        rowAttrs={(p) => ({ 'data-person': p.user.id })}
        loading={list.isPending}
        fetching={list.isFetching && !list.isPending}
        error={list.isError ? tg.people.loadFailed : undefined}
        onRetry={() => void list.refetch()}
        empty={q ? tg.people.noMatch(q) : tg.people.empty}
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
        actions={(p) => (
          <>
            <RowAction
              icon={<Eye aria-hidden="true" />}
              label={tc.table.view}
              ariaLabel={tg.people.openFor(p.user.name)}
              render={<Link to={adminUserPath(p.user.id, 'usage')} />}
            />
            <DropdownMenu>
              <DropdownMenuTrigger
                aria-label={t.fmt.labelled(tc.table.more, p.user.name)}
                className="inline-flex size-10 items-center justify-center rounded-md text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-muted"
              >
                <Ellipsis className="size-4" aria-hidden="true" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-44">
                <DropdownMenuItem className="min-h-10 text-destructive" onClick={() => setDeleting(p)}>
                  <Trash2 />
                  {tg.people.deletePerson}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        )}
      />
      {deleting && (
        <DeletePersonDialog id={deleting.user.id} username={deleting.user.username} onClose={() => setDeleting(null)} />
      )}
      {bulk && (
        <BulkDialog
          action={bulk}
          label={tg.bulk.actions[bulk]}
          request={{ users: [...picked] }}
          onClose={() => {
            setBulk(null)
            setPicked(new Set())
          }}
        />
      )}
    </SettingsSection>
  )
}
