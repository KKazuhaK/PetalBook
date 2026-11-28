import { useState, type FormEvent } from 'react'
import { ArrowLeft, Pencil, Plus, Save, Trash2 } from 'lucide-react'
import { ApiError } from '@/api/client'
import { useCreatePlan, useDeletePlan, usePlans, useSiteSettings, useUpdatePlan } from '@/api/admin'
import type { Plan, PlanRule, SiteSetting } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import { DataTable, RowAction, type Column } from '@/components/ui/data-table'
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
import { useTablePrefs } from '@/lib/tablePrefs'
import { tt } from '@/i18n/tenants'
import { AdminPageActions } from './AdminPageHeader'
import { RuleDialog, nameOf } from './RuleDialog'
import { settingError } from './SettingsPanel'
import { needsCapabilityConfirm, refusedFeature, useCapabilityConfirm } from './CapabilityConfirm'
import { lowerText, showValue } from './TenantRules'

const tp = tt.plans

// What a refusal of a plan call means, in plain words.
export function planError(e: unknown): string {
  if (!(e instanceof ApiError)) return tp.errors.other
  if (e.code === 'plan_in_use') return tp.inUse((e.body as { tenants?: number } | undefined)?.tenants ?? 0)
  const known = (tp.errors as Record<string, string>)[e.code]
  return known ?? e.message ?? tp.errors.other
}

// A setting a plan may have a rule for: one a rule for a tenant can be written for, with a value or a restriction the
// editor can show (a list or a document has no editor).
const forPlans = (s: SiteSetting) => !s.serverWide && s.kind !== 'list' && s.kind !== 'doc'

// The plans a site has, and the rules of each. Site administrators only.
export function PlansPanel() {
  const q = usePlans()
  const prefs = useTablePrefs('plans')
  const [open, setOpen] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Plan | null>(null)
  if (q.isPending) return <PageFallback />
  if (q.isError) return <ErrorState message={tp.loadFailed} onRetry={() => void q.refetch()} />
  const plan = open ? q.data.find((p) => p.slug === open) : undefined
  if (plan) return <PlanDetail plan={plan} onBack={() => setOpen(null)} />
  const columns: Column<Plan>[] = [
    {
      id: 'name',
      header: tp.columns.name,
      required: true,
      weight: 3,
      min: 160,
      cell: (p) => (
        <div className="min-w-0">
          <div className="text-sm [overflow-wrap:anywhere]">{p.name}</div>
          <div className="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]">{p.slug}</div>
          {p.description && <div className="mt-0.5 text-xs text-muted-foreground">{p.description}</div>}
        </div>
      ),
    },
    {
      id: 'rules',
      header: tp.columns.rules,
      weight: 1,
      min: 90,
      priority: 2,
      cell: (p) => <span className="tabular-nums">{tp.rulesCount(Object.keys(p.rules).length)}</span>,
    },
    {
      id: 'tenants',
      header: tp.columns.tenants,
      weight: 1,
      min: 90,
      priority: 3,
      cell: (p) => <span className="tabular-nums">{tp.tenantsCount(p.tenants)}</span>,
    },
  ]
  return (
    <SettingsSection title={tp.title} id="plans-heading" description={tp.help}>
      <AdminPageActions>
        <Button type="button" onClick={() => setCreating(true)}>
          <Plus aria-hidden="true" />
          {tp.create}
        </Button>
      </AdminPageActions>
      <DataTable
        prefs={prefs}
        columns={columns}
        rows={q.data}
        rowId={(p) => p.slug}
        rowLabel={(p) => p.name}
        rowAttrs={(p) => ({ 'data-plan': p.slug })}
        empty={tp.empty}
        actions={(p) => (
          <>
            <RowAction
              icon={<Pencil aria-hidden="true" />}
              label={tp.manage}
              ariaLabel={tp.manageFor(p.name)}
              onClick={() => setOpen(p.slug)}
            />
            <RowAction
              icon={<Trash2 aria-hidden="true" />}
              label={tp.delete}
              ariaLabel={tp.deleteFor(p.name)}
              onClick={() => setDeleting(p)}
            />
          </>
        )}
      />
      {creating && (
        <CreatePlan
          onClose={() => setCreating(false)}
          onMade={(slug) => {
            setCreating(false)
            setOpen(slug)
          }}
        />
      )}
      {deleting && <DeletePlan plan={deleting} onClose={() => setDeleting(null)} onDone={() => setDeleting(null)} />}
    </SettingsSection>
  )
}

