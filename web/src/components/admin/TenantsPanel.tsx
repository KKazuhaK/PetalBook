import { useState, type FormEvent } from 'react'
import { CircleCheck, Copy, Package, Plus, Save, Trash2 } from 'lucide-react'
import { ApiError } from '@/api/client'
import {
  useCreateTenant,
  useDeleteTenant,
  usePlans,
  usePutTenantPlan,
  useSetTenancy,
  useTenancy,
  useTenants,
  useTenantSettings,
  useUpdateTenant,
} from '@/api/admin'
import type { CreatedTenant, Tenant } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { AdminPageActions } from './AdminPageHeader'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { TenantRulesBlock, lowerText, showValue, windowText } from './TenantRules'
import { nameOf } from './RuleDialog'
import { NativeSelect } from '@/components/ui/native-select'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Switch } from '@/components/ui/switch'
import { tt } from '@/i18n/tenants'
import { tu } from '@/i18n/usage'
import { formatUsed } from '@/lib/usage'

const tl = tt.list

// What a refusal of a tenant call means, in plain words.
export function tenantError(e: unknown): string {
  if (!(e instanceof ApiError)) return tl.errors.other
  switch (e.code) {
    case 'exists':
      return tl.errors.exists
    case 'invalid':
      return tl.errors.invalid
    case 'tenants_off':
      return tl.errors.tenants_off
    case 'tenant_limit':
      return tl.errors.tenant_limit
    case 'default_tenant':
      return tl.errors.default_tenant
    case 'conflict':
      return tl.errors.conflict
    default:
      return e.message || tl.errors.other
  }
}

// The switch that lets a server have tenants besides the default one, and the
// tenants themselves. Site administrators only. While the switch is off and there
// is only the default tenant the list is not shown: there is nothing to manage.
export function TenantsPanel() {
  const tenancy = useTenancy()
  const setTenancy = useSetTenancy()
  const [error, setError] = useState('')
  if (tenancy.isPending) return <PageFallback />
  if (tenancy.isError) return <ErrorState message={tt.tenancy.loadFailed} onRetry={() => void tenancy.refetch()} />
  const { enabled, tenants } = tenancy.data
  return (
    <div className="grid gap-8">
      <SettingsSection title={tt.tenancy.title} id="tenancy-heading" description={tt.tenancy.help}>
        <FormMessage>{error}</FormMessage>
        <label className="flex items-start gap-3 rounded-lg border px-3 py-3">
          <Switch
            checked={enabled}
            disabled={setTenancy.isPending}
            aria-label={tt.tenancy.switchLabel}
            onCheckedChange={(on) => {
              setError('')
              setTenancy.mutate(on, { onError: () => setError(tt.tenancy.saveFailed) })
            }}
          />
          <span className="grid gap-0.5">
            <span className="text-sm font-medium">{tt.tenancy.switchLabel}</span>
            <span className="text-xs text-muted-foreground">{tt.tenancy.switchHelp}</span>
            <span className="text-xs text-muted-foreground" data-slot="tenant-count">
              {tt.tenancy.count(tenants)}
            </span>
          </span>
        </label>
        <p className="text-xs text-muted-foreground">{tt.tenancy.isolation}</p>
      </SettingsSection>
      {(enabled || tenants > 1) && <TenantList enabled={enabled} />}
    </div>
  )
}

