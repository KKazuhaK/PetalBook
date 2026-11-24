import { useState } from 'react'
import { manageApi } from '@/api/manage'
import { useQueryClient } from '@tanstack/react-query'
import { keys } from '@/api/hooks'
import { FormMessage } from '@/components/auth/Field'
import { Spinner } from '@/components/common'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { tg } from '@/i18n/manage'
import { manageError } from './shared'

// The reason for an audited, read-only view of a space an administrator is not a member of. The view is recorded on
// the server; the server also says whether one is open.
export function ViewForm({
  space,
  name,
  onStarted,
  onCancel,
}: {
  space: string
  name: string
  onStarted: () => void
  onCancel?: () => void
}) {
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const ok = reason.trim().length >= 1 && reason.trim().length <= 500
  const start = async () => {
    if (!ok || busy) return
    setBusy(true)
    setError('')
    try {
      await manageApi.startView(space, reason.trim())
      void qc.invalidateQueries({ queryKey: keys.viewedSpace(space) })
      onStarted()
    } catch (e) {
      setError(manageError(e, tg.view.failed))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="grid gap-4"
      data-slot="view-form"
      onSubmit={(e) => {
        e.preventDefault()
        void start()
      }}
    >
      <p className="text-sm text-muted-foreground">{tg.view.help}</p>
      <div className="grid gap-1.5">
        <Label htmlFor="view-reason">{tg.view.reason}</Label>
        <Textarea
          id="view-reason"
          rows={3}
          maxLength={500}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          aria-label={`${tg.view.title(name)}: ${tg.view.reason}`}
        />
        <p className="text-xs text-muted-foreground">{tg.view.reasonHelp}</p>
      </div>
      <FormMessage>{error}</FormMessage>
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {tg.cancel}
          </Button>
        )}
        <Button type="submit" disabled={!ok || busy}>
          {busy && <Spinner />}
          {busy ? tg.view.starting : tg.view.start}
        </Button>
      </div>
    </form>
  )
}
