import { useId, useState } from 'react'
import { Check, X } from 'lucide-react'
import { toast } from 'sonner'
import { useJoinDecision, useJoinRequests } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { JoinRequest } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { tdom } from '@/i18n/domains'
import { absoluteTime, relativeTime } from '@/lib/time'

// A tenant's requests to join by email domain (3.6 d5): people who signed up with an address on one of its verified
// domains whose join is "request". An administrator approves (the account is made) or denies each; the decided ones
// show on asking. One component for the organisation's console and the site's page of a tenant.

const tr = tdom.requests

const explain = (e: unknown): string => (e instanceof ApiError ? e.message || tdom.failed : tdom.failed)

// `asking`: some verified domain of the tenant asks for approval. Without it, and with nothing waiting, nothing is shown.
export function JoinRequests({ tenant, asking }: { tenant: string; asking: boolean }) {
  const [all, setAll] = useState(false)
  const q = useJoinRequests(tenant, all)
  const switchId = useId()
  const items = q.data?.pages.flatMap((p) => p.items) ?? []
  if (!asking && !all && q.isSuccess && items.length === 0) return null
  return (
    <SettingsSection title={tr.title} description={tr.help} id={`join-requests-${tenant}`}>
      <label htmlFor={switchId} className="flex min-h-10 min-w-0 items-center gap-2 text-sm lg:min-h-0">
        <Switch id={switchId} className="shrink-0" checked={all} onCheckedChange={setAll} />
        <span className="min-w-0 break-words">{tr.showAll}</span>
      </label>
      {q.isPending ? (
        <PageFallback />
      ) : q.isError ? (
        <ErrorState message={tr.loadFailed} onRetry={() => void q.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-slot="join-requests-empty">
          {all ? tr.emptyAll : tr.empty}
        </p>
      ) : (
        <ul className="grid min-w-0 gap-3" data-slot="join-requests">
          {items.map((r) => (
            <RequestRow key={r.id} tenant={tenant} r={r} />
          ))}
        </ul>
      )}
      {q.hasNextPage && (
        <Button
          type="button"
          variant="outline"
          className="h-10 justify-self-start sm:h-9"
          disabled={q.isFetchingNextPage}
          onClick={() => void q.fetchNextPage()}
        >
          {tr.more}
        </Button>
      )}
    </SettingsSection>
  )
}

function RequestRow({ tenant, r }: { tenant: string; r: JoinRequest }) {
  const decide = useJoinDecision(tenant)
  const [denying, setDenying] = useState(false)
  const [error, setError] = useState('')
  const pending = r.state === 'pending'
  const go = (approve: boolean) => {
    setError('')
    decide.mutate(
      { id: r.id, approve },
      {
        onSuccess: () => toast.success(approve ? tr.approved(r.username) : tr.denied(r.username)),
        onError: (e) => setError(explain(e)),
      },
    )
  }
  return (
    <li className="grid min-w-0 gap-2 rounded-lg border p-4" data-slot="join-request" data-state={r.state}>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="min-w-0 text-sm font-medium break-words">{r.name}</span>
        <Badge variant={pending ? 'default' : 'outline'}>{tr.states[r.state]}</Badge>
      </div>
      <p className="min-w-0 text-xs break-all text-muted-foreground">{tr.line(r.username, r.email)}</p>
      <p className="text-xs text-muted-foreground">
        {tr.asked(relativeTime(r.created))}
        {pending && ` · ${tr.expires(absoluteTime(r.expires))}`}
        {!pending && r.decidedAt && ` · ${tr.decided(absoluteTime(r.decidedAt), r.decidedBy?.name ?? '')}`}
      </p>
      <FormMessage>{error}</FormMessage>
      {pending && (
        <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
          <Button
            type="button"
            size="sm"
            className="h-10 sm:h-8"
            disabled={decide.isPending}
            aria-label={tr.approveLabel(r.username)}
            onClick={() => go(true)}
          >
            <Check aria-hidden="true" />
            {tr.approve}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-10 sm:h-8"
            disabled={decide.isPending}
            aria-label={tr.denyLabel(r.username)}
            onClick={() => setDenying(true)}
          >
            <X aria-hidden="true" />
            {tr.deny}
          </Button>
        </div>
      )}
      <AlertDialog open={denying} onOpenChange={setDenying}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="break-words">{tr.denyTitle(r.username)}</AlertDialogTitle>
            <AlertDialogDescription>{tr.denyBody}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tr.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => go(false)}>
              {tr.deny}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}