function TenantList({ enabled }: { enabled: boolean }) {
  const list = useTenants()
  const [open, setOpen] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [made, setMade] = useState<CreatedTenant | null>(null)
  if (list.isPending) return <PageFallback />
  if (list.isError) return <ErrorState message={tl.loadFailed} onRetry={() => void list.refetch()} />
  return (
    <SettingsSection title={tl.title} id="tenants-heading">
      <div className="flex flex-wrap items-center gap-3">
        {!enabled && <p className="text-xs text-muted-foreground">{tl.off}</p>}
        <AdminPageActions>
          <Button type="button" disabled={!enabled} onClick={() => setCreating(true)}>
            <Plus aria-hidden="true" />
            {tl.create}
          </Button>
        </AdminPageActions>
      </div>
      {made?.invite && <InviteNotice tenant={made} onClose={() => setMade(null)} />}
      {list.data.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tl.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="tenant-list">
          {list.data.map((tn) => (
            <li key={tn.id} className="grid gap-3 px-3 py-2.5 transition-colors hover:bg-muted/40">
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                <div className="min-w-0 flex-1 basis-48">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-sm font-medium">{tn.name}</span>
                    {tn.default && <Badge variant="secondary">{tl.default}</Badge>}
                    {tn.disabled && <Badge variant="secondary">{tl.disabled}</Badge>}
                  </div>
                  <div className="truncate text-xs text-muted-foreground">{tn.slug}</div>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-expanded={open === tn.id}
                  aria-label={`${tl.manage}: ${tn.name}`}
                  onClick={() => setOpen(open === tn.id ? null : tn.id)}
                >
                  {open === tn.id ? tl.close : tl.manage}
                </Button>
              </div>
              {open === tn.id && <TenantDetails tenant={tn} onGone={() => setOpen(null)} />}
            </li>
          ))}
        </ul>
      )}
      {creating && (
        <CreateDialog
          onClose={() => setCreating(false)}
          onMade={(r) => {
            setCreating(false)
            setMade(r)
          }}
        />
      )}
    </SettingsSection>
  )
}

function InviteNotice({ tenant, onClose }: { tenant: CreatedTenant; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const link = `${window.location.origin}/invite#${tenant.invite!.token}`
  return (
    <div className="grid gap-2 rounded-lg border bg-muted/40 px-3 py-3" role="status" data-slot="tenant-invite">
      <p className="text-sm font-medium">{tl.created(tenant.name)}</p>
      <p className="text-xs text-muted-foreground">
        {tl.inviteTitle}. {tl.inviteHelp}
      </p>
      <code className="block overflow-x-auto rounded border bg-background px-2 py-1 text-xs break-all">{link}</code>
      <div className="flex gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => {
            void navigator.clipboard?.writeText(link).then(() => setCopied(true))
          }}
        >
          <Copy aria-hidden="true" />
          {copied ? tl.copied : tl.copy}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onClose}>
          {tl.close}
        </Button>
      </div>
    </div>
  )
}

