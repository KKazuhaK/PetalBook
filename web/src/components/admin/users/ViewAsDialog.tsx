import { useState, type FormEvent } from 'react'
import { manageApi } from '@/api/manage'
import { ApiError } from '@/api/client'
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
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useInScope } from '@/lib/scopedClient'
import { tc } from '@/i18n/console'

// Asks why, then opens the app as the person sees it in a new tab (docs/api.md, "Viewing as a person"). The tab is
// opened at once, while the click still counts as the person's (a browser blocks a window opened after a wait), and is
// sent to the view when the server has opened it; a refusal closes it again. Where the browser blocks it anyway, a
// link opens the view instead.
export function ViewAsDialog({ user, name, onClose }: { user: string; name: string; onClose: () => void }) {
  const inScope = useInScope()
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState<string | null>(null)
  const text = reason.trim()
  const ok = text.length >= 1 && text.length <= 500

  const start = async (e: FormEvent) => {
    e.preventDefault()
    if (!ok || busy) return
    setBusy(true)
    setError('')
    const tab = window.open('', '_blank')
    try {
      // The view keeps the console's scope: a tenant's, when the console is in one.
      const view = await inScope(() => manageApi.startViewAs(user, text))
      const to = `/as/${encodeURIComponent(view.id)}`
      if (tab) {
        tab.opener = null
        tab.location.href = to
        onClose()
      } else setBlocked(to)
    } catch (err) {
      tab?.close()
      setError(err instanceof ApiError ? err.message : tc.person.viewAsDialog.failed)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent sheet className="sm:max-w-md" data-slot="view-as-dialog">
        <form onSubmit={(e) => void start(e)} className="grid min-w-0 gap-4">
          <DialogHeader>
            <DialogTitle>{tc.person.viewAsDialog.title(name)}</DialogTitle>
            <DialogDescription>{tc.person.viewAsDialog.help}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="view-as-reason">{tc.person.viewAsDialog.reason}</Label>
            <Textarea
              id="view-as-reason"
              rows={3}
              maxLength={500}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              disabled={blocked !== null}
            />
            <p className="text-xs text-muted-foreground">{tc.person.viewAsDialog.reasonHelp}</p>
          </div>
          <FormMessage>{error}</FormMessage>
          {blocked && (
            <p className="text-sm" role="status">
              {tc.person.viewAsDialog.blocked}{' '}
              <a
                href={blocked}
                target="_blank"
                rel="noopener"
                className="font-medium underline underline-offset-4"
                onClick={onClose}
              >
                {tc.person.viewAsDialog.openLink}
              </a>
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" className="h-10 sm:h-9" onClick={onClose}>
              {tc.person.viewAsDialog.cancel}
            </Button>
            {blocked === null && (
              <Button type="submit" className="h-10 sm:h-9" disabled={!ok || busy}>
                {busy && <Spinner />}
                {busy ? tc.person.viewAsDialog.starting : tc.person.viewAsDialog.start}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
