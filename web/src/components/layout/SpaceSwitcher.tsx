import { useState } from 'react'
import { Logo } from '@/components/brand/Logo'
import { NavLink, useNavigate } from 'react-router'
import { ChevronsUpDown } from 'lucide-react'
import { rememberSpace, syncOf, useActiveSpace, useRenameSpace } from '@/api/hooks'
import { can } from '@/lib/permissions'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { spaceUrls } from '@/lib/urls'
import { t } from '@/i18n'
import { settingsPath } from '@/components/settings/sections'
import { ProblemDot } from '@/components/settings/ProblemDot'
import { useOpenSettings, type SettingsState } from '@/components/settings/useSettings'
import { NameDialog } from './NameDialog'
import { NewSpaceDialog } from './NewSpaceDialog'
import { SyncIndicator } from './SyncIndicator'

// Accounts mode only: the space in use, the others to switch to, and making or
// renaming one. The space is in the address, so switching is navigating.
export function SpaceSwitcher() {
  const { id, spaces } = useActiveSpace()
  const navigate = useNavigate()
  const openSettings = useOpenSettings()
  const rename = useRenameSpace()
  const [dialog, setDialog] = useState<'new' | 'rename' | null>(null)
  const current = spaces.find((s) => s.id === id)

  return (
    <>
      <div className="flex items-center gap-1">
        <NavLink
          to={id ? spaceUrls(id).home() : '/'}
          className="flex shrink-0 items-center px-1 pt-0.5"
          aria-label={t.nav.home}
        >
          <Logo />
        </NavLink>
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label={t.fmt.labelled(t.spaces.switcher, current?.name ?? '')}
            className="flex min-w-0 flex-1 items-center gap-1 rounded-md px-1.5 py-1 text-left text-sm font-semibold outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <span className="truncate">{current?.name ?? t.appName}</span>
            <ProblemDot />
            <ChevronsUpDown className="ml-auto size-3.5 shrink-0 text-muted-foreground" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-56">
            <DropdownMenuRadioGroup value={id ?? ''} onValueChange={(v) => navigate(spaceUrls(String(v)).home())}>
              {spaces.map((s) => (
                <DropdownMenuRadioItem key={s.id} value={s.id}>
                  <span className="truncate">{s.name}</span>
                  {s.personal && <span className="text-xs text-muted-foreground">{t.spaces.personal}</span>}
                  {s.disabled && (
                    <span className="rounded border px-1 text-[0.65rem] leading-4 text-muted-foreground">
                      {t.spaces.disabledMark}
                    </span>
                  )}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => setDialog('new')}>{t.spaces.new}</DropdownMenuItem>
            {/* Renaming is for whoever may manage the space. */}
            <DropdownMenuItem disabled={!can(current, 'manage')} onClick={() => setDialog('rename')}>
              {t.spaces.rename}
            </DropdownMenuItem>
            {/* Everyone sees the settings (the members, at least); what they may change there depends on the permissions. */}
            <DropdownMenuItem disabled={!current} onClick={() => id && openSettings('general')}>
              {t.spaces.settings}
              <ProblemDot className="ml-auto" />
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        {id && <SyncIndicator space={id} sync={syncOf(current)} />}
      </div>
      <NewSpaceDialog
        open={dialog === 'new'}
        onOpenChange={(o) => !o && setDialog(null)}
        onCreated={(space, then) => {
          const home = spaceUrls(space.id).home()
          if (!then?.openSync) return navigate(home)
          // Made on this server: the address is given in the new space's sync settings.
          rememberSpace(space.id)
          navigate(settingsPath('sync'), { state: { from: home } satisfies SettingsState })
        }}
      />
      <NameDialog
        open={dialog === 'rename'}
        onOpenChange={(o) => !o && setDialog(null)}
        title={t.spaces.renameTitle}
        initial={current?.name ?? ''}
        submitLabel={t.spaces.save}
        onSubmit={async (name) => {
          if (current && name !== current.name) await rename.mutateAsync({ id: current.id, name })
        }}
      />
    </>
  )
}
