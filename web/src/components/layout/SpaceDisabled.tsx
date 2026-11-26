import { Link } from 'react-router'
import { Ban } from 'lucide-react'
import { useActiveSpace, useSession } from '@/api/hooks'
import { can } from '@/lib/access'
import type { SpaceInfo } from '@/api/types'
import { TopBar } from '@/components/layout/TopBar'
import { adminPath, spaceUrls } from '@/lib/urls'
import { t } from '@/i18n'

// What opening a space that an administrator switched off shows, in place of
// the page: the server refuses everything in it, so nothing is asked for.
export function SpaceDisabled({ space }: { space: SpaceInfo }) {
  const session = useSession().data
  const { spaces } = useActiveSpace()
  const admin = session?.mode === 'accounts' && can(session.user, 'tenant.spaces.manage')
  const other = spaces.find((s) => s.id !== space.id && !s.disabled)
  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{space.name}</h1>
      </TopBar>
      <div role="status" className="mx-auto flex max-w-md flex-col items-center gap-3 px-5 py-24 text-center">
        <Ban aria-hidden="true" className="size-8 text-muted-foreground" strokeWidth={1.5} />
        <h2 className="text-base font-medium">{t.spaces.disabledTitle}</h2>
        <p className="text-sm text-muted-foreground">{admin ? t.spaces.disabledAdminHelp : t.spaces.disabledHelp}</p>
        <div className="flex flex-wrap justify-center gap-x-4 gap-y-1">
          {admin && (
            <Link to={adminPath('spaces')} className="text-sm text-brand underline underline-offset-4">
              {t.spaces.disabledAdminLink}
            </Link>
          )}
          {other && (
            <Link to={spaceUrls(other.id).home()} className="text-sm text-brand underline underline-offset-4">
              {t.spaces.disabledBack}
            </Link>
          )}
        </div>
      </div>
    </>
  )
}
