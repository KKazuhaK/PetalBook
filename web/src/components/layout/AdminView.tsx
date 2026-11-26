import { Link } from 'react-router'
import { Eye, ShieldAlert } from 'lucide-react'
import { manageApi } from '@/api/manage'
import { useQueryClient } from '@tanstack/react-query'
import { keys, useSession } from '@/api/hooks'
import type { SpaceInfo } from '@/api/types'
import { tapTarget } from '@/components/admin/manage/shared'
import { ViewForm } from '@/components/admin/manage/ViewForm'
import { TopBar } from '@/components/layout/TopBar'
import { Button } from '@/components/ui/button'
import { openView } from '@/lib/adminView'
import { acrossSite } from '@/lib/access'
import { useFeature } from '@/lib/features'
import { clockTime } from '@/lib/time'
import { adminPath } from '@/lib/urls'
import { tg } from '@/i18n/manage'

// Shown in place of a space an administrator manages without being a member of it: reading takes an audited view.
export function AdminViewGate({ space }: { space: SpaceInfo }) {
  // The view is the feature features.adminView (a site administrator is not bound by it): without it there is nothing to
  // open, and the page says so instead of showing a form the server would refuse.
  const user = useSession().data?.user
  const viewing = useFeature('features.adminView')
  const allowed = viewing !== false || acrossSite(user)
  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{space.name}</h1>
      </TopBar>
      <div className="mx-auto flex max-w-md flex-col gap-4 px-5 py-16" data-slot="admin-view-gate">
        <div className="flex flex-col items-center gap-2 text-center">
          <ShieldAlert aria-hidden="true" className="size-8 text-muted-foreground" strokeWidth={1.5} />
          <h2 className="text-base font-medium">{tg.view.gateTitle}</h2>
          <p className="text-sm text-muted-foreground">{allowed ? tg.view.gateHelp : tg.view.gateOff}</p>
        </div>
        {allowed && <ViewForm space={space.id} name={space.name} onStarted={() => undefined} />}
        <Link
          to={adminPath('spaces')}
          className="text-center text-sm text-muted-foreground underline underline-offset-4 hover:text-foreground"
        >
          {tg.view.back}
        </Link>
      </div>
    </>
  )
}

// While a view is open, on every page of the space: it is read only and it is recorded.
export function AdminViewBanner({ space }: { space: SpaceInfo }) {
  const qc = useQueryClient()
  const until = openView(space.view)?.until
  const end = async () => {
    try {
      await manageApi.endView(space.id)
    } finally {
      void qc.invalidateQueries({ queryKey: keys.viewedSpace(space.id) })
    }
  }
  return (
    <div
      role="status"
      data-slot="admin-view-banner"
      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-sm text-amber-900 dark:text-amber-200"
    >
      <Eye aria-hidden className="size-4 shrink-0" />
      <span className="font-medium">{tg.view.banner}</span>
      {until && <span className="text-xs">{tg.view.until(clockTime(new Date(until)))}</span>}
      <Button type="button" variant="ghost" size="xs" className={`ml-auto ${tapTarget}`} onClick={() => void end()}>
        {tg.view.end}
      </Button>
    </div>
  )
}
