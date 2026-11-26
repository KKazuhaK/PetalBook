import { useState } from 'react'
import { Link } from 'react-router'
import { useCreateExport } from '@/api/manage'
import type { ExportRec, ExportRequest } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAdminPaths } from '../TenantScope'
import { tg } from '@/i18n/manage'
import { daysLeft, manageError, useIsSite, tapTarget, fitScreen } from './shared'

// Starts an export of some spaces (or recycle items). The history is the site's.
export function ExportDialog({
  request,
  name,
  onClose,
}: {
  request: ExportRequest
  name: string
  onClose: () => void
}) {
  const site = useIsSite()
  const make = useCreateExport()
  const { path: adminPath } = useAdminPaths()
  const [history, setHistory] = useState(false)
  const [error, setError] = useState('')
  const [made, setMade] = useState<ExportRec | null>(null)
  const go = () => {
    setError('')
    make.mutate(
      { ...request, history: site && history },
      { onSuccess: setMade, onError: (e) => setError(manageError(e)) },
    )
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className={`${fitScreen} sm:max-w-md`} data-slot="export-dialog">
        <DialogHeader>
          <DialogTitle>
            {tg.exports.dialogTitle}：{name}
          </DialogTitle>
          <DialogDescription>{tg.exports.dialogHelp}</DialogDescription>
        </DialogHeader>
        {made ? (
          <p role="status" className="text-sm">
            {tg.exports.started(Math.max(1, daysLeft(made.expires)))}
          </p>
        ) : (
          <>
            {site && (
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  className="mt-0.5 size-4 accent-[var(--color-primary)]"
                  checked={history}
                  onChange={(e) => setHistory(e.target.checked)}
                />
                <span>
                  {tg.exports.history2}
                  <span className="block text-xs text-muted-foreground">{tg.exports.history2Help}</span>
                </span>
              </label>
            )}
            <FormMessage>{error}</FormMessage>
          </>
        )}
        <DialogFooter>
          {made ? (
            <>
              <Button
                className={tapTarget}
                type="button"
                variant="outline"
                render={<Link to={adminPath('spaces', 'deleted')} onClick={onClose} />}
              >
                {tg.exports.goList}
              </Button>
              <Button className={tapTarget} type="button" onClick={onClose}>
                {tg.bulk.close}
              </Button>
            </>
          ) : (
            <>
              <Button className={tapTarget} type="button" variant="outline" onClick={onClose}>
                {tg.cancel}
              </Button>
              <Button className={tapTarget} type="button" disabled={make.isPending} onClick={go}>
                {make.isPending && <Spinner />}
                {tg.exports.start}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
