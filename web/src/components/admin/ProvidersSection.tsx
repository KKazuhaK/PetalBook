import { useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { adminApi, useAuthProviders } from '@/api/admin'
import { ApiError } from '@/api/client'
import { keys } from '@/api/hooks'
import type { AuthProvider, ProviderInput, ProviderKind } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ICON_PRESETS, ProviderIcon } from '@/components/auth/ProviderIcon'
import { ErrorState } from '@/components/common'
import { SettingsSection } from '@/components/settings/SettingsParts'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'
import { RequiredMark } from '@/components/ui/field-mark'

const tp = tsec.providers
const KINDS: readonly ProviderKind[] = ['oidc', 'saml', 'github']

// What a refusal of the provider endpoints says, in words.
function refusal(e: unknown): string {
  if (!(e instanceof ApiError)) return tp.errors.unknown
  if (e.code !== 'invalid' && Object.hasOwn(tp.errors, e.code)) return tp.errors[e.code]
  return e.message || tp.errors.unknown
}

export function ProvidersSection() {
  const providers = useAuthProviders()
  const [editing, setEditing] = useState<AuthProvider | 'new' | null>(null)
  const [deleting, setDeleting] = useState<AuthProvider | null>(null)
  const list = providers.data?.providers ?? []
  return (
    <SettingsSection title={tp.title} description={tp.help} id="security-providers">
      <div>
        <Button type="button" onClick={() => setEditing('new')}>
          {tp.add}
        </Button>
      </div>
      {providers.isPending ? (
        <div aria-busy="true" className="h-16 animate-pulse rounded-lg bg-muted" />
      ) : providers.isError ? (
        <ErrorState message={t.fmt.labelled(tp.loadFailed, providers.error.message)} onRetry={() => void providers.refetch()} />
      ) : list.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tp.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="providers">
          {list.map((p) => (
            <ProviderRow key={p.slug} p={p} onEdit={() => setEditing(p)} onDelete={() => setDeleting(p)} />
          ))}
        </ul>
      )}
      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100vw-1rem)] max-w-2xl gap-4 overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editing === 'new' || editing === null ? tp.createTitle : tp.editTitle(editing.name)}</DialogTitle>
            <DialogDescription className="sr-only">{tp.help}</DialogDescription>
          </DialogHeader>
          {editing !== null && <ProviderForm key={editing === 'new' ? 'new' : editing.slug} initial={editing === 'new' ? null : editing} onDone={() => setEditing(null)} />}
        </DialogContent>
      </Dialog>
      <DeleteDialog provider={deleting} onClose={() => setDeleting(null)} />
    </SettingsSection>
  )
}

// What changing a provider changes elsewhere: the settings page (is there a provider at all) and what the sign-in page draws.
function useChanged() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: keys.adminProviders })
    void qc.invalidateQueries({ queryKey: keys.adminSecurity })
    void qc.invalidateQueries({ queryKey: ['admin', 'audit'] })
    void qc.invalidateQueries({ queryKey: keys.providers })
  }
}

function ProviderRow({ p, onEdit, onDelete }: { p: AuthProvider; onEdit: () => void; onDelete: () => void }) {
  const changed = useChanged()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = async (fn: () => Promise<unknown>, done?: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      changed()
      if (done) toast.success(done)
    } catch (e) {
      setError(refusal(e))
    }
    setBusy(false)
  }
  return (
    <li className="grid min-w-0 grid-cols-1 gap-2 px-3 py-3" data-provider={p.slug}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
        <ProviderIcon icon={p.icon} className="size-5 shrink-0" />
        <div className="min-w-0 flex-1 basis-48">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="min-w-0 truncate text-sm font-medium">{p.name}</span>
            <Badge variant="secondary">{tp.kinds[p.kind]}</Badge>
            {p.status.ok ? <Badge variant="secondary">{tp.statusOk}</Badge> : <Badge variant="destructive">{tp.statusBad}</Badge>}
            <span className="text-xs text-muted-foreground">{tp.people(p.people)}</span>
          </div>
          <div className="text-xs break-all text-muted-foreground">
            {p.slug}
            {p.status.checked ? ` · ${tp.checked(absoluteTime(p.status.checked))}` : ''}
          </div>
          {!p.status.ok && p.status.message && <div className="text-xs break-words text-destructive">{p.status.message}</div>}
        </div>
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={p.enabled} disabled={busy} aria-label={t.fmt.labelled(p.enabled ? tp.enabled : tp.disabled, p.name)} onCheckedChange={(on) => void run(() => adminApi.patchAuthProvider(p.slug, { enabled: on }))} />
          <span className="text-xs text-muted-foreground">{p.enabled ? tp.enabled : tp.disabled}</span>
        </label>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" disabled={busy} aria-label={tp.editFor(p.name)} onClick={onEdit}>
          {tp.edit}
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={busy} aria-label={tp.refreshFor(p.name)} onClick={() => void run(() => adminApi.refreshAuthProvider(p.slug), tp.refreshed)}>
          {busy ? tp.refreshing : tp.refresh}
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={busy} aria-label={tp.deleteFor(p.name)} onClick={onDelete}>
          {tp.delete}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </li>
  )
}