function CreateDialog({ onClose, onMade }: { onClose: () => void; onMade: (t: CreatedTenant) => void }) {
  const create = useCreateTenant()
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [admin, setAdmin] = useState('')
  const [error, setError] = useState('')
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    const input = {
      name: name.trim(),
      slug: slug.trim(),
      ...(admin.trim() ? { admin: { username: admin.trim() } } : {}),
    }
    create.mutate(input, { onSuccess: onMade, onError: (err) => setError(tenantError(err)) })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{tl.createTitle}</DialogTitle>
            <DialogDescription>{tl.createHelp}</DialogDescription>
          </DialogHeader>
          <FormMessage>{error}</FormMessage>
          <Field
            label={tl.nameLabel}
            help={tl.nameHelp}
            value={name}
            maxLength={80}
            required
            onChange={(e) => setName(e.target.value)}
          />
          <Field
            label={tl.slugLabel}
            value={slug}
            maxLength={32}
            required
            autoCapitalize="none"
            spellCheck={false}
            onChange={(e) => setSlug(e.target.value.toLowerCase())}
          />
          <Field
            label={tl.adminLabel}
            help={tl.adminHelp}
            value={admin}
            autoCapitalize="none"
            spellCheck={false}
            onChange={(e) => setAdmin(e.target.value)}
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {tl.cancel}
            </Button>
            <Button type="submit" disabled={create.isPending || !name.trim() || !slug.trim()}>
              {create.isPending ? tl.creating : tl.create}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function TenantDetails({ tenant, onGone }: { tenant: Tenant; onGone: () => void }) {
  const update = useUpdateTenant()
  const del = useDeleteTenant()
  const [name, setName] = useState(tenant.name)
  const [slug, setSlug] = useState(tenant.slug)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const [confirm, setConfirm] = useState(false)
  const save = (e: FormEvent) => {
    e.preventDefault()
    setMessage(null)
    update.mutate(
      { id: tenant.id, name: name.trim(), ...(tenant.default ? {} : { slug: slug.trim() }) },
      {
        onSuccess: () => setMessage({ tone: 'info', text: tl.saved }),
        onError: (err) => setMessage({ tone: 'error', text: tenantError(err) }),
      },
    )
  }
  const flip = () => {
    setMessage(null)
    update.mutate(
      { id: tenant.id, disabled: !tenant.disabled },
      { onError: (err) => setMessage({ tone: 'error', text: tenantError(err) }) },
    )
  }
  const remove = () => {
    del.mutate(tenant.id, {
      onSuccess: onGone,
      onError: (err) => {
        setConfirm(false)
        const b =
          err instanceof ApiError
            ? (err.body as { users?: number; spaces?: number; groups?: number; invites?: number } | undefined)
            : undefined
        setMessage({
          tone: 'error',
          text:
            err instanceof ApiError && err.code === 'tenant_not_empty'
              ? tl.notEmpty(b?.users ?? 0, b?.spaces ?? 0, b?.groups ?? 0, b?.invites ?? 0)
              : tenantError(err),
        })
      },
    })
  }
  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4 border-t pt-3">
      <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      <form onSubmit={save} className="grid gap-3 sm:grid-cols-2">
        <Field label={tl.nameLabel} value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
        <Field
          label={tl.slugLabel}
          value={slug}
          maxLength={32}
          disabled={tenant.default}
          autoCapitalize="none"
          spellCheck={false}
          onChange={(e) => setSlug(e.target.value.toLowerCase())}
        />
        <div className="sm:col-span-2">
          <Button type="submit" size="sm" disabled={update.isPending || !name.trim()}>
            <Save aria-hidden="true" />
            {update.isPending ? tl.saving : tl.save}
          </Button>
        </div>
      </form>
      <TenantUsageBlock tenant={tenant} />
      <TenantPlanBlock tenant={tenant} />
      {!tenant.default && (
        <div className="flex flex-wrap items-start gap-3">
          <div className="grid max-w-prose gap-1">
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={update.isPending}
              onClick={flip}
              className="w-fit"
            >
              <CircleCheck aria-hidden="true" />
              {tenant.disabled ? tl.enable : tl.disable}
            </Button>
            <p className="text-xs text-muted-foreground">{tl.disableHelp}</p>
          </div>
          <Button type="button" size="sm" variant="destructive" className="ml-auto" onClick={() => setConfirm(true)}>
            <Trash2 aria-hidden="true" />
            {tl.delete}
          </Button>
        </div>
      )}
      {confirm && (
        <Dialog open onOpenChange={(o) => !o && setConfirm(false)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{tl.deleteTitle(tenant.name)}</DialogTitle>
              <DialogDescription>{tl.deleteHelp}</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setConfirm(false)}>
                {tl.cancel}
              </Button>
              <Button type="button" variant="destructive" disabled={del.isPending} onClick={remove}>
                {del.isPending ? tl.deleting : tl.delete}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

// What the tenant uses (measured), and under it the limits sheet that holds the totals it is held to. Only a site
// administrator opens this page.
function TenantUsageBlock({ tenant }: { tenant: Tenant }) {
  const u = tenant.usage
  return (
    <>
      <div className="grid gap-3 border-t pt-3" data-slot="tenant-usage-block">
        <div className="grid gap-0.5">
          <h4 className="text-sm font-medium">{tu.tenant.usageTitle}</h4>
          <p className="text-xs text-muted-foreground">{tu.tenant.usageHelp}</p>
        </div>
        <dl className="grid min-w-0 grid-cols-2 gap-x-4 gap-y-1 text-sm sm:grid-cols-4" data-slot="tenant-numbers">
          {(['people', 'spaces', 'bytes', 'pages'] as const).map((k) => (
            <div key={k} className={`min-w-0 ${u.over.includes(k) ? 'text-destructive' : ''}`} data-quota={k}>
              <dt className="text-xs text-muted-foreground">{tu.quotas[k]}</dt>
              <dd className="tabular-nums break-words">{formatUsed(k, u[k])}</dd>
            </div>
          ))}
        </dl>
        {u.over.length > 0 && (
          <p className="text-xs text-destructive">{tu.tenant.over(u.over.map((k) => tu.quotas[k]).join('、'))}</p>
        )}
      </div>
      <TenantRulesBlock tenant={tenant} />
    </>
  )
}

// The tenant's plan, and the way to change it. A change of plan clears every manual rule of the tenant, so the dialog that
// confirms it lists them, and what the server cleared is listed after.
function TenantPlanBlock({ tenant }: { tenant: Tenant }) {
  const tp = tt.plans.tenantPlan
  const plans = usePlans()
  const sheet = useTenantSettings(tenant.id)
  const put = usePutTenantPlan()
  const current = tenant.plan?.slug ?? ''
  const [picked, setPicked] = useState(current)
  const [asking, setAsking] = useState(false)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  // The manual rules the change will clear, as the tenant has them now.
  const manual = (sheet.data?.settings ?? [])
    .map((s) => ({ s, rule: s.tenant?.rules?.find((r) => r.source === 'manual') }))
    .filter((x) => !!x.rule)
  const names = new Map((plans.data ?? []).map((p) => [p.slug, p.name]))
  const target = picked ? (names.get(picked) ?? picked) : tp.none
  const change = () => {
    put.mutate(
      { tenant: tenant.id, plan: picked || null },
      {
        onSuccess: (r) => {
          setAsking(false)
          const head = r.plan ? tp.changed(r.plan.name) : tp.changedNone
          setMessage({
            tone: 'info',
            text: r.cleared.length ? `${head}. ${tp.cleared(r.cleared.map((c) => nameOf(c.key)).join('、'))}` : head,
          })
        },
        onError: () => {
          setAsking(false)
          setMessage({ tone: 'error', text: tp.failed })
        },
      },
    )
  }
  return (
    <div className="grid min-w-0 gap-3 border-t pt-3" data-slot="tenant-plan">
      <div className="grid gap-0.5">
        <h4 className="text-sm font-medium">{tp.label}</h4>
        <p className="text-xs text-muted-foreground">{tt.plans.help}</p>
      </div>
      <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      <div className="flex flex-wrap items-end gap-3">
        <div className="grid min-w-0 flex-1 basis-48 gap-1.5 sm:max-w-xs">
          <Label htmlFor={`plan-${tenant.id}`}>{tp.label}</Label>
          <NativeSelect
            id={`plan-${tenant.id}`}
            className="h-10 w-full lg:h-8"
            value={picked}
            disabled={plans.isPending}
            onChange={(e) => setPicked(e.target.value)}
          >
            <option value="">{tp.none}</option>
            {(plans.data ?? []).map((p) => (
              <option key={p.slug} value={p.slug}>
                {p.name}
              </option>
            ))}
          </NativeSelect>
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-10 lg:h-8"
          disabled={picked === current || put.isPending}
          onClick={() => setAsking(true)}
        >
          <Package aria-hidden="true" />
          {tp.change}
        </Button>
      </div>
      {asking && (
        <Dialog open onOpenChange={(o) => !o && setAsking(false)}>
          <DialogContent sheet className="sm:max-w-md" data-slot="plan-change">
            <DialogHeader>
              <DialogTitle className="pr-8">{tp.changeTitle(tenant.name)}</DialogTitle>
              <DialogDescription>
                {tp.label}: {tenant.plan?.name ?? tp.none} → {target}
              </DialogDescription>
            </DialogHeader>
            <p className="text-sm">{tp.changeHelp}</p>
            {manual.length === 0 ? (
              <p className="text-sm text-muted-foreground" data-slot="nothing-to-clear">
                {tp.nothingToClear}
              </p>
            ) : (
              <div className="grid gap-1.5" data-slot="to-clear">
                <p className="text-sm font-medium">{tp.willClear}</p>
                <ul className="max-h-56 divide-y overflow-y-auto rounded-lg border text-sm">
                  {manual.map(({ s, rule }) => (
                    <li key={s.key} className="grid gap-0.5 px-3 py-2" data-key={s.key}>
                      <span className="[overflow-wrap:anywhere]">{nameOf(s.key)}</span>
                      <span className="text-xs text-muted-foreground [overflow-wrap:anywhere]">
                        {[
                          rule!.value === null || rule!.value === undefined ? '' : showValue(s, rule!.value),
                          lowerText(s, rule!.lower),
                          windowText(rule!.from, rule!.until),
                        ]
                          .filter(Boolean)
                          .join(' · ')}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setAsking(false)}>
                {tl.cancel}
              </Button>
              <Button type="button" disabled={put.isPending} onClick={change}>
                {put.isPending ? tp.working : tp.confirm}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}
