import { useState } from 'react'
import { Link } from 'react-router'
import { MoreHorizontal, RefreshCw, Search } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAdminSpaces, useDeleteSpace, useResizeSpace, useSetSpaceDisabled, useTransferSpace, useUsers } from '@/api/hooks'
import type { AdminSpace, DeletedSpace } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { spaceUrls } from '@/lib/urls'
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'

type Action = { kind: 'transfer' | 'disable' | 'delete'; space: AdminSpace }

// What a refusal of an administration call means, in plain words.
function actionError(e: unknown): string {
  const text = t.admin.spaces.errors
  if (!(e instanceof ApiError)) return t.admin.spaces.failed
  switch (e.code) {
    case 'personal_space':
      return text.personalSpace
    case 'user_not_found':
      return text.userNotFound
    case 'user_disabled':
      return text.userDisabled
    case 'busy':
      return text.busy
    case 'not_found':
      return text.notFound
    default:
      return e.message || t.admin.spaces.failed
  }
}

// Every space on the server, a page at a time, with what an administrator may do
// to one. The list is read from stored values only: the server never walks the
// spaces to answer it, so sizes are as old as the last measurement.
export function SpacesPanel() {
  const [text, setText] = useState('')
  const q = useDebounced(text.trim(), 300)
  const spaces = useAdminSpaces(q)
  const setDisabled = useSetSpaceDisabled()
  const [action, setAction] = useState<Action | null>(null)

  const items = spaces.data?.pages.flatMap((p) => p.items) ?? []
  const enable = async (s: AdminSpace) => {
    try {
      await setDisabled.mutateAsync({ id: s.id, disabled: false })
      toast.success(t.admin.spaces.enabledDone)
    } catch (e) {
      toast.error(actionError(e))
    }
  }

  return (
    <SettingsSection title={t.admin.spaces.title} id="admin-spaces-heading" description={t.admin.spaces.help}>
      <div className="relative max-w-sm">
        <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t.admin.spaces.searchPlaceholder} aria-label={t.admin.spaces.search} className="pl-8" autoComplete="off" />
      </div>

      {spaces.isPending ? (
        <PageFallback />
      ) : spaces.isError && !spaces.data ? (
        <ErrorState message={t.fmt.labelled(t.admin.spaces.loadFailed, spaces.error.message)} onRetry={() => void spaces.refetch()} />
      ) : items.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">{q ? t.admin.spaces.noMatch(q) : t.admin.spaces.empty}</p>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[44rem] text-left text-sm">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium">{t.admin.spaces.name}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{t.admin.spaces.owner}</th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">{t.admin.spaces.members}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{t.admin.spaces.size}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{t.admin.spaces.sync}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{t.admin.spaces.status}</th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">{t.admin.spaces.actions}</th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {items.map((s) => (
                  <SpaceRow key={s.id} space={s} onAction={(kind) => setAction({ kind, space: s })} onEnable={() => void enable(s)} />
                ))}
              </tbody>
            </table>
          </div>
          <div className="flex flex-col items-center gap-2">
            {spaces.hasNextPage && (
              <Button type="button" variant="outline" size="sm" disabled={spaces.isFetchingNextPage} onClick={() => void spaces.fetchNextPage()}>
                {spaces.isFetchingNextPage && <Spinner />}
                {spaces.isFetchingNextPage ? t.admin.spaces.loadingMore : t.admin.spaces.loadMore}
              </Button>
            )}
            <span className="text-xs text-muted-foreground">{spaces.hasNextPage ? t.admin.spaces.loaded(items.length) : t.admin.spaces.allLoaded(items.length)}</span>
            {spaces.isFetchNextPageError && <ErrorState message={spaces.error.message} onRetry={() => void spaces.fetchNextPage()} />}
          </div>
        </>
      )}

      {action?.kind === 'transfer' && <TransferDialog space={action.space} onClose={() => setAction(null)} />}
      {action?.kind === 'disable' && <DisableDialog space={action.space} onClose={() => setAction(null)} />}
      {action?.kind === 'delete' && <DeleteDialog space={action.space} onClose={() => setAction(null)} />}
    </SettingsSection>
  )
}

