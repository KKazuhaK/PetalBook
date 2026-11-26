import { useEffect, useState } from 'react'
import { Eye, LogOut } from 'lucide-react'
import { manageApi } from '@/api/manage'
import type { ViewAsInfo } from '@/api/types'
import { Button } from '@/components/ui/button'
import { forgetViewAs, minutesLeft } from '@/lib/viewAs'
import { adminPath } from '@/lib/urls'
import { t } from '@/i18n'

// Leaves the view: the server forgets it (asked without the view's header, as the administrator), and the tab closes;
// a tab the browser will not let a page close goes to the administrator's own app instead.
export async function exitViewAs(user: string, leave: () => void = leaveTab): Promise<void> {
  forgetViewAs()
  try {
    await manageApi.endViewAs(user)
  } catch {
    // It ends by itself within the hour; the tab leaves it either way.
  }
  leave()
}

function leaveTab() {
  window.close()
  window.location.replace('/')
}

// The bar of a tab that views the app as a person: above everything, in the safe area at the top, and it cannot be
// dismissed. Its height is --viewas-h (index.css), which the top bar and the sidebar leave room for.
export function ViewAsBanner({
  view,
  user,
  name,
  leave = leaveTab,
}: {
  view: ViewAsInfo
  user: string
  name: string
  // How the tab leaves once the view is ended (a page load; a test watches it instead).
  leave?: () => void
}) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(id)
  }, [])
  const left = t.viewAs.left(minutesLeft(view.until, now))
  return (
    <div
      role="region"
      aria-label={t.viewAs.region}
      data-slot="view-as-banner"
      className="fixed inset-x-0 top-0 z-50 flex h-(--viewas-h) items-center gap-2 bg-violet-700 pt-[env(safe-area-inset-top)] pr-[max(0.5rem,env(safe-area-inset-right))] pl-[max(0.75rem,env(safe-area-inset-left))] text-sm text-white shadow-sm dark:bg-violet-900"
    >
      <Eye className="size-4 shrink-0" aria-hidden="true" />
      <p className="min-w-0 flex-1 truncate">
        <span className="font-medium">{t.viewAs.viewing(name)}</span>
        <span aria-hidden="true"> · </span>
        <span>{t.viewAs.readOnly}</span>
        <span className="max-sm:sr-only">
          <span aria-hidden="true"> · </span>
          {left}
        </span>
      </p>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => void exitViewAs(user, leave)}
        aria-label={t.viewAs.exitLabel}
        className="h-9 shrink-0 border-white/40 bg-transparent text-white hover:bg-white/15 hover:text-white max-sm:size-10 max-sm:px-0 pointer-coarse:h-10 dark:border-white/30 dark:bg-transparent dark:hover:bg-white/15"
      >
        <LogOut aria-hidden="true" />
        <span className="max-sm:sr-only">{t.viewAs.exit}</span>
      </Button>
    </div>
  )
}

// In place of the app when the view is over (its hour, a restart, the key lost, or the server no longer knows it).
export function ViewAsEnded() {
  const back = () => {
    forgetViewAs()
    window.location.replace(adminPath('users'))
  }
  return (
    <main
      data-slot="view-as-ended"
      className="flex min-h-dvh flex-col items-center justify-center gap-4 bg-background px-6 pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)] text-center"
    >
      <Eye className="size-8 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
      <h1 className="text-lg font-medium">{t.viewAs.endedTitle}</h1>
      <p className="max-w-sm text-sm text-muted-foreground">{t.viewAs.endedBody}</p>
      <Button type="button" className="h-10" onClick={back}>
        {t.viewAs.back}
      </Button>
    </main>
  )
}