function DeleteDialog({ provider, onClose }: { provider: AuthProvider | null; onClose: () => void }) {
  const changed = useChanged()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Set when the server said some accounts have no other way in: the same dialog then asks about forcing.
  const [stranded, setStranded] = useState<number | null>(null)
  const close = () => {
    setStranded(null)
    setError('')
    onClose()
  }
  const remove = async (force: boolean) => {
    if (!provider) return
    setBusy(true)
    setError('')
    try {
      await adminApi.deleteAuthProvider(provider.slug, force)
      changed()
      toast.success(tp.deleted)
      close()
    } catch (e) {
      if (e instanceof ApiError && e.code === 'provider_in_use') {
        const n = (e.body as { accounts?: number } | undefined)?.accounts
        setStranded(typeof n === 'number' ? n : 1)
      } else setError(refusal(e))
    }
    setBusy(false)
  }
  return (
    <AlertDialog open={provider !== null} onOpenChange={(open) => !open && close()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{provider ? (stranded === null ? tp.deleteTitle(provider.name) : tp.deleteInUseTitle(provider.name)) : ''}</AlertDialogTitle>
          <AlertDialogDescription>{stranded === null ? tp.deleteHelp : tp.deleteInUse(stranded)}</AlertDialogDescription>
        </AlertDialogHeader>
        <FormMessage>{error}</FormMessage>
        <AlertDialogFooter>
          <AlertDialogCancel>{tp.cancel}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={busy} onClick={() => void remove(stranded !== null)}>
            {stranded === null ? tp.deleteConfirm : tp.deleteForce}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

// --- the form ---------------------------------------------------------------------------

interface F {
  slug: string
  kind: ProviderKind
  name: string
  icon: string
  enabled: boolean
  order: string
  autoCreate: boolean
  mergeVerifiedEmail: boolean
  oidc: { issuer: string; clientId: string; secret: string | null; scopes: string; legacyCallback: boolean }
  saml: { metadataUrl: string; metadataXml: string; entityId: string; email: string; name: string; username: string; nameIdFormat: string; signRequests: boolean; allowIdpInitiated: boolean }
  github: { baseUrl: string; clientId: string; secret: string | null; organizations: string }
}

const blank = (): F => ({
  slug: '',
  kind: 'oidc',
  name: '',
  icon: '',
  enabled: true,
  order: '0',
  autoCreate: false,
  mergeVerifiedEmail: false,
  oidc: { issuer: '', clientId: '', secret: null, scopes: 'profile email', legacyCallback: false },
  saml: { metadataUrl: '', metadataXml: '', entityId: '', email: 'mail', name: 'displayName', username: 'uid', nameIdFormat: '', signRequests: false, allowIdpInitiated: false },
  github: { baseUrl: 'https://github.com', clientId: '', secret: null, organizations: '' },
})

function formOf(p: AuthProvider): F {
  const f = blank()
  f.slug = p.slug
  f.kind = p.kind
  f.name = p.name
  f.icon = p.icon
  f.enabled = p.enabled
  f.order = String(p.order)
  f.autoCreate = p.autoCreate
  f.mergeVerifiedEmail = p.mergeVerifiedEmail
  if (p.oidc) f.oidc = { issuer: p.oidc.issuer, clientId: p.oidc.clientId, secret: null, scopes: p.oidc.scopes.join(' '), legacyCallback: p.oidc.legacyCallback }
  if (p.saml) f.saml = { metadataUrl: p.saml.metadataUrl, metadataXml: p.saml.metadataXml, entityId: p.saml.entityId, email: p.saml.attributes.email, name: p.saml.attributes.name, username: p.saml.attributes.username, nameIdFormat: p.saml.nameIdFormat, signRequests: p.saml.signRequests, allowIdpInitiated: p.saml.allowIdpInitiated }
  if (p.github) f.github = { baseUrl: p.github.baseUrl, clientId: p.github.clientId, secret: null, organizations: p.github.organizations.join('\n') }
  return f
}

const words = (s: string) => s.split(/\s+/).filter(Boolean)
const lines = (s: string) => s.split(/\r?\n/).map((l) => l.trim()).filter(Boolean)

// What to send: the whole provider for a new one; for an existing one only what changed.
function inputOf(f: F, was: AuthProvider | null): ProviderInput {
  const base = was ? formOf(was) : null
  const out: ProviderInput = was ? {} : { slug: f.slug.trim(), kind: f.kind }
  const diff = <K extends keyof F>(k: K) => !base || JSON.stringify(f[k]) !== JSON.stringify(base[k])
  if (diff('name')) out.name = f.name.trim()
  if (diff('icon')) out.icon = f.icon
  // A new provider that is left on is not asked to be: the server then keeps one it cannot reach switched off.
  if (was ? diff('enabled') : !f.enabled) out.enabled = f.enabled
  if (diff('order')) out.order = Number(f.order)
  if (diff('autoCreate')) out.autoCreate = f.autoCreate
  if (diff('mergeVerifiedEmail')) out.mergeVerifiedEmail = f.mergeVerifiedEmail
  if (f.kind === 'oidc') {
    const o = f.oidc
    const b = base?.oidc
    const block: NonNullable<ProviderInput['oidc']> = {}
    if (!b || o.issuer !== b.issuer) block.issuer = o.issuer.trim()
    if (!b || o.clientId !== b.clientId) block.clientId = o.clientId.trim()
    if (o.secret !== null) block.clientSecret = o.secret
    if (!b || o.scopes !== b.scopes) block.scopes = words(o.scopes)
    if (!b || o.legacyCallback !== b.legacyCallback) block.legacyCallback = o.legacyCallback
    if (Object.keys(block).length || !was) out.oidc = block
  } else if (f.kind === 'saml') {
    const s = f.saml
    const b = base?.saml
    const block: NonNullable<ProviderInput['saml']> = {}
    if (!b || s.metadataUrl !== b.metadataUrl) block.metadataUrl = s.metadataUrl.trim()
    if (!b || s.metadataXml !== b.metadataXml) block.metadataXml = s.metadataXml
    if (!b || s.entityId !== b.entityId) block.entityId = s.entityId.trim()
    if (!b || s.email !== b.email || s.name !== b.name || s.username !== b.username) block.attributes = { email: s.email.trim(), name: s.name.trim(), username: s.username.trim() }
    if (!b || s.nameIdFormat !== b.nameIdFormat) block.nameIdFormat = s.nameIdFormat.trim()
    if (!b || s.signRequests !== b.signRequests) block.signRequests = s.signRequests
    if (!b || s.allowIdpInitiated !== b.allowIdpInitiated) block.allowIdpInitiated = s.allowIdpInitiated
    if (Object.keys(block).length || !was) out.saml = block
  } else {
    const g = f.github
    const b = base?.github
    const block: NonNullable<ProviderInput['github']> = {}
    if (!b || g.baseUrl !== b.baseUrl) block.baseUrl = g.baseUrl.trim()
    if (!b || g.clientId !== b.clientId) block.clientId = g.clientId.trim()
    if (g.secret !== null) block.clientSecret = g.secret
    if (!b || g.organizations !== b.organizations) block.organizations = lines(g.organizations)
    if (Object.keys(block).length || !was) out.github = block
  }
  return out
}

function ProviderForm({ initial, onDone }: { initial: AuthProvider | null; onDone: () => void }) {
  const changed = useChanged()
  const [f, setF] = useState<F>(() => (initial ? formOf(initial) : blank()))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const messageRef = useRef<HTMLParagraphElement>(null)
  const set = (patch: Partial<F>) => setF((x) => ({ ...x, ...patch }))
  const edit = initial !== null
  const input = inputOf(f, initial)
  const dirty = Object.keys(input).length > 0

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const saved = edit ? await adminApi.patchAuthProvider(initial.slug, input) : await adminApi.createAuthProvider(input)
      changed()
      toast.success(edit ? tp.updated : tp.created)
      // Stored but not reachable: it stays off, and the person is told why.
      if (!edit && !saved.status.ok) toast.warning(tp.unreachable)
      onDone()
      return
    } catch (err) {
      setError(refusal(err))
      setTimeout(() => messageRef.current?.focus(), 0)
    }
    setBusy(false)
  }

  return (
    <form className="grid min-w-0 grid-cols-1 gap-4" onSubmit={(e) => void submit(e)} noValidate data-slot="provider-form">
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <fieldset className="grid min-w-0 grid-cols-1 gap-4">
        <legend className="pb-1 text-sm font-semibold">{tp.common}</legend>
        {!edit && <Field label={tp.slug} requiredMark help={tp.slugHelp} value={f.slug} onChange={(e) => set({ slug: e.target.value })} autoComplete="off" autoCapitalize="none" spellCheck={false} maxLength={32} className="break-all" />}
        {edit && <ReadOnly label={tp.slug} value={f.slug} />}
        <div className="grid gap-1.5">
          <Label htmlFor="provider-kind">{tp.kind}</Label>
          <NativeSelect id="provider-kind" value={f.kind} disabled={edit} onChange={(e) => set({ kind: e.target.value as ProviderKind })}>
            {KINDS.map((k) => (
              <option key={k} value={k}>
                {tp.kinds[k]}
              </option>
            ))}
          </NativeSelect>
          {edit && <p className="text-xs text-muted-foreground">{tp.kindFixed}</p>}
        </div>
        <Field label={tp.name} requiredMark help={tp.nameHelp} value={f.name} onChange={(e) => set({ name: e.target.value })} maxLength={60} autoComplete="off" />
        <div className="grid gap-1.5">
          <Label htmlFor="provider-icon">{tp.icon}</Label>
          <div className="flex min-w-0 items-center gap-2">
            <ProviderIcon icon={f.icon} className="size-5 shrink-0" />
            <NativeSelect id="provider-icon" value={f.icon} onChange={(e) => set({ icon: e.target.value })}>
              <option value="">{tp.iconNone}</option>
              {ICON_PRESETS.map((n) => (
                <option key={n} value={`preset:${n}`}>
                  {tp.icons[n] ?? n}
                </option>
              ))}
            </NativeSelect>
          </div>
        </div>
        <Field label={tp.order} help={tp.orderHelp} type="number" inputMode="numeric" value={f.order} onChange={(e) => set({ order: e.target.value })} />
        <Check label={tp.enabledLabel} checked={f.enabled} onChange={(on) => set({ enabled: on })} />
        <Check label={tp.autoCreate} help={tp.autoCreateHelp} checked={f.autoCreate} onChange={(on) => set({ autoCreate: on })} />
        <Check label={tp.mergeVerifiedEmail} help={tp.mergeVerifiedEmailHelp} checked={f.mergeVerifiedEmail} onChange={(on) => set({ mergeVerifiedEmail: on })} />
      </fieldset>

      {f.kind === 'oidc' && <OidcFields f={f} set={set} initial={initial} />}
      {f.kind === 'saml' && <SamlFields f={f} set={set} initial={initial} />}
      {f.kind === 'github' && <GithubFields f={f} set={set} initial={initial} />}

      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy || !dirty}>
          {edit ? (busy ? tp.saving : tp.save) : busy ? tp.creating : tp.create}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {tp.cancel}
        </Button>
      </div>
    </form>
  )
}

