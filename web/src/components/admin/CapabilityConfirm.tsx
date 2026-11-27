import { useEffect, useId, useState, type ReactNode } from 'react'
import { ApiError } from '@/api/client'
import { useConfirm } from '@/api/manage'
import type { CapabilityPlanLine, ConfirmResult } from '@/api/types'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { tdom } from '@/i18n/domains'
import { featureName } from '@/i18n/featureNames'

// Switching off a feature that is in use (docs/api.md, change_capabilities): the change was refused with
// 409 confirm_required, so the preview is asked for (what each organization would lose), the administrator types the
// number of organizations, and the same change is sent again with the confirmation. Nothing stored is deleted.

const tc = tdom.capability

// A change that was refused for want of the confirmation: the feature it switches off, the tenant of a rule or of a
// plan's assignment (none: the site's value, or a plan's change), and how to send it again with the token.
export interface CapabilityChange {
  key: string
  tenant?: string
  send: (confirm: string) => Promise<unknown>
}

export const needsCapabilityConfirm = (e: unknown) => e instanceof ApiError && e.code === 'confirm_required'

// The feature a refusal names (its preview's lines), for a change that is not of one setting (a plan).
export function refusedFeature(e: unknown): string {
  const body = e instanceof ApiError ? (e.body as { plan?: { feature?: string }[] } | undefined) : undefined
  return body?.plan?.[0]?.feature ?? 'features.domains'
}

// The dialog, and the function that opens it with a change that was refused for want of the confirmation. onDone is
// told when the change went through.
export function useCapabilityConfirm(onDone: () => void): { ask: (c: CapabilityChange) => void; dialog: ReactNode } {
  const [change, setChange] = useState<CapabilityChange | null>(null)
  const dialog = (
    <Dialog open={change !== null} onOpenChange={(open) => !open && setChange(null)}>
      <DialogContent>
        {change && (
          <ConfirmBody
            key={`${change.tenant ?? ''}/${change.key}`}
            change={change}
            onClose={(done) => {
              setChange(null)
              if (done) onDone()
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
  return { ask: setChange, dialog }
}

function ConfirmBody({ change, onClose }: { change: CapabilityChange; onClose: (done: boolean) => void }) {
  const confirm = useConfirm()
  const [sending, setSending] = useState(false)
  const [plan, setPlan] = useState<ConfirmResult | null>(null)
  const [typed, setTyped] = useState('')
  const [error, setError] = useState('')
  const id = useId()
  const { mutate } = confirm
  useEffect(() => {
    mutate(
      { action: 'change_capabilities', key: change.key, ...(change.tenant ? { tenant: change.tenant } : {}) },
      { onSuccess: setPlan, onError: (e) => setError(e instanceof ApiError ? e.message : tc.failed) },
    )
  }, [mutate, change.key, change.tenant])
  const lines = (plan?.plan ?? []) as CapabilityPlanLine[]
  const n = lines.length
  const go = () => {
    if (!plan) return
    setError('')
    setSending(true)
    change.send(plan.confirm).then(
      () => onClose(true),
      (e: unknown) => {
        setSending(false)
        setError(e instanceof ApiError ? e.message : tc.failed)
      },
    )
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>{tc.title(featureName(change.key) ?? change.key)}</DialogTitle>
        <DialogDescription>{tc.help}</DialogDescription>
      </DialogHeader>
      {!plan && !error && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          {tc.loading}
        </p>
      )}
      {plan && (
        <ul className="grid max-h-64 min-w-0 gap-1 overflow-y-auto text-sm" data-slot="capability-plan">
          {lines.map((l) => (
            <li key={l.tenant} className="min-w-0 break-words" data-slot="plan-line">
              <span className="font-medium">{l.name}</span>{' '}
              <span className="text-muted-foreground">
                ({l.slug}) ·{' '}
                {l.providers !== undefined
                  ? tc.providersLine(l.providers, l.enabled ?? 0)
                  : tc.line(l.domains ?? 0, l.verified ?? 0)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {plan && (
        <div className="grid gap-1.5">
          <Label htmlFor={id}>{tc.typeLabel(n)}</Label>
          <Input
            id={id}
            inputMode="numeric"
            autoComplete="off"
            className="text-base sm:text-sm"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
          />
        </div>
      )}
      <FormMessage>{error}</FormMessage>
      <DialogFooter>
        <Button type="button" variant="outline" className="h-10 sm:h-9" onClick={() => onClose(false)}>
          {tc.cancel}
        </Button>
        <Button
          type="button"
          variant="destructive"
          className="h-10 sm:h-9"
          disabled={!plan || sending || typed.trim() !== String(n)}
          onClick={go}
        >
          {tc.confirm}
        </Button>
      </DialogFooter>
    </>
  )
}
