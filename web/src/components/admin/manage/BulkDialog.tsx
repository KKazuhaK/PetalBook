import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '@/api/client'
import { useConfirm, useJob, useStartBulk } from '@/api/manage'
import type {
  BulkJob,
  ConfirmAction,
  ConfirmRequest,
  ConfirmResult,
  SpaceDeletionPreview,
  User,
  UserDeletionPlan,
} from '@/api/types'
import { Spinner } from '@/components/common'
import { FormMessage } from '@/components/auth/Field'
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
import { formatBytes } from '@/lib/bytes'
import { tg } from '@/i18n/manage'
import { manageError, UserPicker } from './shared'

const isSpacePreview = (l: unknown): l is SpaceDeletionPreview =>
  !!l && typeof l === 'object' && 'heldBytes' in l && 'recycleDays' in l
const isUserPlan = (l: unknown): l is UserDeletionPlan =>
  !!l && typeof l === 'object' && 'revoke' in l && 'recycle' in l

// One line of the plan, in words.
function PlanLine({ line }: { line: ConfirmResult['plan'][number] }) {
  if (isSpacePreview(line)) {
    return (
      <li data-slot="plan-line">
        <span className="font-medium">{line.space.name}</span>{' '}
        <span className="text-muted-foreground">
          {formatBytes(line.heldBytes)} · {tg.del.space.people(line.members, line.groups, line.drafts)}
        </span>
      </li>
    )
  }
  if (isUserPlan(line)) {
    return (
      <li data-slot="plan-line">
        <span className="font-medium">{line.user.username}</span>{' '}
        <span className="text-muted-foreground">
          {tg.bulk.perSpace(tg.del.user.recycle, String(line.recycle.length))} ·{' '}
          {tg.bulk.perSpace(tg.del.user.transfer, String(line.transfer.length))} ·{' '}
          {tg.bulk.perSpace(tg.del.user.leave, String(line.leave.length))}
        </span>
      </li>
    )
  }
  const l = line as Record<string, unknown>
  const name =
    (l.name as string | undefined) ??
    (l.space as { name?: string } | undefined)?.name ??
    (l.user as { username?: string } | undefined)?.username ??
    ''
  return (
    <li data-slot="plan-line">
      <span className="font-medium">{name}</span>
      {typeof l.bytes === 'number' && <span className="text-muted-foreground"> · {formatBytes(l.bytes)}</span>}
    </li>
  )
}