function Check({ label, help, checked, onChange }: { label: string; help?: string; checked: boolean; onChange: (on: boolean) => void }) {
  return (
    <label className="flex min-w-0 items-start gap-3 text-sm">
      <Switch checked={checked} onCheckedChange={onChange} aria-label={label} className="mt-0.5" />
      <span className="min-w-0">
        {label}
        {help && <span className="block text-xs break-words text-muted-foreground">{help}</span>}
      </span>
    </label>
  )
}

// A value the server made, to be copied to the identity provider.
function ReadOnly({ label, value, help, multiline }: { label: string; value: string; help?: string; multiline?: boolean }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }
  return (
    <div className="grid min-w-0 gap-1.5" data-readonly={label}>
      <Label>{label}</Label>
      <div className="flex min-w-0 flex-wrap gap-2">
        {multiline ? (
          <Textarea readOnly value={value} aria-label={label} className="min-w-0 flex-1 basis-56 font-mono text-xs break-all" rows={4} onFocus={(e) => e.currentTarget.select()} />
        ) : (
          <Input readOnly value={value} aria-label={label} className="min-w-0 flex-1 basis-56 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
        )}
        <Button type="button" variant="outline" size="sm" onClick={() => void copy()}>
          {copied ? tp.copied : tp.copy}
        </Button>
      </div>
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  )
}

