import { useId, useState } from 'react'
import { ArrowRightLeft } from 'lucide-react'
import { toast } from 'sonner'
import { useAssignDomain, useSiteDomains, useTenants } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { TenantDomain } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
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
import { NativeSelect } from '@/components/ui/native-select'
import { tdom } from '@/i18n/domains'
import { MixedBadge, StateBadge } from './DomainsSection'

// The site's list of every organization's email domains (Tenants, Email domains), and the transfer of a verified one
// to another organization. A domain whose label mixes scripts carries a warning here and in the transfer dialog.

const ts = tdom.site

export function SiteDomains() {
  const q = useSiteDomains()
  const [moving, setMoving] = useState<TenantDomain | null>(null)
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={ts.loadFailed} onRetry={() => void q.refetch()} />
  const rows = q.data.pages.flatMap((p) => p.domains)
  return (
    <SettingsSection title={ts.title} description={ts.help} id="site-domains">
      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">{ts.empty}</p>
      ) : (
        <ul className="w-full min-w-0 divide-y rounded-lg border" data-slot="site-domains">
          {rows.map((d) => (
            <li
              key={`${d.domain}/${d.tenant?.id ?? ''}`}
              data-slot="site-domain"
              data-domain={d.domain}
              className="grid min-w-0 gap-2 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
            >
              <div className="grid min-w-0 gap-1">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="min-w-0 text-sm font-medium break-all">{d.display}</span>
                  <StateBadge d={d} />
                  <MixedBadge d={d} />
                </div>
                {d.display !== d.domain && (
                  <p className="font-mono text-xs break-all text-muted-foreground">{tdom.punycode(d.domain)}</p>
                )}
                <p className="text-xs break-words text-muted-foreground">
                  {ts.tenant(d.tenant?.name ?? d.tenant?.id ?? '')}
                </p>
              </div>
              {(d.state === 'verified' || d.state === 'missing') && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-10 max-sm:w-full sm:h-8"
                  aria-label={ts.transferLabel(d.display)}
                  onClick={() => setMoving(d)}
                >
                  <ArrowRightLeft aria-hidden="true" />
                  {ts.transfer}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {q.hasNextPage && (
        <Button
          type="button"
          variant="outline"
          className="h-10 w-full sm:h-9 sm:w-fit"
          disabled={q.isFetchingNextPage}
          onClick={() => void q.fetchNextPage()}
        >
          {ts.more}
        </Button>
      )}
      <Dialog open={moving !== null} onOpenChange={(open) => !open && setMoving(null)}>
        <DialogContent>
          {moving && <TransferForm key={moving.domain} d={moving} onClose={() => setMoving(null)} />}
        </DialogContent>
      </Dialog>
    </SettingsSection>
  )
}

function TransferForm({ d, onClose }: { d: TenantDomain; onClose: () => void }) {
  const tenants = useTenants()
  const assign = useAssignDomain()
  const [to, setTo] = useState('')
  const [error, setError] = useState('')
  const id = useId()
  const others = (tenants.data ?? []).filter((t) => t.id !== d.tenant?.id)
  const go = () => {
    setError('')
    assign.mutate(
      { domain: d.domain, tenant: to },
      {
        onSuccess: () => {
          toast.success(ts.done(d.display, others.find((t) => t.id === to)?.name ?? to))
          onClose()
        },
        onError: (e) => setError(e instanceof ApiError ? e.message : tdom.failed),
      },
    )
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle className="break-all">{ts.transferTitle(d.display)}</DialogTitle>
        <DialogDescription>{ts.transferHelp}</DialogDescription>
      </DialogHeader>
      {d.mixedScript && (
        <div className="grid gap-1 rounded-md border border-destructive/40 p-3" data-slot="transfer-mixed">
          <MixedBadge d={d} />
          <p className="text-sm break-words">{tdom.mixedHelp}</p>
        </div>
      )}
      <div className="grid gap-1.5">
        <Label htmlFor={id}>{ts.to}</Label>
        <NativeSelect id={id} value={to} onChange={(e) => setTo(e.target.value)} className="text-base sm:text-sm">
          <option value="">{ts.pick}</option>
          {others.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name} ({t.slug})
            </option>
          ))}
        </NativeSelect>
      </div>
      <FormMessage>{error}</FormMessage>
      <DialogFooter>
        <Button type="button" variant="outline" className="h-10 sm:h-9" onClick={onClose}>
          {ts.cancel}
        </Button>
        <Button type="button" className="h-10 sm:h-9" disabled={!to || assign.isPending} onClick={go}>
          {ts.confirm}
        </Button>
      </DialogFooter>
    </>
  )
}
