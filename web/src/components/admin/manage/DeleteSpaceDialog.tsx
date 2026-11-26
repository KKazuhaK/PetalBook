import { useState } from 'react'
import { Download } from 'lucide-react'
import { Link } from 'react-router'
import { useDeleteSpace } from '@/api/admin'
import { useSpaceDeletion } from '@/api/manage'
import type { RecycleItem } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { formatBytes } from '@/lib/bytes'
import { absoluteTime } from '@/lib/time'
import { useAdminPaths } from '../TenantScope'
import { tg } from '@/i18n/manage'
import { ts } from '@/i18n/storage'
import { ExportDialog } from './ExportDialog'
import { manageError, tapTarget, fitScreen } from './shared'

const sameName = (a: string, b: string) => a.trim().normalize('NFC') === b.trim().normalize('NFC')

// Deleting one space: what it would do, then the name typed, then where it went. Everything goes through the bin.
export function DeleteSpaceDialog({
  id,
  name,
  personal,
  onClose,
}: {
  id: string
  name: string
  personal: boolean
  onClose: () => void
}) {
  const preview = useSpaceDeletion(id)
  const { path: adminPath } = useAdminPaths()
  const del = useDeleteSpace()
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [done, setDone] = useState<RecycleItem | null>(null)
  const [exporting, setExporting] = useState(false)
  const p = preview.data
  const ok = sameName(typed, name) && !personal

  const confirm = () => {
    if (!ok || del.isPending) return
    setError('')
    del.mutate({ id, reason: reason.trim() }, { onSuccess: setDone, onError: (e) => setError(manageError(e)) })
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`${fitScreen} sm:max-w-lg`} data-slot="delete-space">
        {done ? (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>{tg.del.space.deleted(name)}</DialogTitle>
              <DialogDescription>{tg.del.space.deletedHelp(absoluteTime(done.purgeAt))}</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button
                className={tapTarget}
                type="button"
                variant="outline"
                render={<Link to={adminPath('spaces', 'deleted')} onClick={onClose} />}
              >
                {tg.del.space.goBin}
              </Button>
              <Button className={tapTarget} type="button" onClick={onClose}>
                {tg.bulk.close}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="grid gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              confirm()
            }}
          >
            <DialogHeader>
              <DialogTitle>{tg.del.space.title(name)}</DialogTitle>
              <DialogDescription>{p ? tg.del.daysLine(p.recycleDays) : tg.del.daysUnknown}</DialogDescription>
            </DialogHeader>
            {preview.isPending ? (
              <PageFallback />
            ) : preview.isError ? (
              <ErrorState
                message={manageError(preview.error, tg.del.space.loadFailed)}
                onRetry={() => void preview.refetch()}
              />
            ) : (
              p && (
                <div className="grid gap-3 text-sm" data-slot="deletion-preview">
                  <p>
                    {tg.del.space.numbers(tg.del.space.pages(p.pages), formatBytes(p.bytes), formatBytes(p.heldBytes))}
                  </p>
                  <p>{tg.del.space.people(p.members, p.groups, p.drafts)}</p>
                  {p.drafts > 0 && <p className="text-xs text-muted-foreground">{tg.del.space.draftsKept}</p>}
                  <p className="text-xs text-muted-foreground">
                    {tg.del.space.cache}：{ts.spaces.caches[p.cache]}
                  </p>
                  {p.remote.url !== '' && (
                    <p className="text-xs text-muted-foreground">{tg.del.remoteKept(p.remote.url)}</p>
                  )}
                  {p.assetsRemote.url !== '' && (
                    <p className="text-xs text-muted-foreground">{tg.del.assetsKept(p.assetsRemote.url)}</p>
                  )}
                  <div className="grid gap-1 rounded-lg border p-3">
                    <p className="font-medium">{tg.del.space.exportTitle}</p>
                    <p className="text-xs text-muted-foreground">
                      {p.exportable ? tg.del.space.exportHelp : tg.del.space.notExportable}
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className={`w-fit ${tapTarget}`}
                      disabled={!p.exportable}
                      onClick={() => setExporting(true)}
                    >
                      <Download aria-hidden="true" />
                      {tg.del.space.exportNow}
                    </Button>
                  </div>
                </div>
              )
            )}
            {personal && (
              <p role="alert" className="text-sm text-destructive">
                {tg.del.space.personalBlocked}
              </p>
            )}
            <div className="grid gap-1.5">
              <Label htmlFor="delete-reason">{tg.del.space.reason}</Label>
              <Textarea
                id="delete-reason"
                rows={2}
                maxLength={500}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="delete-confirm">{tg.del.space.type}</Label>
              <code className="rounded-md bg-muted px-2 py-1 text-sm break-all select-all">{name}</code>
              <Input
                id="delete-confirm"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </div>
            <FormMessage>{error}</FormMessage>
            <DialogFooter>
              <Button className={tapTarget} type="button" variant="outline" onClick={onClose}>
                {tg.cancel}
              </Button>
              <Button
                className={tapTarget}
                type="submit"
                variant="destructive"
                disabled={!ok || del.isPending || preview.isPending}
              >
                {del.isPending && <Spinner />}
                {tg.del.space.confirm}
              </Button>
            </DialogFooter>
          </form>
        )}
        {exporting && <ExportDialog request={{ spaces: [id] }} name={name} onClose={() => setExporting(false)} />}
      </DialogContent>
    </Dialog>
  )
}