// A secret the server never shows again: saved · change, clear.
function WriteOnly({ label, help, stored, value, onChange, required }: { label: string; help?: string; stored: boolean; value: string | null; onChange: (v: string | null) => void; required?: boolean }) {
  const [replacing, setReplacing] = useState(false)
  let body: ReactNode
  if (stored && value === null && !replacing) {
    body = (
      <div className="flex flex-wrap items-center gap-2 text-sm" data-slot="secret-saved">
        <span>
          {tp.secretSaved} ·{' '}
          <button type="button" className="underline underline-offset-4" onClick={() => setReplacing(true)}>
            {tp.secretReplace}
          </button>
        </span>
        <Button type="button" variant="outline" size="sm" onClick={() => onChange('')}>
          {tp.secretClear}
        </Button>
      </div>
    )
  } else if (value === '' && !replacing) {
    body = (
      <p className="text-sm text-muted-foreground">
        {tp.secretWillClear} ·{' '}
        <button type="button" className="underline underline-offset-4" onClick={() => onChange(null)}>
          {tp.cancel}
        </button>
      </p>
    )
  } else {
    body = <Input type="password" autoComplete="new-password" aria-label={label} aria-required={required ? true : undefined} placeholder={stored ? tp.secretKeep : tp.secretNone} value={value ?? ''} onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)} />
  }
  return (
    <div className="grid min-w-0 gap-1.5">
      <Label>
        {label}
        {required && <RequiredMark />}
      </Label>
      {body}
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  )
}

