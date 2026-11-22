import { useState } from 'react'
import { NavLink, useNavigate } from 'react-router'
import { ChevronsUpDown } from 'lucide-react'
import { syncOf, useActiveSpace, useCreateSpace, useRenameSpace } from '@/api/hooks'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { spaceUrls } from '@/lib/urls'
import { t } from '@/i18n'
import { NameDialog } from './NameDialog'
import { SyncIndicator } from './SyncIndicator'

// Accounts mode only: the space in use, the others to switch to, and making or
// renaming one. The space is in the address, so switching is navigating.
export function SpaceSwitcher() {
  const { id, spaces } = useActiveSpace()
  const navigate = useNavigate()
  const create = useCreateSpace()
  const rename = useRenameSpace()
  const [dialog, setDialog] = useState<'new' | 'rename' | null>(null)
  const current = spaces.find((s) => s.id === id)

  return (
    <>
      <div className="flex items-center gap-1">
        <NavLink to={id ? spaceUrls(id).recent() : '/'} className="flex shrink-0 items-center px-1 pt-0.5" aria-label={t.nav.recent}>
          <img src="/favicon.svg" alt="" className="size-6 rounded-md" />
        </NavLink>
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label={t.fmt.labelled(t.spaces.switcher, current?.name ?? '')}
            className="flex min-w-0 flex-1 items-center gap-1 rounded-md px-1.5 py-1 text-left text-sm font-semibold outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            <span className="truncate">{current?.name ?? t.appName}</span>
            <ChevronsUpDown className="ml-auto size-3.5 shrink-0 text-muted-foreground" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-56">
            <DropdownMenuRadioGroup value={id ?? ''} onValueChange={(v) => navigate(spaceUrls(String(v)).recent())}>
              {spaces.map((s) => (
                <DropdownMenuRadioItem key={s.id} value={s.id}>
                  <span className="truncate">{s.name}</span>
                  {s.personal && <span className="text-xs text-muted-foreground">{t.spaces.personal}</span>}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => setDialog('new')}>{t.spaces.new}</DropdownMenuItem>
            <DropdownMenuItem disabled={!current} onClick={() => setDialog('rename')}>
              {t.spaces.rename}
            </DropdownMenuItem>
            {/* Only the owner can change where the space keeps its repository. */}
            <DropdownMenuItem disabled={!current || current.role !== 'owner'} onClick={() => id && navigate(spaceUrls(id).settings())}>
              {t.spaces.settings}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        {id && <SyncIndicator space={id} sync={syncOf(current)} />}
      </div>
      <NameDialog
        open={dialog === 'new'}
        onOpenChange={(o) => !o && setDialog(null)}
        title={t.spaces.newTitle}
        initial=""
        submitLabel={t.spaces.create}
        onSubmit={async (name) => {
          // A Chinese-language app names the inbox folder in Chinese.
          const space = await create.mutateAsync({ name, inbox: t.inbox.folderName })
          navigate(spaceUrls(space.id).recent())
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
