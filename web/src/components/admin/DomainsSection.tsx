import { useEffect, useId, useState, type FormEvent } from 'react'
import { Copy, Plus, RefreshCw, ShieldCheck, Trash2, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'
import { useDomainChange, useTenantDomains, useTenantSettings } from '@/api/admin'
import { ApiError } from '@/api/client'
import type { AuthProvider, DomainCheck, TenantDomain } from '@/api/types'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { tdom } from '@/i18n/domains'
import { absoluteTime, relativeTime } from '@/lib/time'

// A tenant's claimed email domains (3.6 d2): claim one, publish the record it shows, check it, keep others from making
// organisations on it, remove it. One component for the site's page of a tenant (its Sign-in tab) and the tenant's own
// console. The code is shown only by the answer that made it (the server keeps a hash), so the records are shown
// while the page holds that answer, and a new code brings them back.

const td = tdom

// The client already put the refusal in the page's words (apiErrors).
const explain = (e: unknown): string => (e instanceof ApiError ? e.message || td.failed : td.failed)

// Whole seconds until a moment, 0 once it is past.
const secondsUntil = (at: number, now: number) => Math.max(0, Math.ceil((at - now) / 1000))

// A clock that ticks each second until the deadline, and stands still after it; refresh reads it again at once (when a
// new deadline is set from an answer).
function useNowUntil(deadline: number): [number, () => void] {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (deadline <= now) return
    const t = setTimeout(() => setNow(Date.now()), Math.min(1000, deadline - now))
    return () => clearTimeout(t)
  }, [deadline, now])
  return [now, () => setNow(Date.now())]
}

// `providers`: the tenant's own sign-in providers a verified domain may be routed to (3.6 d3); left out while the
// organisation has no single sign-on of its own, and then no routing is shown.
export function DomainsSection({ tenant, providers }: { tenant: string; providers?: AuthProvider[] }) {
  const q = useTenantDomains(tenant)
  const settings = useTenantSettings(tenant)
  const off = settings.data?.settings.find((s) => s.key === 'features.domains')?.effective === false
  // When "Check now" may be asked again, by domain (from the answers and a 429's Retry-After), in ms.
  const [cooldown, setCooldown] = useState<Record<string, number>>({})
  const until = (d: TenantDomain) =>
    Math.max(d.nextCheckAllowedAt ? Date.parse(d.nextCheckAllowedAt) : 0, cooldown[d.domain] ?? 0)
  const [now, refresh] = useNowUntil(Math.max(0, ...(q.data?.domains ?? []).map(until)))

  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={td.loadFailed} onRetry={() => void q.refetch()} />
  const cool = (d: TenantDomain) => secondsUntil(until(d), now)
  return (
    <SettingsSection title={td.title} description={td.help} id={`domains-${tenant}`}>
      {off && (
        <p role="status" className="text-sm break-words text-muted-foreground" data-slot="feature-off">
          {td.featureOff}
        </p>
      )}
      <ClaimForm tenant={tenant} />
      {q.data.domains.length === 0 ? (
        <p className="text-sm text-muted-foreground" data-slot="domains-empty">
          {td.empty}
        </p>
      ) : (
        <ul className="grid min-w-0 gap-3" data-slot="domains">
          {q.data.domains.map((d) => (
            <DomainCard
              key={d.domain}
              tenant={tenant}
              d={d}
              providers={providers}
              wait={cool(d)}
              onCooldown={(at) => {
                setCooldown((c) => ({ ...c, [d.domain]: at }))
                refresh()
              }}
            />
          ))}
        </ul>
      )}
    </SettingsSection>
  )
}

function ClaimForm({ tenant }: { tenant: string }) {
  const change = useDomainChange(tenant)
  const [domain, setDomain] = useState('')
  const [error, setError] = useState('')
  const id = useId()
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    change.mutate(
      { op: 'claim', domain: domain.trim() },
      {
        onSuccess: (d) => {
          setDomain('')
          if (d) toast.success(td.add.added(d.display))
        },
        onError: (err) => setError(explain(err)),
      },
    )
  }
  return (
    <form onSubmit={submit} className="grid min-w-0 gap-1.5" data-slot="domain-claim">
      <Label htmlFor={id}>{td.add.label}</Label>
      <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
        <Input
          id={id}
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          placeholder={td.add.placeholder}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          inputMode="url"
          className="min-w-0 text-base sm:max-w-sm sm:text-sm"
          aria-invalid={error ? true : undefined}
        />
        <Button type="submit" className="h-10 sm:h-9" disabled={change.isPending || domain.trim() === ''}>
          <Plus aria-hidden="true" />
          {change.isPending ? td.add.adding : td.add.submit}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </form>
  )
}

// The warning of a domain whose label mixes scripts, with the reason as its title.
export function MixedBadge({ d }: { d: TenantDomain }) {
  if (!d.mixedScript) return null
  return (
    <Badge variant="destructive" data-slot="mixed-script" title={td.mixedHelp}>
      <TriangleAlert aria-hidden="true" />
      {td.mixedBadge}
    </Badge>
  )
}