type Sub = { f: F; set: (p: Partial<F>) => void; initial: AuthProvider | null }

function OidcFields({ f, set, initial }: Sub) {
  const o = f.oidc
  const up = (p: Partial<F['oidc']>) => set({ oidc: { ...o, ...p } })
  return (
    <fieldset className="grid min-w-0 grid-cols-1 gap-4">
      <legend className="pb-1 text-sm font-semibold">{tp.kinds.oidc}</legend>
      <Field label={tp.oidc.issuer} requiredMark help={tp.oidc.issuerHelp} value={o.issuer} onChange={(e) => up({ issuer: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <Field label={tp.oidc.clientId} requiredMark value={o.clientId} onChange={(e) => up({ clientId: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <WriteOnly label={tp.oidc.clientSecret} help={tp.oidc.clientSecretHelp} stored={initial?.oidc?.hasSecret === true} value={o.secret} onChange={(v) => up({ secret: v })} />
      <Field label={tp.oidc.scopes} help={tp.oidc.scopesHelp} value={o.scopes} onChange={(e) => up({ scopes: e.target.value })} autoComplete="off" spellCheck={false} />
      <Check label={tp.oidc.legacyCallback} help={tp.oidc.legacyCallbackHelp} checked={o.legacyCallback} onChange={(on) => up({ legacyCallback: on })} />
      {initial?.oidc && <ReadOnly label={tp.oidc.redirectUrl} value={initial.oidc.redirectUrl} />}
    </fieldset>
  )
}

function SamlFields({ f, set, initial }: Sub) {
  const s = f.saml
  const up = (p: Partial<F['saml']>) => set({ saml: { ...s, ...p } })
  const saved = initial?.saml
  return (
    <fieldset className="grid min-w-0 grid-cols-1 gap-4">
      <legend className="pb-1 text-sm font-semibold">{tp.kinds.saml}</legend>
      <Field label={tp.saml.metadataUrl} help={tp.saml.metadataUrlHelp} value={s.metadataUrl} onChange={(e) => up({ metadataUrl: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <div className="grid gap-1.5">
        <Label htmlFor="saml-xml">{tp.saml.metadataXml}</Label>
        <Textarea id="saml-xml" value={s.metadataXml} onChange={(e) => up({ metadataXml: e.target.value })} spellCheck={false} className="min-w-0 font-mono text-xs break-all" rows={4} />
      </div>
      <Field label={tp.saml.entityId} help={tp.saml.entityIdHelp} value={s.entityId} onChange={(e) => up({ entityId: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <div className="grid min-w-0 gap-3">
        <div>
          <h4 className="text-sm font-medium">{tp.saml.attributes}</h4>
          <p className="text-xs text-muted-foreground">{tp.saml.attributesHelp}</p>
        </div>
        <Field label={tp.saml.attrEmail} value={s.email} onChange={(e) => up({ email: e.target.value })} autoComplete="off" spellCheck={false} />
        <Field label={tp.saml.attrName} value={s.name} onChange={(e) => up({ name: e.target.value })} autoComplete="off" spellCheck={false} />
        <Field label={tp.saml.attrUsername} value={s.username} onChange={(e) => up({ username: e.target.value })} autoComplete="off" spellCheck={false} />
      </div>
      <Field label={tp.saml.nameIdFormat} help={tp.saml.nameIdFormatHelp} value={s.nameIdFormat} onChange={(e) => up({ nameIdFormat: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <Check label={tp.saml.signRequests} checked={s.signRequests} onChange={(on) => up({ signRequests: on })} />
      <Check label={tp.saml.allowIdpInitiated} help={tp.saml.allowIdpInitiatedHelp} checked={s.allowIdpInitiated} onChange={(on) => up({ allowIdpInitiated: on })} />
      {saved && (
        <div className="grid min-w-0 gap-3 rounded-md border p-3" data-slot="saml-readonly">
          <ReadOnly label={tp.saml.idpEntityId} value={saved.idpEntityId} />
          <ReadOnly label={tp.saml.entityId} value={saved.entityId} />
          <ReadOnly label={tp.saml.acsUrl} value={saved.acsUrl} help={tp.saml.acsHelp} />
          <ReadOnly label={tp.saml.metadataPath} value={saved.metadataPath} />
          <ReadOnly label={tp.saml.certificate} value={saved.certificate} multiline help={`${tp.saml.certificateHelp} ${tp.saml.certificateExpires(absoluteTime(saved.certificateExpires))}`} />
        </div>
      )}
    </fieldset>
  )
}

function GithubFields({ f, set, initial }: Sub) {
  const g = f.github
  const up = (p: Partial<F['github']>) => set({ github: { ...g, ...p } })
  return (
    <fieldset className="grid min-w-0 grid-cols-1 gap-4">
      <legend className="pb-1 text-sm font-semibold">{tp.kinds.github}</legend>
      <Field label={tp.github.baseUrl} help={tp.github.baseUrlHelp} value={g.baseUrl} onChange={(e) => up({ baseUrl: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <Field label={tp.github.clientId} requiredMark value={g.clientId} onChange={(e) => up({ clientId: e.target.value })} autoComplete="off" spellCheck={false} className="break-all" />
      <WriteOnly label={tp.github.clientSecret} required stored={initial?.github?.hasSecret === true} value={g.secret} onChange={(v) => up({ secret: v })} />
      <div className="grid gap-1.5">
        <Label htmlFor="github-orgs">{tp.github.organizations}</Label>
        <Textarea id="github-orgs" value={g.organizations} onChange={(e) => up({ organizations: e.target.value })} spellCheck={false} rows={3} className="min-w-0 break-all" />
        <p className="text-xs text-muted-foreground">{tp.github.organizationsHelp}</p>
      </div>
      {initial?.github && <ReadOnly label={tp.github.redirectUrl} value={initial.github.redirectUrl} />}
    </fieldset>
  )
}
