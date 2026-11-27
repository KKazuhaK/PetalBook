import { useId, useState } from 'react'
import { Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { useAuditPurgeCount, usePurgeAudit } from '@/api/admin'
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
  const validDay = before === '' || DAY.test(before)
  const input = { category, ...(before && validDay ? { before } : {}) }
  const count = useAuditPurgeCount(input, validDay)
  const purge = usePurgeAudit()
  const id = useId()
  const n = count.data?.count
  const label = category === 'all' ? tp.all.toLowerCase() : tsec.audit.categories[category]
  const run = () => {
    setError('')
    purge.mutate(input, {
      onSuccess: (r) => {
        toast.success(tp.done(r.count))
        onClose()
      },
      onError: (e) => setError(e instanceof ApiError && e.message ? e.message : tp.failed),
    })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !purge.isPending && onClose()}>
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
              disabled={purge.isPending}
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
              disabled={purge.isPending}
              aria-invalid={!validDay}
              onChange={(e) => setBefore(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">{tp.beforeHelp}</p>
          </div>
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
                  : tp.will(n, label, before)}
          </p>
          <FormMessage>{error}</FormMessage>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" disabled={purge.isPending} onClick={onClose}>
            {tp.cancel}
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={purge.isPending || !n || count.isFetching}
            onClick={run}
          >
            {purge.isPending ? tp.deleting : tp.confirm(n ?? 0)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
