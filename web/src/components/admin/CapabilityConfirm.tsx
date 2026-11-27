import { useEffect, useId, useState, type ReactNode } from 'react'
import { usePutSetting } from '@/api/admin'
import { ApiError } from '@/api/client'
import { useConfirm } from '@/api/manage'
import type { CapabilityPlanLine, ConfirmResult, RuleWrite, SettingLower } from '@/api/types'
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

// The change of a setting as usePutSetting takes it.
export interface SettingChange {
  tenant?: string
  key: string
  value?: unknown
  lower?: SettingLower | null
  siteLock?: boolean
  siteRule?: RuleWrite | null
}

export const needsCapabilityConfirm = (e: unknown) => e instanceof ApiError && e.code === 'confirm_required'

// The dialog, and the function that opens it with a change that was refused for want of the confirmation. onDone is
// told when the change went through.
export function useCapabilityConfirm(onDone: () => void): { ask: (c: SettingChange) => void; dialog: ReactNode } {
  const [change, setChange] = useState<SettingChange | null>(null)
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

function ConfirmBody({ change, onClose }: { change: SettingChange; onClose: (done: boolean) => void }) {
  const confirm = useConfirm()
  const put = usePutSetting()
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
    put.mutate(
      { ...change, confirm: plan.confirm },
      {
        onSuccess: () => onClose(true),
        onError: (e) => setError(e instanceof ApiError ? e.message : tc.failed),
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
                ({l.slug}) · {tc.line(l.domains, l.verified)}
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
          disabled={!plan || put.isPending || typed.trim() !== String(n)}
          onClick={go}
        >
          {tc.confirm}
        </Button>
      </DialogFooter>
    </>
  )
}