function SpaceRow({ space: s, onAction, onEnable }: { space: AdminSpace; onAction: (kind: Action['kind']) => void; onEnable: () => void }) {
  const sp = t.admin.spaces
  return (
    <tr data-space={s.id}>
      <th scope="row" className="max-w-56 px-3 py-2.5 font-normal">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="truncate font-medium">{s.name}</span>
          {s.personal && <Badge variant="outline">{sp.personal}</Badge>}
        </div>
      </th>
      <td className="max-w-40 px-3 py-2.5">
        {s.owner ? (
          <>
            <div className="truncate">{s.owner.name}</div>
            <div className="truncate text-xs text-muted-foreground">{s.owner.username}</div>
          </>
        ) : (
          <span className="text-muted-foreground">{sp.noOwner}</span>
        )}
      </td>
      <td className="px-3 py-2.5 text-right tabular-nums">{s.members}</td>
      <td className="px-3 py-2.5">
        <SizeCell space={s} />
      </td>
      <td className="px-3 py-2.5 whitespace-nowrap">{sp.syncStates[s.sync]}</td>
      <td className="px-3 py-2.5">{s.disabled ? <Badge variant="secondary">{sp.disabled}</Badge> : <span className="text-muted-foreground">{sp.active}</span>}</td>
      <td className="px-3 py-2.5">
        <div className="flex items-center justify-end gap-1">
          <Button variant="outline" size="sm" render={<Link to={spaceUrls(s.id).recent()} aria-label={t.fmt.labelled(sp.open, s.name)} />}>
            {sp.open}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={sp.moreFor(s.name)} />}>
              <MoreHorizontal />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-48">
              <DropdownMenuItem disabled={s.personal} onClick={() => onAction('transfer')}>
                {sp.transfer}
              </DropdownMenuItem>
              {s.disabled ? <DropdownMenuItem onClick={onEnable}>{sp.enable}</DropdownMenuItem> : <DropdownMenuItem onClick={() => onAction('disable')}>{sp.disable}</DropdownMenuItem>}
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" disabled={s.personal} onClick={() => onAction('delete')}>
                {sp.delete}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </td>
    </tr>
  )
}

// The stored size, or a dash until the server has measured it, and a way to
// measure this one space now.
function SizeCell({ space: s }: { space: AdminSpace }) {
  const sp = t.admin.spaces
  const resize = useResizeSpace()
  const measure = async () => {
    try {
      await resize.mutateAsync(s.id)
    } catch (e) {
      toast.error(t.fmt.labelled(sp.measureFailed, e instanceof Error ? e.message : ''))
    }
  }
  const measured = s.sizeBytes !== null
  return (
    <div className="flex items-center gap-1.5 whitespace-nowrap">
      <span className={`tabular-nums ${measured ? '' : 'text-muted-foreground'}`} title={s.sizeAt ? `${sp.measuredAt(absoluteTime(s.sizeAt))}. ${sp.measureHelp}` : sp.measureHelp}>
        {s.sizeBytes !== null ? formatBytes(s.sizeBytes) : sp.notMeasured}
      </span>
      {measured ? (
        <Button type="button" variant="ghost" size="icon-xs" disabled={resize.isPending} onClick={() => void measure()} aria-label={t.fmt.labelled(sp.measure, s.name)} title={sp.measure}>
          {resize.isPending ? <Spinner className="size-3" /> : <RefreshCw className="size-3" />}
        </Button>
      ) : (
        <Button type="button" variant="ghost" size="xs" disabled={resize.isPending} onClick={() => void measure()} aria-label={t.fmt.labelled(sp.measure, s.name)}>
          {resize.isPending ? sp.measuring : sp.measure}
        </Button>
      )}
    </div>
  )
}