export function StateBadge({ d }: { d: TenantDomain }) {
  switch (d.state) {
    case 'verified':
      return (
        <Badge data-slot="domain-state" data-state="verified">
          <ShieldCheck aria-hidden="true" />
          {td.states.verified}
        </Badge>
      )
    case 'missing':
      return (
        <Badge variant="destructive" data-slot="domain-state" data-state="missing">
          {td.states.missing(d.missingSince ? absoluteTime(d.missingSince) : '')}
        </Badge>
      )
    case 'lapsed':
      return (
        <Badge variant="destructive" data-slot="domain-state" data-state="lapsed">
          {td.states.lapsed}
        </Badge>
      )
    default:
      return (
        <Badge variant="outline" data-slot="domain-state" data-state="unverified">
          {td.states.unverified}
        </Badge>
      )
  }
}

function CopyButton({ text, what }: { text: string; what: string }) {
  const copy = () =>
    navigator.clipboard.writeText(text).then(
      () => toast.success(td.records.copied),
      () => toast.error(td.records.copyFailed),
    )
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="size-10 shrink-0 lg:size-8"
      aria-label={td.records.copy(what)}
      title={td.records.copy(what)}
      onClick={() => void copy()}
    >
      <Copy aria-hidden="true" />
    </Button>
  )
}

// One field of a record: its label, the text in a monospace box that wraps anywhere, and a copy button.
function RecordField({ label, text }: { label: string; text: string }) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="flex min-w-0 items-center gap-1 rounded-md border bg-muted/40 pl-2">
        <code className="min-w-0 flex-1 py-1.5 font-mono text-xs break-all select-all" data-slot="record-text">
          {text}
        </code>
        <CopyButton text={text} what={label} />
      </div>
    </div>
  )
}

function Records({ d }: { d: TenantDomain }) {
  if (!d.records) {
    return (
      <div className="grid min-w-0 gap-1" data-slot="domain-record-name">
        <RecordField label={td.hidden.title} text={d.recordName} />
        <p className="text-xs text-muted-foreground">{td.hidden.help}</p>
      </div>
    )
  }
  const { txt, cname } = d.records
  return (
    <div className="grid min-w-0 gap-3 rounded-lg border p-3" data-slot="domain-records">
      <div>
        <p className="text-sm font-medium">{td.records.title}</p>
        <p className="text-xs text-muted-foreground">{td.records.help}</p>
      </div>
      <div className="grid min-w-0 gap-2" data-record="txt">
        <p className="text-xs font-medium">{td.records.txtFirst}</p>
        <RecordField label={td.records.name} text={txt.name} />
        <RecordField label={td.records.value} text={txt.value} />
      </div>
      {cname && (
        <div className="grid min-w-0 gap-2" data-record="cname">
          <p className="text-xs font-medium">{td.records.cname}</p>
          <RecordField label={td.records.name} text={cname.name} />
          <RecordField label={td.records.value} text={cname.target} />
        </div>
      )}
    </div>
  )
}

function CheckLine({ c }: { c: DomainCheck }) {
  const via = c.via === 'cname' ? 'CNAME' : 'TXT'
  const text =
    c.result === 'found'
      ? td.check.results.found(via)
      : c.result === 'missing'
        ? td.check.results.missing
        : td.check.results[c.result]
  return (
    <div className="grid min-w-0 gap-0.5 text-xs" data-slot="domain-check" data-result={c.result}>
      <p className="text-muted-foreground">{td.check.last(relativeTime(c.at), td.check.by[c.by])}</p>
      <p className={c.result === 'found' ? '' : 'text-destructive'}>{text}</p>
      {c.result === 'wrong_value' &&
        c.seen.map((v) => (
          <code key={v} className="font-mono break-all text-muted-foreground">
            {v}
          </code>
        ))}
      {c.result === 'dns_error' && c.error && (
        <code className="font-mono break-all text-muted-foreground">{c.error}</code>
      )}
    </div>
  )
}

