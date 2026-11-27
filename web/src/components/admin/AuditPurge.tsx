import { useEffect, useId, useState } from 'react'
import { Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAuditPurgeCount, useAuditPurged, usePurgeAudit } from '@/api/admin'
import { useJob } from '@/api/manage'
import type { AuditCategory } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FormMessage } from '@/components/auth/Field'
import { JobBar } from '@/components/admin/manage/JobProgress'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { tsec } from '@/i18n/security'

const tp = tsec.audit.purge
const CATEGORIES: AuditCategory[] = ['security', 'admin', 'data', 'system', 'signins', 'reads', 'downloads', 'api']
const DAY = /^\d{4}-\d{2}-\d{2}$/

// The button that opens the purge, and the dialog: what to delete (a category, a day), the count the server gives for
// a dry run, and a statement of exactly what will go before the one destructive button. Site administrators only.
export function AuditPurge({ compact = false }: { compact?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        type="button"
        variant="outline"
        className={compact ? 'h-10' : undefined}
        data-slot="audit-purge-open"
        onClick={() => setOpen(true)}
      >
        <Trash2 aria-hidden="true" />
        {tp.open}
      </Button>
      {open && <PurgeDialog onClose={() => setOpen(false)} />}
    </>
  )
}

function PurgeDialog({ onClose }: { onClose: () => void }) {
  const [category, setCategory] = useState<AuditCategory | 'all'>('all')
  const [before, setBefore] = useState('')
  const [error, setError] = useState('')
  const [jobId, setJobId] = useState<string | null>(null)
  const validDay = before === '' || DAY.test(before)
  const input = { category, ...(before && validDay ? { before } : {}) }
  const count = useAuditPurgeCount(input, validDay && !jobId)
  const purge = usePurgeAudit()
  const purged = useAuditPurged()
  const job = useJob(jobId).data
  const id = useId()
  const n = count.data?.count
  const more = count.data?.more ?? false
  const label = category === 'all' ? tp.all.toLowerCase() : tsec.audit.categories[category]
  const running = purge.isPending || (jobId !== null && (!job || job.state === 'queued' || job.state === 'running'))
  const ended = job && job.state !== 'queued' && job.state !== 'running' ? job : null
  // The log is read again once the job has ended; the result stays in the dialog until it is closed.
  useEffect(() => {
    if (ended) {
      void purged()
      if (ended.state === 'done') toast.success(tp.done(ended.done))
    }
  }, [ended?.id, ended?.state]) // eslint-disable-line react-hooks/exhaustive-deps
  const run = () => {
    setError('')
    purge.mutate(input, {
      onSuccess: (r) => setJobId(r.job.id),
      onError: (e) => setError(e instanceof ApiError && e.message ? e.message : tp.failed),
    })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !running && onClose()}>
      <DialogContent data-slot="audit-purge-dialog">
        <DialogHeader>
          <DialogTitle>{tp.title}</DialogTitle>
          <DialogDescription>{tp.help}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor={`${id}-category`}>{tp.category}</Label>
            <NativeSelect
              id={`${id}-category`}
              value={category}
              disabled={running || !!ended}
              onChange={(e) => setCategory(e.target.value as AuditCategory | 'all')}
            >
              <option value="all">{tp.all}</option>
              {CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {tsec.audit.categories[c]}
                </option>
              ))}
            </NativeSelect>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor={`${id}-before`}>{tp.before}</Label>
            <Input
              id={`${id}-before`}
              type="date"
              value={before}
              disabled={running || !!ended}
              aria-invalid={!validDay}
              onChange={(e) => setBefore(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">{tp.beforeHelp}</p>
          </div>
          {jobId ? (
            <div className="grid gap-2" data-slot="audit-purge-job">
              {job && <JobBar job={job} />}
              <p role="status" aria-live="polite" data-slot="audit-purge-result" className="text-sm font-medium">
                {ended
                  ? ended.state === 'done'
                    ? tp.done(ended.done)
                    : tp.stopped(ended.done)
                  : tp.progress(job?.done ?? 0)}
              </p>
            </div>
          ) : (
            <p
              role="status"
              aria-live="polite"
              data-slot="audit-purge-count"
              className={n && n > 0 ? 'text-sm font-medium text-destructive' : 'text-sm text-muted-foreground'}
            >
              {count.isError
                ? tp.countFailed
                : n === undefined
                  ? tp.counting
                  : n === 0
                    ? tp.none
                    : tp.will(n, label, before, more)}
            </p>
          )}
          <FormMessage>{error}</FormMessage>
        </div>
        <DialogFooter>
          {ended ? (
            <Button type="button" onClick={onClose}>
              {tp.close}
            </Button>
          ) : (
            <>
              <Button type="button" variant="outline" disabled={running} onClick={onClose}>
                {tp.cancel}
              </Button>
              <Button type="button" variant="destructive" disabled={running || !n || count.isFetching} onClick={run}>
                {running ? tp.deleting : tp.confirm(n ?? 0, more)}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