function CreatePlan({ onClose, onMade }: { onClose: () => void; onMade: (slug: string) => void }) {
  const create = useCreatePlan()
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const submit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    create.mutate(
      { slug: slug.trim(), name: name.trim(), ...(description.trim() ? { description: description.trim() } : {}) },
      { onSuccess: (p) => onMade(p.slug), onError: (err) => setError(planError(err)) },
    )
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent sheet>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{tp.createTitle}</DialogTitle>
            <DialogDescription>{tp.createHelp}</DialogDescription>
          </DialogHeader>
          <FormMessage>{error}</FormMessage>
          <Field label={tp.nameLabel} value={name} maxLength={80} required onChange={(e) => setName(e.target.value)} />
          <Field
            label={tp.slugLabel}
            value={slug}
            maxLength={32}
            required
            autoCapitalize="none"
            spellCheck={false}
            onChange={(e) => setSlug(e.target.value.toLowerCase())}
          />
          <Field
            label={tp.descriptionLabel}
            value={description}
            maxLength={500}
            onChange={(e) => setDescription(e.target.value)}
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {tp.cancel}
            </Button>
            <Button type="submit" disabled={create.isPending || !name.trim() || !slug.trim()}>
              {create.isPending ? tp.creating : tp.create}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function DeletePlan({ plan, onClose, onDone }: { plan: Plan; onClose: () => void; onDone: () => void }) {
  const del = useDeletePlan()
  const [error, setError] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent sheet>
        <DialogHeader>
          <DialogTitle>{tp.deleteTitle(plan.name)}</DialogTitle>
          <DialogDescription>{tp.deleteHelp}</DialogDescription>
        </DialogHeader>
        <FormMessage>{error}</FormMessage>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {tp.cancel}
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={del.isPending}
            onClick={() => del.mutate(plan.slug, { onSuccess: onDone, onError: (e) => setError(planError(e)) })}
          >
            {del.isPending ? tp.deleting : tp.delete}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

interface RuleRow {
  key: string
  kind: SiteSetting['kind']
  rule: PlanRule
}

// One plan: its name and description, and its rules, each edited with the rule editor (no dates, and a limit a tenant
// also sets must cap).
function PlanDetail({ plan, onBack }: { plan: Plan; onBack: () => void }) {
  const update = useUpdatePlan()
  const settings = useSiteSettings()
  const prefs = useTablePrefs('plan-rules')
  const [name, setName] = useState(plan.name)
  const [description, setDescription] = useState(plan.description)
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  // The rule being added (adding: true) or edited.
  const [editing, setEditing] = useState<{ key: string; adding: boolean } | null>(null)
  const [ruleError, setRuleError] = useState('')
  // The rule asked about removing: it leaves every tenant on the plan, so it is confirmed.
  const [removing, setRemoving] = useState<string | null>(null)
  const all = settings.data?.settings ?? []
  const byKey = new Map(all.map((s) => [s.key, s]))
  const rows: RuleRow[] = Object.entries(plan.rules)
    .map(([key, rule]) => ({ key, kind: byKey.get(key)?.kind ?? 'text', rule }))
    .sort((a, b) => a.key.localeCompare(b.key))
  const saveDetails = (e: FormEvent) => {
    e.preventDefault()
    setMessage(null)
    update.mutate(
      { slug: plan.slug, name: name.trim(), description: description.trim() },
      {
        onSuccess: (r) =>
          setMessage({ tone: 'info', text: r.affected.length ? tp.savedAffected(r.affected.length) : tp.saved }),
        onError: (err) => setMessage({ tone: 'error', text: planError(err) }),
      },
    )
  }
  // Rules that switch off a feature a tenant on the plan uses go through its preview and a second confirmation.
  const capability = useCapabilityConfirm(() => setMessage({ tone: 'info', text: tp.saved }))
  const writeRule = (key: string, rule: PlanRule | null, done: () => void) => {
    setRuleError('')
    update.mutate(
      { slug: plan.slug, rules: { [key]: rule } },
      {
        onSuccess: (r) => {
          done()
          setMessage({
            tone: 'info',
            text: r.affected.length
              ? tp.savedAffected(r.affected.length)
              : rule
                ? tp.ruleSaved(key)
                : tp.ruleRemoved(key),
          })
        },
        onError: (err) =>
          needsCapabilityConfirm(err)
            ? capability.ask({
                key: refusedFeature(err),
                send: (confirm) =>
                  update.mutateAsync({ slug: plan.slug, rules: { [key]: rule }, confirm }).then(() => done()),
              })
            : setRuleError(
                err instanceof ApiError && err.code.startsWith('setting_') ? settingError(err) : planError(err),
              ),
      },
    )
  }
  const columns: Column<RuleRow>[] = [
    {
      id: 'setting',
      header: tp.columnSetting,
      required: true,
      weight: 3,
      min: 170,
      cell: (r) => (
        <div className="min-w-0">
          <div className="text-sm [overflow-wrap:anywhere]">{nameOf(r.key)}</div>
          <div className="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]">{r.key}</div>
        </div>
      ),
    },
    {
      id: 'value',
      header: tp.columnValue,
      weight: 1.5,
      min: 100,
      priority: 2,
      cell: (r) => (
        <span className="tabular-nums [overflow-wrap:anywhere]">
          {r.rule.value === undefined || r.rule.value === null ? tp.noValue : showValue(r, r.rule.value)}
        </span>
      ),
    },
    {
      id: 'rule',
      header: tp.columnRule,
      weight: 2,
      min: 130,
      priority: 3,
      cell: (r) => <span className="text-sm [overflow-wrap:anywhere]">{lowerText(r, r.rule.lower ?? null)}</span>,
    },
  ]
  const editRow = editing ? rows.find((r) => r.key === editing.key) : undefined
  return (
    <div className="grid min-w-0 gap-8" data-slot="plan-detail" data-plan={plan.slug}>
      {capability.dialog}
      <div>
        <Button type="button" variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft aria-hidden="true" />
          {tp.back}
        </Button>
      </div>
      <SettingsSection title={plan.name} id="plan-details-heading" description={tp.detailsTitle}>
        <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
        <form onSubmit={saveDetails} className="grid gap-3 sm:grid-cols-2">
          <Field label={tp.nameLabel} value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
          <Field
            label={tp.descriptionLabel}
            value={description}
            maxLength={500}
            onChange={(e) => setDescription(e.target.value)}
          />
          <div className="sm:col-span-2">
            <Button type="submit" size="sm" disabled={update.isPending || !name.trim()}>
              <Save aria-hidden="true" />
              {update.isPending ? tp.saving : tp.save}
            </Button>
          </div>
        </form>
      </SettingsSection>
      <SettingsSection title={tp.rulesTitle} id="plan-rules-heading" description={tp.rulesHelp}>
        <div>
          <Button
            type="button"
            size="sm"
            onClick={() => {
              setRuleError('')
              setEditing({ key: '', adding: true })
            }}
          >
            <Plus aria-hidden="true" />
            {tp.addRule}
          </Button>
        </div>
        <DataTable
          prefs={prefs}
          columns={columns}
          rows={rows}
          rowId={(r) => r.key}
          rowLabel={(r) => r.key}
          rowAttrs={(r) => ({ 'data-key': r.key })}
          loading={settings.isPending}
          empty={tp.noRules}
          actions={(r) => (
            <>
              {r.kind !== 'list' && r.kind !== 'doc' && (
                <RowAction
                  icon={<Pencil aria-hidden="true" />}
                  label={tp.edit}
                  ariaLabel={tp.editFor(r.key)}
                  onClick={() => {
                    setRuleError('')
                    setEditing({ key: r.key, adding: false })
                  }}
                />
              )}
              <RowAction
                icon={<Trash2 aria-hidden="true" />}
                label={tp.remove}
                ariaLabel={tp.removeFor(r.key)}
                onClick={() => setRemoving(r.key)}
              />
            </>
          )}
        />
      </SettingsSection>
      {removing !== null && (
        <Dialog open onOpenChange={(o) => !o && setRemoving(null)}>
          <DialogContent sheet data-slot="rule-remove">
            <DialogHeader>
              <DialogTitle className="pr-8">{tp.removeTitle(nameOf(removing))}</DialogTitle>
              <DialogDescription>{tp.removeHelp(plan.tenants)}</DialogDescription>
            </DialogHeader>
            <FormMessage>{ruleError}</FormMessage>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setRemoving(null)}>
                {tp.cancel}
              </Button>
              <Button
                type="button"
                variant="destructive"
                disabled={update.isPending}
                onClick={() => writeRule(removing, null, () => setRemoving(null))}
              >
                {tp.remove}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
      {editing && (
        <PlanRuleDialog
          adding={editing.adding}
          initialKey={editing.key}
          rule={editRow?.rule ?? null}
          options={all.filter((s) => forPlans(s) && (editing.adding ? !(s.key in plan.rules) : s.key === editing.key))}
          pending={update.isPending}
          error={ruleError}
          onSubmit={(key, rule) => writeRule(key, rule, () => setEditing(null))}
          onRemove={editRow ? () => writeRule(editRow.key, null, () => setEditing(null)) : undefined}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  )
}

// The rule of one setting of the plan: which setting (when adding), then the rule editor.
function PlanRuleDialog({
  adding,
  initialKey,
  rule,
  options,
  pending,
  error,
  onSubmit,
  onRemove,
  onClose,
}: {
  adding: boolean
  initialKey: string
  rule: PlanRule | null
  options: SiteSetting[]
  pending: boolean
  error: string
  onSubmit: (key: string, rule: PlanRule) => void
  onRemove?: () => void
  onClose: () => void
}) {
  const [key, setKey] = useState(initialKey || (options[0]?.key ?? ''))
  const s = options.find((x) => x.key === key)
  if (!s) return null
  return (
    <RuleDialog
      // The form starts again for another setting.
      key={s.key}
      setting={{
        key: s.key,
        kind: s.kind,
        choices: s.choices,
        strictness: s.strictness,
        tenantSets: s.layers.includes('tenant'),
      }}
      title={adding ? tp.addRuleTitle : undefined}
      rule={rule}
      start={s.site.value}
      windows={false}
      capRequired
      header={
        adding && (
          <div className="grid gap-1.5">
            <Label htmlFor="plan-rule-key">{tp.pickSetting}</Label>
            <NativeSelect
              id="plan-rule-key"
              className="h-10 w-full lg:h-8"
              value={key}
              onChange={(e) => setKey(e.target.value)}
            >
              {options.map((o) => (
                <option key={o.key} value={o.key}>
                  {nameOf(o.key)} ({o.key})
                </option>
              ))}
            </NativeSelect>
          </div>
        )
      }
      pending={pending}
      error={error}
      removeLabel={tp.remove}
      onRemove={onRemove}
      onSubmit={(r) =>
        onSubmit(s.key, {
          ...(r.value !== undefined ? { value: r.value } : {}),
          ...(r.lower ? { lower: r.lower } : {}),
        })
      }
      onClose={onClose}
    />
  )
}
