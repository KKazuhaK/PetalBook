import { useJob } from '@/api/manage'
import type { Job } from '@/api/types'
import { formatBytes } from '@/lib/bytes'
import { tg } from '@/i18n/manage'

// The bar and the words of a job that is not a bulk action: an export being made, an item of the recycle bin being
// moved, restored or purged. Which job is the caller's: JobWatch asks the server about it (useJob), and only while it
// is queued or running.
function words(job: Job): string {
  switch (job.state) {
    case 'queued':
      return tg.jobs.queued
    case 'running':
      return job.total > 0
        ? tg.jobs.progress(job.done, job.total, job.bytesDone > 0 ? formatBytes(job.bytesDone) : '')
        : tg.jobs.running
    case 'done':
      return tg.jobs.done
    case 'failed':
      return tg.jobs.failed(job.error || job.failed[0]?.error || '')
    case 'interrupted':
      return tg.jobs.interrupted
    case 'cancelled':
      return tg.jobs.cancelled
  }
}

export function JobBar({ job }: { job: Job }) {
  const pct = job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 100)) : job.state === 'done' ? 100 : 0
  const bad = job.state === 'failed' || job.state === 'interrupted'
  return (
    <div className="grid gap-1" data-slot="job-progress" data-job={job.id} data-state={job.state}>
      <div
        className="h-1.5 overflow-hidden rounded-full bg-muted"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={Math.max(job.total, 1)}
        aria-valuenow={job.done}
      >
        <div
          className={`h-full transition-all ${bad ? 'bg-destructive' : 'bg-primary'}`}
          style={{ width: `${pct}%` }}
        />
      </div>
      <p role="status" className={`text-xs tabular-nums ${bad ? 'text-destructive' : 'text-muted-foreground'}`}>
        {words(job)}
      </p>
    </div>
  )
}

export function JobWatch({ id }: { id: string }) {
  const job = useJob(id)
  if (!job.data) return null
  return <JobBar job={job.data} />
}