export function JobView({ job }: { job: BulkJob }) {
  const done = job.state !== 'queued' && job.state !== 'running'
  const bad = job.failed
  return (
    <div className="grid gap-3" data-slot="job" data-state={job.state}>
      <p role="status" className="text-sm">
        {tg.bulk.states[job.state]}
      </p>
      <div
        className="h-2 overflow-hidden rounded-full bg-muted"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={job.total}
        aria-valuenow={job.done}
      >
        <div
          className="h-full bg-primary transition-all"
          style={{
            width: `${job.total ? Math.round((job.done / job.total) * 100) : 0}%`,
          }}
        />
      </div>
      <p className="text-xs text-muted-foreground tabular-nums">{tg.bulk.progress(job.done, job.total)}</p>
      {done && job.state === 'done' && (
        <p className="text-sm">
          {bad.length === 0 ? tg.bulk.doneAll(job.total) : tg.bulk.doneSome(job.total - bad.length, bad.length)}
        </p>
      )}
      {done && job.state !== 'done' && job.total - job.done > 0 && (
        <p className="text-sm text-destructive">{tg.bulk.notDone(job.total - job.done)}</p>
      )}
      {bad.length > 0 && (
        <div
          role="alert"
          className="grid gap-1 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm"
          data-slot="job-failed"
        >
          <p className="font-medium">{tg.bulk.failedItems}</p>
          <ul className="list-disc pl-5 text-xs">
            {bad.map((f) => (
              <li key={`${f.id}:${f.code}`}>
                {f.id ? `${f.id}: ` : ''}
                {(tg.errors as Record<string, string>)[f.code ?? ''] ?? f.error}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

// The second confirmation of an action on many things (or a purge): what it will do, the number of items typed, then
// the job with its progress. `names` says what each id is, for the failures.
export function BulkDialog({
  action,
  request,
  onClose,
  label,
}: {
  action: ConfirmAction
  request: Omit<ConfirmRequest, 'action'>
  onClose: () => void
  label: string
}) {
  const confirm = useConfirm()
  const start = useStartBulk()
  const [plan, setPlan] = useState<ConfirmResult | null>(null)
  const [error, setError] = useState('')
  const [stale, setStale] = useState(false)
  const [typed, setTyped] = useState('')
  const [heir, setHeir] = useState<User | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const { mutate: ask } = confirm

  const look = useCallback(
    (withHeir?: string) => {
      setError('')
      setStale(false)
      setTyped('')
      ask(
        { action, ...request, ...(withHeir ? { heir: withHeir } : {}) },
        {
          onSuccess: setPlan,
          onError: (e) => setError(manageError(e, tg.bulk.checkFailed)),
        },
      )
    },
    // The request is the caller's selection, fixed while the dialog is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [action, ask],
  )
  useEffect(() => {
    ask(
      { action, ...request },
      {
        onSuccess: setPlan,
        onError: (e) => setError(manageError(e, tg.bulk.checkFailed)),
      },
    )
    // The request is the caller's selection, fixed while the dialog is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [action, ask])

  // What the deletion preview of the first line says the bin keeps things for.
  const first = plan?.plan[0]
  const days = first && 'recycleDays' in first && typeof first.recycleDays === 'number' ? first.recycleDays : null
  const blockers = plan?.blockers ?? []
  const needs = plan?.needsHeir ?? []
  const blocked = blockers.length > 0 || (needs.length > 0 && !heir)
  const matches = plan !== null && typed.trim() === String(plan.count)

  const go = () => {
    if (!plan || !matches || blocked || start.isPending) return
    setError('')
    start.mutate(plan.confirm, {
      onSuccess: (r) => setJobId(r.job.id),
      onError: (e) => {
        if (e instanceof ApiError && e.code === 'confirm_stale') setStale(true)
        else if (e instanceof ApiError && e.status === 404) setError(tg.bulk.expired)
        else setError(manageError(e))
      },
    })
  }

  const running = jobId !== null
  const finished = job.data && job.data.state !== 'queued' && job.data.state !== 'running'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg" data-slot="bulk-dialog">
        <DialogHeader>
          <DialogTitle>
            {tg.bulk.title(label, plan?.count ?? (request.spaces ?? request.users ?? request.items ?? []).length)}
          </DialogTitle>
          <DialogDescription>
            {action === 'delete_spaces' || action === 'delete_users' ? tg.bulk.deleteNote(days) : tg.bulk.plan}
          </DialogDescription>
        </DialogHeader>
        {running ? (
          job.data ? (
            <JobView job={job.data} />
          ) : (
            <Spinner />
          )
        ) : confirm.isPending && !plan ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner /> {tg.bulk.checking}
          </p>
        ) : plan ? (
          <div className="grid gap-4">
            <ul className="grid max-h-56 gap-1.5 overflow-auto rounded-lg border p-3 text-sm" data-slot="plan">
              {plan.plan.map((l, i) => (
                <PlanLine key={i} line={l} />
              ))}
            </ul>
            {plan.bytes > 0 && (
              <p className="text-xs text-muted-foreground">{tg.bulk.bytes(formatBytes(plan.bytes))}</p>
            )}
            {blockers.length > 0 && (
              <ul role="alert" className="grid gap-1 text-sm text-destructive" data-slot="blockers">
                {blockers.map((b, i) => (
                  <li key={i}>{(tg.bulk.blockers as Record<string, string>)[b.reason] ?? b.reason}</li>
                ))}
              </ul>
            )}
            {needs.length > 0 && (
              <div className="grid gap-2" data-slot="needs-heir">
                <p className="text-sm">
                  {tg.bulk.needsHeirFor}: {needs.flatMap((n) => n.spaces.map((s) => s.name)).join('、')}
                </p>
                <UserPicker
                  label={tg.bulk.heir}
                  value={heir}
                  onChange={(u) => {
                    setHeir(u)
                    if (u) look(u.id)
                  }}
                />
                <p className="text-xs text-muted-foreground">{tg.bulk.heirHelp}</p>
              </div>
            )}
            {stale && (
              <div
                role="alert"
                className="grid gap-2 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm"
                data-slot="stale"
              >
                <p>{tg.bulk.stale}</p>
                <Button type="button" variant="outline" size="sm" className="w-fit" onClick={() => look(heir?.id)}>
                  {tg.bulk.lookAgain}
                </Button>
              </div>
            )}
            <div className="grid gap-1.5">
              <Label htmlFor="bulk-count">{tg.bulk.typeCount(plan.count)}</Label>
              <Input
                id="bulk-count"
                inputMode="numeric"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
              />
              <p className="text-xs text-muted-foreground">{tg.bulk.typeCountHelp(plan.count)}</p>
            </div>
          </div>
        ) : null}
        <FormMessage>{error}</FormMessage>
        {error && !plan && (
          <Button type="button" variant="outline" size="sm" className="w-fit" onClick={() => look()}>
            {tg.retry}
          </Button>
        )}
        <DialogFooter>
          {running ? (
            <Button type="button" onClick={onClose} disabled={!finished}>
              {tg.bulk.close}
            </Button>
          ) : (
            <>
              <Button type="button" variant="outline" onClick={onClose}>
                {tg.cancel}
              </Button>
              <Button
                type="button"
                variant={action.startsWith('delete') || action === 'purge' ? 'destructive' : 'default'}
                disabled={!plan || !matches || blocked || start.isPending || stale}
                onClick={go}
              >
                {start.isPending && <Spinner />}
                {start.isPending ? tg.bulk.starting : tg.bulk.confirm}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