function DomainCard({
  tenant,
  d,
  providers,
  wait,
  onCooldown,
}: {
  tenant: string
  d: TenantDomain
  providers?: AuthProvider[]
  wait: number
  onCooldown: (until: number) => void
}) {
  const change = useDomainChange(tenant)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const [removing, setRemoving] = useState(false)
  const restrictId = useId()
  const routeId = useId()
  const held = d.state === 'verified' || d.state === 'missing'
  const busy = change.isPending
  const fail = (e: unknown) => {
    if (e instanceof ApiError && e.code === 'rate_limited') onCooldown(Date.now() + (e.retryAfter ?? 60) * 1000)
    setMessage({ tone: 'error', text: explain(e) })
  }
  const check = () => {
    setMessage(null)
    change.mutate(
      { op: 'verify', domain: d.domain },
      {
        onSuccess: (r) => {
          onCooldown(Date.now() + 60_000)
          if (r && r.state === 'verified' && !held) toast.success(td.check.verified(r.display))
        },
        onError: fail,
      },
    )
  }
  const renew = () => {
    setMessage(null)
    change.mutate(
      { op: 'code', domain: d.domain },
      { onSuccess: () => setMessage({ tone: 'info', text: td.code.renewed }), onError: fail },
    )
  }
  const restrict = (on: boolean) => {
    setMessage(null)
    change.mutate(
      { op: 'restrict', domain: d.domain, restrictOrgs: on },
      { onSuccess: () => setMessage({ tone: 'info', text: on ? td.restrict.on : td.restrict.off }), onError: fail },
    )
  }
  const route = (provider: string | null) => {
    setMessage(null)
    change.mutate(
      { op: 'route', domain: d.domain, provider },
      { onSuccess: () => setMessage({ tone: 'info', text: td.route.saved }), onError: fail },
    )
  }
  const remove = () =>
    change.mutate(
      { op: 'delete', domain: d.domain },
      { onSuccess: () => toast.success(td.remove.done(d.display)), onError: fail },
    )
  return (
    <li
      className="grid min-w-0 gap-3 rounded-lg border p-4"
      data-slot="domain"
      data-domain={d.domain}
      data-state={d.state}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="min-w-0 text-sm font-medium break-all">{d.display}</span>
        <StateBadge d={d} />
        {d.restrictOrgs && <Badge variant="secondary">{td.restrictedBadge}</Badge>}
        <MixedBadge d={d} />
      </div>
      {d.display !== d.domain && (
        <p className="-mt-2 font-mono text-xs break-all text-muted-foreground">{td.punycode(d.domain)}</p>
      )}
      {d.state === 'lapsed' && <p className="text-xs text-destructive">{td.check.lapsedHelp}</p>}
      {d.state === 'missing' && <p className="text-xs text-destructive">{td.check.missingHelp}</p>}
      {!held && <Records d={d} />}
      {!held && d.codeExpires && (
        <p
          className={`text-xs ${d.codeExpired ? 'text-destructive' : 'text-muted-foreground'}`}
          data-slot="domain-code"
        >
          {d.codeExpired ? td.code.expired : td.code.validUntil(absoluteTime(d.codeExpires))}
        </p>
      )}
      {d.lastCheck && <CheckLine c={d.lastCheck} />}
      {held && d.nextCheckAt && (
        <p className="text-xs text-muted-foreground">{td.check.nextDaily(absoluteTime(d.nextCheckAt))}</p>
      )}
      <div className="grid min-w-0 gap-1">
        <label htmlFor={restrictId} className="flex min-h-10 min-w-0 items-start gap-2 text-sm lg:min-h-0">
          <Switch
            id={restrictId}
            className="mt-0.5 shrink-0"
            checked={d.restrictOrgs}
            disabled={busy}
            onCheckedChange={restrict}
          />
          <span className="min-w-0 break-words">{td.restrict.label}</span>
        </label>
        <p className="text-xs text-muted-foreground">{td.restrict.help}</p>
      </div>
      {held && providers && providers.length > 0 && (
        <div className="grid min-w-0 gap-1.5" data-slot="domain-route">
          <Label htmlFor={routeId}>{td.route.short}</Label>
          <NativeSelect
            id={routeId}
            aria-label={td.route.label(d.display)}
            className="w-full sm:max-w-xs"
            value={d.provider ?? ''}
            disabled={busy}
            onChange={(e) => route(e.target.value === '' ? null : e.target.value)}
          >
            <option value="">{td.route.none}</option>
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{td.route.help}</p>
        </div>
      )}
      <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:flex-wrap">
        <Button
          type="button"
          size="sm"
          className="h-10 sm:h-8"
          disabled={busy || wait > 0 || (!held && d.codeExpired)}
          onClick={check}
          data-slot="check-now"
          aria-live="polite"
        >
          <RefreshCw aria-hidden="true" />
          {busy ? td.check.checking : wait > 0 ? td.check.again(wait) : td.check.now}
        </Button>
        {!held && (
          <Button type="button" variant="outline" size="sm" className="h-10 sm:h-8" disabled={busy} onClick={renew}>
            {busy ? td.code.renewing : td.code.renew}
          </Button>
        )}
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-10 sm:ml-auto sm:h-8"
          disabled={busy}
          aria-label={td.remove.label(d.display)}
          onClick={() => setRemoving(true)}
        >
          <Trash2 aria-hidden="true" />
          {td.remove.action}
        </Button>
      </div>
      <AlertDialog open={removing} onOpenChange={setRemoving}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="break-all">{td.remove.title(d.display)}</AlertDialogTitle>
            <AlertDialogDescription>{held ? td.remove.verifiedBody : td.remove.body}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{td.remove.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={remove}>
              {td.remove.confirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}