function TransferDialog({ space, onClose }: { space: AdminSpace; onClose: () => void }) {
  const sp = t.admin.spaces
  const users = useUsers()
  const transfer = useTransferSpace()
  const [userId, setUserId] = useState('')
  const [error, setError] = useState('')
  const choices = (users.data?.users ?? []).filter((u) => !u.disabled && u.id !== space.owner?.id)

  const submit = async () => {
    if (!userId || transfer.isPending) return
    setError('')
    try {
      await transfer.mutateAsync({ id: space.id, userId })
      toast.success(sp.transferred(choices.find((u) => u.id === userId)?.name ?? ''))
      onClose()
    } catch (e) {
      setError(actionError(e))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{sp.transferTitle(space.name)}</DialogTitle>
            <DialogDescription>{sp.transferHelp}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="transfer-to">{sp.transferTo}</Label>
            <NativeSelect id="transfer-to" value={userId} onChange={(e) => setUserId(e.target.value)} disabled={users.isPending}>
              <option value="">{sp.transferChoose}</option>
              {choices.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.name} ({u.username})
                </option>
              ))}
            </NativeSelect>
          </div>
          <FormMessage>{error}</FormMessage>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {sp.cancel}
            </Button>
            <Button type="submit" disabled={!userId || transfer.isPending}>
              {transfer.isPending && <Spinner />}
              {sp.transferConfirm}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function DisableDialog({ space, onClose }: { space: AdminSpace; onClose: () => void }) {
  const sp = t.admin.spaces
  const disable = useSetSpaceDisabled()
  const [error, setError] = useState('')
  const confirm = async () => {
    setError('')
    try {
      await disable.mutateAsync({ id: space.id, disabled: true })
      toast.success(sp.disabledDone)
      onClose()
    } catch (e) {
      setError(actionError(e))
    }
  }
  return (
    <AlertDialog open onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{sp.disableTitle(space.name)}</AlertDialogTitle>
          <AlertDialogDescription>{sp.disableHelp}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormMessage>{error}</FormMessage>
        <AlertDialogFooter>
          <AlertDialogCancel>{sp.cancel}</AlertDialogCancel>
          {/* Not AlertDialogAction: the dialog stays open if the server refuses. */}
          <Button type="button" variant="destructive" disabled={disable.isPending} onClick={() => void confirm()}>
            {disable.isPending && <Spinner />}
            {sp.disableConfirm}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

// Deleting asks for the space's name, and afterwards says where its files went.
function DeleteDialog({ space, onClose }: { space: AdminSpace; onClose: () => void }) {
  const sp = t.admin.spaces
  const del = useDeleteSpace()
  const [typed, setTyped] = useState('')
  const [error, setError] = useState('')
  const [result, setResult] = useState<DeletedSpace | null>(null)

  const confirm = async () => {
    if (typed !== space.name || del.isPending) return
    setError('')
    try {
      setResult(await del.mutateAsync(space.id))
    } catch (e) {
      setError(actionError(e))
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        {result ? (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{sp.deleted(space.name)}</DialogTitle>
              <DialogDescription>{result.trashed ? sp.movedTo(result.trashed) : sp.notMoved}</DialogDescription>
            </DialogHeader>
            {result.warnings.length > 0 && (
              <div role="alert" className="grid gap-1 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
                <p className="font-medium">{sp.warnings}</p>
                <ul className="list-disc pl-5 text-xs">
                  {result.warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </ul>
              </div>
            )}
            <DialogFooter>
              <Button type="button" onClick={onClose}>
                {t.common.close}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              void confirm()
            }}
          >
            <DialogHeader>
              <DialogTitle>{sp.deleteTitle(space.name)}</DialogTitle>
              <DialogDescription>{sp.deleteHelp}</DialogDescription>
            </DialogHeader>
            <div className="grid gap-1.5">
              <Label htmlFor="delete-confirm">{sp.deleteType(space.name)}</Label>
              <Input id="delete-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
            </div>
            <FormMessage>{error}</FormMessage>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>
                {sp.cancel}
              </Button>
              <Button type="submit" variant="destructive" disabled={typed !== space.name || del.isPending}>
                {del.isPending && <Spinner />}
                {sp.deleteConfirm}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
