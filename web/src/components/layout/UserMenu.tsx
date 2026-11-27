import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { LogOut, Settings, ShieldCheck } from 'lucide-react'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { applySession, useSession } from '@/api/hooks'
import { useConsoleAccess } from '@/api/admin'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useOpenSettings } from '@/components/settings/useSettings'
import { flushAllSaves } from '@/editor/saveController'
import { syncedPrefs } from '@/lib/syncedPrefs'
import { adminPath } from '@/lib/urls'
import { t } from '@/i18n'

// Accounts mode only: the person's name, and settings, users (administrators)
// and sign out behind it. The language and theme live in Settings, Preferences.
export function UserMenu() {
  const session = useSession().data
  const qc = useQueryClient()
  const navigate = useNavigate()
  const openSettings = useOpenSettings()
  const user = session?.user
  const consoleOpen = useConsoleAccess().open
  if (!user) return null

  const signOut = async () => {
    // What is typed goes to the server first, if it will take it within a few
    // seconds; the browser's own draft keeps whatever does not make it.
    await Promise.race([flushAllSaves(), new Promise((r) => setTimeout(r, 4000))]).catch(() => undefined)
    // What was set since the last batch goes to the person's file before the session ends.
    await Promise.race([syncedPrefs()?.flush(), new Promise((r) => setTimeout(r, 2000))]).catch(() => undefined)
    try {
      await api.logout()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t.auth.failed)
      return
    }
    void syncedPrefs()?.reset()
    applySession(qc, null)
    navigate('/login', { replace: true })
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={t.fmt.labelled(t.userMenu.label, user.name)}
        className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1 text-left text-sm outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <span
          aria-hidden="true"
          className="flex size-6 shrink-0 items-center justify-center rounded-full bg-brand/15 text-xs font-medium text-brand"
        >
          {Array.from(user.name)[0]?.toUpperCase() ?? '?'}
        </span>
        <span className="truncate">{user.name}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="min-w-48">
        <DropdownMenuItem onClick={() => openSettings(session?.mode === 'accounts' ? 'profile' : 'preferences')}>
          <Settings />
          {t.userMenu.settings}
        </DropdownMenuItem>
        {consoleOpen && (
          <DropdownMenuItem onClick={() => navigate(adminPath())}>
            <ShieldCheck />
            {t.userMenu.admin}
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => void signOut()}>
          <LogOut />
          {t.userMenu.signOut}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
