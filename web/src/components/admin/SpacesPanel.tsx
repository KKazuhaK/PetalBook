import { useState } from 'react'
import { Link } from 'react-router'
import { MoreHorizontal, RefreshCw, Search } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAdminSpaces, useDeleteSpace, useResizeSpace, useSetSpaceDisabled, useTransferSpace, useUserSearch, USER_SEARCH_LIMIT } from '@/api/admin'
import type { AdminSpace, DeletedSpace, User } from '@/api/types'
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
import { ta } from '@/i18n/admin'

// Two names that read the same: trimmed, and in the same Unicode normal form.
const sameName = (a: string, b: string) => a.trim().normalize('NFC') === b.trim().normalize('NFC')

type Action = { kind: 'transfer' | 'disable' | 'delete'; space: AdminSpace }

// What a refusal of an administration call means, in plain words.
function actionError(e: unknown): string {
  const text = ta.spaces.errors
  if (!(e instanceof ApiError)) return ta.spaces.failed
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
      return e.message || ta.spaces.failed
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
      toast.success(ta.spaces.enabledDone)
    } catch (e) {
      toast.error(actionError(e))
    }
  }

  return (
    <SettingsSection title={ta.spaces.title} id="admin-spaces-heading" description={ta.spaces.help}>
      <div className="relative max-w-sm">
        <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={ta.spaces.searchPlaceholder} aria-label={ta.spaces.search} className="pl-8" autoComplete="off" />
      </div>

      {spaces.isPending ? (
        <PageFallback />
      ) : spaces.isError && !spaces.data ? (
        <ErrorState message={t.fmt.labelled(ta.spaces.loadFailed, spaces.error.message)} onRetry={() => void spaces.refetch()} />
      ) : items.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">{q ? ta.spaces.noMatch(q) : ta.spaces.empty}</p>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <table className="w-full min-w-[44rem] text-left text-sm">
              <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium">{ta.spaces.name}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{ta.spaces.owner}</th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">{ta.spaces.members}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{ta.spaces.size}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{ta.spaces.sync}</th>
                  <th scope="col" className="px-3 py-2 font-medium">{ta.spaces.status}</th>
                  <th scope="col" className="px-3 py-2 text-right font-medium">{ta.spaces.actions}</th>
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
                {spaces.isFetchingNextPage ? ta.spaces.loadingMore : ta.spaces.loadMore}
              </Button>
            )}
            <span className="text-xs text-muted-foreground">{spaces.hasNextPage ? ta.spaces.loaded(items.length) : ta.spaces.allLoaded(items.length)}</span>
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
  const sp = ta.spaces
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
  const sp = ta.spaces
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
  const sp = ta.spaces
  const transfer = useTransferSpace()
  // Who to hand the space to is looked for on the server, a few at a time: a server
  // with many accounts is never asked for all of them.
  const [term, setTerm] = useState('')
  const found = useUserSearch(useDebounced(term.trim(), 250))
  const [chosen, setChosen] = useState<User | null>(null)
  const [error, setError] = useState('')
  const userId = chosen?.id ?? ''
  const matches = (found.data?.users ?? []).filter((u) => u.id !== space.owner?.id)
  // The person already picked stays in the list while the search moves on.
  const choices = chosen && !matches.some((u) => u.id === chosen.id) ? [chosen, ...matches] : matches
  const truncated = (found.data?.users.length ?? 0) >= USER_SEARCH_LIMIT

  const submit = async () => {
    if (!userId || transfer.isPending) return
    setError('')
    try {
      await transfer.mutateAsync({ id: space.id, userId })
      toast.success(sp.transferred(chosen?.name ?? ''))
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
            <Label htmlFor="transfer-search">{sp.transferSearch}</Label>
            <Input id="transfer-search" type="search" value={term} onChange={(e) => setTerm(e.target.value)} placeholder={sp.transferSearchHint} autoComplete="off" spellCheck={false} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="transfer-to">{sp.transferTo}</Label>
            <NativeSelect id="transfer-to" value={userId} onChange={(e) => setChosen(choices.find((u) => u.id === e.target.value) ?? null)} disabled={found.isPending}>
              <option value="">{sp.transferChoose}</option>
              {choices.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.name} ({u.username})
                </option>
              ))}
            </NativeSelect>
            {found.isError ? (
              <p className="text-xs text-destructive">{t.fmt.labelled(sp.transferSearchFailed, found.error.message)}</p>
            ) : (
              !found.isPending && (
                <p className="text-xs text-muted-foreground">{matches.length === 0 ? sp.transferNoMatch : truncated ? sp.transferNarrow(USER_SEARCH_LIMIT) : ''}</p>
              )
            )}
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
  const sp = ta.spaces
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
  const sp = ta.spaces
  const del = useDeleteSpace()
  const [typed, setTyped] = useState('')
  const [error, setError] = useState('')
  const [result, setResult] = useState<DeletedSpace | null>(null)
  // Typed names are compared as people see them: without stray spaces around, and
  // with the same characters composed the same way.
  const typedMatches = sameName(typed, space.name)

  const confirm = async () => {
    if (!typedMatches || del.isPending) return
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
              <Label htmlFor="delete-confirm">{sp.deleteType}</Label>
              <code className="rounded-md bg-muted px-2 py-1 text-sm break-all select-all">{space.name}</code>
              <Input id="delete-confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
            </div>
            <FormMessage>{error}</FormMessage>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onClose}>
                {sp.cancel}
              </Button>
              <Button type="submit" variant="destructive" disabled={!typedMatches || del.isPending}>
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
