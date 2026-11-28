import { createContext, useContext, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import {
  ArrowLeft,
  ChevronDown,
  ChevronUp,
  Copy,
  Eraser,
  FileKey,
  FlaskConical,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
} from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { adminApi, useAuthProviders, useProviderTemplates, useSyncedGroups, useTenantProviders } from '@/api/admin'
import { ApiError } from '@/api/client'
import { Link, useSearchParams } from 'react-router'
import { keys } from '@/api/hooks'
import type {
  AuthProvider,
  ProviderClass,
  ProviderInput,
  ProviderKind,
  ProviderTemplate,
  ProviderTestResult,
} from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ssoErrorMessage } from '@/pages/auth/LoginPage'
import { ProviderIcon } from '@/components/auth/ProviderIcon'
import { ErrorState } from '@/components/common'
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
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { absoluteTime } from '@/lib/time'
import { adminPath } from '@/lib/urls'
import { useDebounced } from '@/lib/useDebounced'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'
import { tg } from '@/i18n/groups'
import { RequiredMark } from '@/components/ui/field-mark'

const tp = tsec.providers
const KINDS: readonly ProviderKind[] = ['oidc', 'saml', 'github']
// An organisation's own single sign-on is OpenID Connect or SAML (3.6 d3).
const ORG_KINDS: readonly ProviderKind[] = ['oidc', 'saml']

// Where the section's providers live: the site's (/api/admin/providers), or one tenant's own
// (/api/admin/tenants/{tenant}/providers, 3.6 d3). Every part of the section calls through this.
interface ProviderApi {
  tenant: string | null
  create: (input: ProviderInput) => Promise<AuthProvider>
  patch: (id: string, input: ProviderInput) => Promise<AuthProvider>
  remove: (id: string, force: boolean) => Promise<void>
  check: (id: string) => Promise<AuthProvider>
  test: (id: string) => Promise<{ url: string }>
  testResult: (id: string) => Promise<{ result: ProviderTestResult | null }>
}

const siteApi: ProviderApi = {
  tenant: null,
  create: (input) => adminApi.createAuthProvider(input),
  patch: (id, input) => adminApi.patchAuthProvider(id, input),
  remove: (id, force) => adminApi.deleteAuthProvider(id, force),
  check: (id) => adminApi.checkAuthProvider(id),
  test: (id) => adminApi.testAuthProvider(id),
  testResult: (id) => adminApi.authProviderTestResult(id),
}

function tenantApi(tenant: string): ProviderApi {
  return {
    tenant,
    create: (input) => adminApi.createTenantProvider(tenant, input),
    patch: (id, input) => adminApi.patchTenantProvider(tenant, id, input),
    remove: (id, force) => adminApi.deleteTenantProvider(tenant, id, force),
    check: (id) => adminApi.checkTenantProvider(tenant, id),
    // The test comes back to the page it was started on, in the same scope.
    test: (id) => adminApi.testTenantProvider(tenant, id, window.location.pathname),
    testResult: (id) => adminApi.tenantProviderTestResult(tenant, id),
  }
}

const ProviderScope = createContext<ProviderApi>(siteApi)
const useProviderApi = () => useContext(ProviderScope)

// What a refusal of the provider endpoints says, in words.
function refusal(e: unknown): string {
  if (!(e instanceof ApiError)) return tp.errors.unknown
  if (e.code === 'group_not_synced') return tg.provider.errors.notSynced
  if (e.code === 'group_not_found') return tg.provider.errors.notFound
  if (e.code === 'exists' && /mapped twice/.test(e.message)) return tg.provider.errors.exists
  if (e.code !== 'invalid' && Object.hasOwn(tp.errors, e.code)) return tp.errors[e.code]
  return e.message || tp.errors.unknown
}

// The site's sign-in providers; with `tenant`, that tenant's own single sign-on (OpenID Connect and SAML only, no
// templates). `off`: the tenant's features.ownSso is off (its providers are kept and shown, none can be added).
export function ProvidersSection({
  tenant,
  off = false,
  children,
}: {
  tenant?: string
  off?: boolean
  children?: ReactNode
}) {
  return tenant ? (
    <TenantProviders tenant={tenant} off={off}>
      {children}
    </TenantProviders>
  ) : (
    <SiteProviders />
  )
}

function SiteProviders() {
  const providers = useAuthProviders()
  return (
    <ProviderScope.Provider value={siteApi}>
      <ProvidersBody providers={providers} title={tp.title} help={tp.help} id="security-providers" />
    </ProviderScope.Provider>
  )
}

function TenantProviders({ tenant, off, children }: { tenant: string; off: boolean; children?: ReactNode }) {
  const providers = useTenantProviders(tenant, !off)
  const [api] = useState(() => tenantApi(tenant))
  return (
    <ProviderScope.Provider value={api}>
      <ProvidersBody providers={providers} title={tp.org.title} help={tp.org.help} id="org-providers" off={off}>
        {children}
      </ProvidersBody>
    </ProviderScope.Provider>
  )
}

function ProvidersBody({
  providers,
  title,
  help,
  id,
  off = false,
  children,
}: {
  providers: ReturnType<typeof useAuthProviders>
  title: string
  help: string
  id: string
  off?: boolean
  children?: ReactNode
}) {
  const [editing, setEditing] = useState<AuthProvider | 'new' | null>(null)
  const [deleting, setDeleting] = useState<AuthProvider | null>(null)
  const list = providers.data?.providers ?? []
  if (off)
    return (
      <SettingsSection title={title} description={help} id={id}>
        <p className="text-sm text-muted-foreground" data-slot="feature-off">
          {tp.org.featureOff}
        </p>
      </SettingsSection>
    )
  return (
    <SettingsSection title={title} description={help} id={id}>
      {children}
      <div>
        <Button type="button" onClick={() => setEditing('new')}>
          <Plus aria-hidden="true" />
          {tp.add}
        </Button>
      </div>
      {providers.isPending ? (
        <div aria-busy="true" className="h-16 animate-pulse rounded-lg bg-muted" />
      ) : providers.isError ? (
        <ErrorState
          message={t.fmt.labelled(tp.loadFailed, providers.error.message)}
          onRetry={() => void providers.refetch()}
        />
      ) : list.length === 0 ? (
        <p className="text-sm text-muted-foreground">{tp.empty}</p>
      ) : (
        <ul className="divide-y rounded-lg border" data-slot="providers">
          {list.map((p) => (
            <ProviderRow key={p.id} p={p} onEdit={() => setEditing(p)} onDelete={() => setDeleting(p)} />
          ))}
        </ul>
      )}
      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100vw-1rem)] max-w-2xl gap-4 overflow-y-auto sm:max-w-2xl">
          {editing === 'new' ? (
            <NewProvider onDone={() => setEditing(null)} />
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{editing === null ? tp.createTitle : tp.editTitle(editing.name)}</DialogTitle>
                <DialogDescription className="sr-only">{tp.help}</DialogDescription>
              </DialogHeader>
              {editing !== null && <ProviderForm key={editing.id} initial={editing} onDone={() => setEditing(null)} />}
            </>
          )}
        </DialogContent>
      </Dialog>
      <DeleteDialog provider={deleting} onClose={() => setDeleting(null)} />
      <TestOutcome />
    </SettingsSection>
  )
}

// Where a test sign-in comes back to: `?provider_test=<id>` (the result is read once) or `?sso_error=<code>`. Both are
// shown, then taken out of the address.
function TestOutcome() {
  const api = useProviderApi()
  const [params, setParams] = useSearchParams()
  const [arrival] = useState(() => ({ id: params.get('provider_test'), error: params.get('sso_error') }))
  const [result, setResult] = useState<ProviderTestResult | null>(null)
  const [message, setMessage] = useState(arrival.error ? ssoErrorMessage(arrival.error) : '')
  const [open, setOpen] = useState(arrival.id !== null || arrival.error !== null)
  const read = useRef(false)
  useEffect(() => {
    if (params.has('provider_test') || params.has('sso_error'))
      setParams(
        (p) => {
          const next = new URLSearchParams(p)
          next.delete('provider_test')
          next.delete('sso_error')
          return next
        },
        { replace: true },
      )
    if (arrival.id === null || arrival.error !== null) return
    // Read once: the server forgets the result, so a second read (a remount) would find nothing.
    if (read.current) return
    read.current = true
    api
      .testResult(arrival.id)
      .then((r) => (r.result ? setResult(r.result) : setMessage(tp.testNone)))
      .catch((e) => setMessage(`${tp.testFailed}: ${refusal(e)}`))
    // Once, on arrival.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const rows: [string, ReactNode][] = result
    ? [
        [tp.testSubject, result.subject],
        [
          tp.testEmail,
          result.email ? `${result.email} (${result.emailTrusted ? tp.testVerified : tp.testUnverified})` : '—',
        ],
        [tp.testName, result.name || '—'],
        [tp.testUsername, result.username || '—'],
      ]
    : []
  return (
    <Dialog open={open && (result !== null || message !== '')} onOpenChange={setOpen}>
      <DialogContent className="w-[calc(100vw-1rem)] max-w-lg gap-4 sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{tp.testTitle}</DialogTitle>
          <DialogDescription>{result ? tp.testHelp : message}</DialogDescription>
        </DialogHeader>
        {result && (
          <dl
            className="grid min-w-0 grid-cols-1 gap-x-4 gap-y-2 text-sm sm:grid-cols-[auto_1fr]"
            data-slot="test-result"
          >
            {rows.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="min-w-0 break-all">{v}</dd>
              </div>
            ))}
          </dl>
        )}
        <div className="flex justify-end">
          <Button type="button" variant="outline" onClick={() => setOpen(false)}>
            {tp.testClose}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// What changing a provider changes elsewhere: the settings page (is there a provider at all) and what the sign-in page draws.
function useChanged() {
  const qc = useQueryClient()
  const { tenant } = useProviderApi()
  return () => {
    if (tenant) {
      void qc.invalidateQueries({ queryKey: keys.tenantProviders(tenant) })
      void qc.invalidateQueries({ queryKey: keys.tenantDomains(tenant) })
    }
    void qc.invalidateQueries({ queryKey: keys.adminProviders })
    void qc.invalidateQueries({ queryKey: keys.adminSecurity })
    void qc.invalidateQueries({ queryKey: ['admin', 'audit'] })
    void qc.invalidateQueries({ queryKey: keys.providers })
  }
}

function ProviderRow({ p, onEdit, onDelete }: { p: AuthProvider; onEdit: () => void; onDelete: () => void }) {
  const api = useProviderApi()
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
            {p.class && <Badge variant="outline">{tp.classes[p.class]}</Badge>}
            {p.status.ok ? (
              <Badge variant="secondary">{tp.statusOk}</Badge>
            ) : (
              <Badge variant="destructive">{tp.statusBad}</Badge>
            )}
            <span className="text-xs text-muted-foreground">{tp.people(p.people)}</span>
          </div>
          <div className="text-xs break-all text-muted-foreground">
            {p.slug}
            {p.status.checked ? ` · ${tp.checked(absoluteTime(p.status.checked))}` : ''}
          </div>
          {!p.status.ok && p.status.message && (
            <div className="text-xs break-words text-destructive">{p.status.message}</div>
          )}
          {(p.domains?.length ?? 0) > 0 && (
            <div className="text-xs break-all text-muted-foreground" data-slot="routed-domains">
              {tp.org.domains(p.domains.join(', '))}
            </div>
          )}
        </div>
        <label className="flex items-center gap-2 text-sm">
          <Switch
            checked={p.enabled}
            disabled={busy}
            aria-label={t.fmt.labelled(p.enabled ? tp.enabled : tp.disabled, p.name)}
            onCheckedChange={(on) => void run(() => api.patch(p.id, { enabled: on }))}
          />
          <span className="text-xs text-muted-foreground">{p.enabled ? tp.enabled : tp.disabled}</span>
        </label>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          aria-label={tp.editFor(p.name)}
          onClick={onEdit}
        >
          <Pencil aria-hidden="true" />
          {tp.edit}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          aria-label={tp.refreshFor(p.name)}
          onClick={() => void run(() => api.check(p.id), tp.refreshed)}
        >
          <RefreshCw aria-hidden="true" />
          {busy ? tp.refreshing : tp.refresh}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          aria-label={tp.testFor(p.name)}
          onClick={() =>
            void run(async () => {
              const { url } = await api.test(p.id)
              window.location.assign(url)
            })
          }
        >
          <FlaskConical aria-hidden="true" />
          {tp.test}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={busy}
          aria-label={tp.deleteFor(p.name)}
          onClick={onDelete}
        >
          <Trash2 aria-hidden="true" />
          {tp.delete}
        </Button>
      </div>
      <FormMessage>{error}</FormMessage>
    </li>
  )
}

function DeleteDialog({ provider, onClose }: { provider: AuthProvider | null; onClose: () => void }) {
  const api = useProviderApi()
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
      await api.remove(provider.id, force)
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
          <AlertDialogTitle>
            {provider ? (stranded === null ? tp.deleteTitle(provider.name) : tp.deleteInUseTitle(provider.name)) : ''}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {stranded === null ? tp.deleteHelp : tp.deleteInUse(stranded)}
          </AlertDialogDescription>
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
  class: ProviderClass
  // Sign in with Apple's: the key is write-only (null keeps the stored one).
  apple: { teamId: string; keyId: string; privateKey: string | null }
  name: string
  icon: string
  enabled: boolean
  order: string
  mergeVerifiedEmail: boolean
  oidc: { issuer: string; clientId: string; secret: string | null; scopes: string }
  saml: {
    metadataUrl: string
    metadataXml: string
    entityId: string
    email: string
    name: string
    username: string
    nameIdFormat: string
    signRequests: boolean
    allowIdpInitiated: boolean
  }
  github: { baseUrl: string; clientId: string; secret: string | null }
  groups: {
    enabled: boolean
    claim: string
    missing: 'keep' | 'clear'
    map: { value: string; groupId: string }[]
    // The allowlist, one value per line.
    allow: string
  }
}

const blank = (): F => ({
  slug: '',
  kind: 'oidc',
  class: 'sso',
  apple: { teamId: '', keyId: '', privateKey: null },
  name: '',
  icon: '',
  enabled: true,
  order: '0',
  mergeVerifiedEmail: false,
  oidc: { issuer: '', clientId: '', secret: null, scopes: 'profile email' },
  saml: {
    metadataUrl: '',
    metadataXml: '',
    entityId: '',
    email: 'mail',
    name: 'displayName',
    username: 'uid',
    nameIdFormat: '',
    signRequests: false,
    allowIdpInitiated: false,
  },
  github: { baseUrl: 'https://github.com', clientId: '', secret: null },
  groups: { enabled: false, claim: 'groups', missing: 'keep', map: [], allow: '' },
})

function formOf(p: AuthProvider): F {
  const f = blank()
  f.slug = p.slug
  f.kind = p.kind
  f.class = p.class
  if (p.apple) f.apple = { teamId: p.apple.teamId, keyId: p.apple.keyId, privateKey: null }
  f.name = p.name
  f.icon = p.icon
  f.enabled = p.enabled
  f.order = String(p.order)
  f.mergeVerifiedEmail = p.mergeVerifiedEmail
  if (p.oidc)
    f.oidc = {
      issuer: p.oidc.issuer,
      clientId: p.oidc.clientId,
      secret: null,
      scopes: p.oidc.scopes.join(' '),
    }
  if (p.saml)
    f.saml = {
      metadataUrl: p.saml.metadataUrl,
      metadataXml: p.saml.metadataXml,
      entityId: p.saml.entityId,
      email: p.saml.attributes.email,
      name: p.saml.attributes.name,
      username: p.saml.attributes.username,
      nameIdFormat: p.saml.nameIdFormat,
      signRequests: p.saml.signRequests,
      allowIdpInitiated: p.saml.allowIdpInitiated,
    }
  f.groups = {
    enabled: p.groups.enabled,
    claim: p.groups.claim,
    missing: p.groups.missing,
    map: p.groups.map.map((e) => ({ ...e })),
    allow: (p.groups.allow ?? []).join('\n'),
  }
  if (p.github)
    f.github = {
      baseUrl: p.github.baseUrl,
      clientId: p.github.clientId,
      secret: null,
    }
  return f
}

const words = (s: string) => s.split(/\s+/).filter(Boolean)
const lines = (s: string) =>
  s
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean)

// What to send: the whole provider for a new one; for an existing one only what changed.
function inputOf(f: F, was: AuthProvider | null): ProviderInput {
  const base = was ? formOf(was) : null
  // The class is chosen once, when the provider is made; GitHub is always social sign-in.
  const out: ProviderInput = was
    ? {}
    : { slug: f.slug.trim(), kind: f.kind, class: f.kind === 'github' ? 'social' : f.class }
  const diff = <K extends keyof F>(k: K) => !base || JSON.stringify(f[k]) !== JSON.stringify(base[k])
  if (diff('name')) out.name = f.name.trim()
  if (diff('icon')) out.icon = f.icon
  // A new provider that is left on is not asked to be: the server then keeps one it cannot reach switched off.
  if (was ? diff('enabled') : !f.enabled) out.enabled = f.enabled
  if (diff('order')) out.order = Number(f.order)
  if (diff('mergeVerifiedEmail')) out.mergeVerifiedEmail = f.mergeVerifiedEmail
  const gb = base?.groups
  const groups: NonNullable<ProviderInput['groups']> = {}
  if (f.groups.enabled !== (gb?.enabled ?? false)) groups.enabled = f.groups.enabled
  if (f.kind !== 'github' && f.groups.claim.trim() !== (gb?.claim ?? (f.kind === 'oidc' ? 'groups' : '')))
    groups.claim = f.groups.claim.trim()
  if (f.groups.missing !== (gb?.missing ?? 'keep')) groups.missing = f.groups.missing
  const rows = f.groups.map.filter((e) => e.value !== '' || e.groupId !== '')
  if (JSON.stringify(rows) !== JSON.stringify(gb?.map ?? [])) groups.map = rows
  if (f.groups.allow.trim() !== (gb?.allow ?? '').trim()) groups.allow = lines(f.groups.allow)
  if (Object.keys(groups).length > 0) out.groups = groups
  if (f.kind === 'oidc') {
    const o = f.oidc
    const b = base?.oidc
    const block: NonNullable<ProviderInput['oidc']> = {}
    if (!b || o.issuer !== b.issuer) block.issuer = o.issuer.trim()
    if (!b || o.clientId !== b.clientId) block.clientId = o.clientId.trim()
    if (o.secret !== null) block.clientSecret = o.secret
    if (!b || o.scopes !== b.scopes) block.scopes = words(o.scopes)
    if (Object.keys(block).length || !was) out.oidc = block
    if (was?.template === 'apple') {
      const a = f.apple
      const ab = base?.apple
      const apple: NonNullable<ProviderInput['apple']> = {}
      if (a.teamId !== ab?.teamId) apple.teamId = a.teamId.trim()
      if (a.keyId !== ab?.keyId) apple.keyId = a.keyId.trim()
      if (a.privateKey !== null) apple.privateKey = a.privateKey
      if (Object.keys(apple).length) out.apple = apple
    }
  } else if (f.kind === 'saml') {
    const s = f.saml
    const b = base?.saml
    const block: NonNullable<ProviderInput['saml']> = {}
    if (!b || s.metadataUrl !== b.metadataUrl) block.metadataUrl = s.metadataUrl.trim()
    if (!b || s.metadataXml !== b.metadataXml) block.metadataXml = s.metadataXml
    if (!b || s.entityId !== b.entityId) block.entityId = s.entityId.trim()
    if (!b || s.email !== b.email || s.name !== b.name || s.username !== b.username)
      block.attributes = { email: s.email.trim(), name: s.name.trim(), username: s.username.trim() }
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
    if (Object.keys(block).length || !was) out.github = block
  }
  return out
}

function ProviderForm({ initial, onDone }: { initial: AuthProvider | null; onDone: () => void }) {
  const api = useProviderApi()
  const org = api.tenant !== null
  const changed = useChanged()
  const [f, setF] = useState<F>(() => (initial ? formOf(initial) : blank()))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const messageRef = useRef<HTMLParagraphElement>(null)
  const set = (patch: Partial<F>) => setF((x) => ({ ...x, ...patch }))
  const edit = initial !== null
  const input = inputOf(f, initial)
  // The site's trust switch is not an organisation's: its provider vouches for its verified domains.
  if (org) delete input.mergeVerifiedEmail
  const dirty = Object.keys(input).length > 0

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const saved = edit ? await api.patch(initial.id, input) : await api.create(input)
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
    <form
      className="grid min-w-0 grid-cols-1 gap-4"
      onSubmit={(e) => void submit(e)}
      noValidate
      data-slot="provider-form"
    >
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <fieldset className="grid min-w-0 grid-cols-1 gap-4">
        <legend className="pb-1 text-sm font-semibold">{tp.common}</legend>
        {!edit && (
          <Field
            label={tp.slug}
            requiredMark
            help={org ? tp.org.slugHelp : tp.slugHelp}
            value={f.slug}
            onChange={(e) => set({ slug: e.target.value })}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            maxLength={32}
            className="break-all"
          />
        )}
        {edit && <ReadOnly label={tp.slug} value={f.slug} />}
        <div className="grid gap-1.5">
          <Label htmlFor="provider-kind">{tp.kind}</Label>
          <NativeSelect
            id="provider-kind"
            value={f.kind}
            disabled={edit}
            onChange={(e) => set({ kind: e.target.value as ProviderKind })}
          >
            {(org ? ORG_KINDS : KINDS).map((k) => (
              <option key={k} value={k}>
                {tp.kinds[k]}
              </option>
            ))}
          </NativeSelect>
          {edit && <p className="text-xs text-muted-foreground">{tp.kindFixed}</p>}
        </div>
        {/* An organisation's own provider is its single sign-on: no class to choose. */}
        <div className={org ? 'hidden' : 'grid gap-1.5'}>
          <Label htmlFor="provider-class">{tp.classLabel}</Label>
          <NativeSelect
            id="provider-class"
            value={f.kind === 'github' ? 'social' : f.class}
            disabled={edit || f.kind === 'github'}
            onChange={(e) => set({ class: e.target.value as ProviderClass })}
          >
            <option value="social">{tp.classes.social}</option>
            <option value="sso">{tp.classes.sso}</option>
          </NativeSelect>
          <p className="text-xs text-muted-foreground">{edit ? tp.classFixed : tp.classHelp}</p>
        </div>
        {initial?.template && (
          <p className="text-xs text-muted-foreground" data-slot="from-template">
            {tp.fromTemplate(templateName(initial.template))}
          </p>
        )}
        <Field
          label={tp.name}
          requiredMark
          help={tp.nameHelp}
          value={f.name}
          onChange={(e) => set({ name: e.target.value })}
          maxLength={60}
          autoComplete="off"
        />
        <Field
          label={tp.order}
          help={tp.orderHelp}
          type="number"
          inputMode="numeric"
          value={f.order}
          onChange={(e) => set({ order: e.target.value })}
        />
        <Check label={tp.enabledLabel} checked={f.enabled} onChange={(on) => set({ enabled: on })} />
        <p className="text-xs text-muted-foreground" data-slot="registration-note">
          {tp.registrationNote}{' '}
          <Link to={adminPath('settings', undefined, api.tenant)} className="text-brand underline underline-offset-4">
            {tp.registrationNoteLink}
          </Link>
        </p>
        {org ? (
          <p className="text-xs text-muted-foreground" data-slot="org-trust">
            {tp.org.trust}
          </p>
        ) : (
          <Check
            label={tp.mergeVerifiedEmail}
            help={tp.mergeVerifiedEmailHelp}
            checked={f.mergeVerifiedEmail}
            onChange={(on) => set({ mergeVerifiedEmail: on })}
          />
        )}
      </fieldset>

      {f.kind === 'oidc' && <OidcFields f={f} set={set} initial={initial} />}
      {f.kind === 'saml' && <SamlFields f={f} set={set} initial={initial} />}
      {f.kind === 'github' && <GithubFields f={f} set={set} initial={initial} />}

      <GroupsFields f={f} set={set} />

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

// How the provider's groups claim becomes synced groups (see "Groups from sign-in").
function GroupsFields({ f, set }: { f: F; set: (p: Partial<F>) => void }) {
  const tgp = tg.provider
  const [find, setFind] = useState('')
  const q = useDebounced(find.trim(), 200)
  const synced = useSyncedGroups(q)
  // The names of the groups seen in any search, so a chosen group keeps its name when the search moves on.
  const [names, setNames] = useState<Record<string, string>>({})
  const items = synced.data?.items
  const [seen, setSeen] = useState(items)
  if (items !== seen) {
    setSeen(items)
    if (items) setNames((old) => ({ ...old, ...Object.fromEntries(items.map((c) => [c.id, c.name])) }))
  }
  const g = f.groups
  const up = (p: Partial<F['groups']>) => set({ groups: { ...g, ...p } })
  const choices = items ?? []
  // Every synced group is in the list only when nothing was searched and there is no further page.
  const complete = synced.isSuccess && q === '' && !synced.data.next
  const labelOf = (id: string) => names[id] ?? (complete ? tgp.unknownGroup : id)
  const setRow = (i: number, p: Partial<{ value: string; groupId: string }>) =>
    up({ map: g.map.map((e, j) => (j === i ? { ...e, ...p } : e)) })
  const claimHelp = f.kind === 'oidc' ? tgp.claimOidcHelp : f.kind === 'saml' ? tgp.claimSamlHelp : tgp.claimGithubHelp
  return (
    <fieldset className="grid min-w-0 grid-cols-1 gap-4" data-slot="groups-fields">
      <legend className="pb-1 text-sm font-semibold">{tgp.title}</legend>
      <p className="text-xs text-muted-foreground">{tgp.help}</p>
      <Check label={tgp.enabled} checked={g.enabled} onChange={(on) => up({ enabled: on })} />
      {f.kind !== 'github' ? (
        <Field
          label={tgp.claim}
          help={claimHelp}
          value={g.claim}
          onChange={(e) => up({ claim: e.target.value })}
          autoComplete="off"
          spellCheck={false}
          maxLength={256}
          className="break-all"
        />
      ) : (
        <p className="text-xs text-muted-foreground">{claimHelp}</p>
      )}
      <div className="grid gap-1.5">
        <Label htmlFor="groups-missing">{tgp.missing}</Label>
        <NativeSelect
          id="groups-missing"
          value={g.missing}
          onChange={(e) => up({ missing: e.target.value as 'keep' | 'clear' })}
        >
          <option value="keep">{tgp.missingKeep}</option>
          <option value="clear">{tgp.missingClear}</option>
        </NativeSelect>
        <p className="text-xs text-muted-foreground">{tgp.missingHelp}</p>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="groups-allow">{tgp.allow}</Label>
        <Textarea
          id="groups-allow"
          value={g.allow}
          onChange={(e) => up({ allow: e.target.value })}
          rows={3}
          autoComplete="off"
          spellCheck={false}
          className="font-mono text-base break-all sm:text-sm"
          data-slot="groups-allow"
        />
        <p className="text-xs text-muted-foreground">{tgp.allowHelp}</p>
      </div>
      <div className="grid gap-2" data-slot="groups-map">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium">
            {tgp.map} <span className="font-normal text-muted-foreground">({tgp.rows(g.map.length)})</span>
          </span>
        </div>
        <p className="text-xs text-muted-foreground">{tgp.mapHelp}</p>
        {complete && choices.length === 0 && g.map.length === 0 && (
          <p className="text-xs text-muted-foreground">{tgp.noSynced}</p>
        )}
        {!(complete && choices.length === 0) && (
          <div className="grid gap-1">
            <Input
              type="search"
              value={find}
              onChange={(e) => setFind(e.target.value)}
              aria-label={tgp.findGroup}
              placeholder={tgp.findGroup}
              autoComplete="off"
              spellCheck={false}
              className="sm:max-w-xs"
            />
            <p className="text-xs text-muted-foreground" aria-live="polite">
              {synced.isSuccess && q !== '' && choices.length === 0
                ? tgp.findNone
                : synced.data?.next
                  ? tgp.findMore
                  : ''}
            </p>
          </div>
        )}
        {g.map.map((e, i) => (
          <div
            key={i}
            className="grid min-w-0 gap-2 rounded-md border p-2 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
            data-row={i}
          >
            <div className="grid gap-1">
              <Label htmlFor={`groups-value-${i}`} className="text-xs">
                {tgp.value}
              </Label>
              <Input
                id={`groups-value-${i}`}
                value={e.value}
                onChange={(ev) => setRow(i, { value: ev.target.value })}
                autoComplete="off"
                spellCheck={false}
                className="break-all"
              />
            </div>
            <div className="grid gap-1">
              <Label htmlFor={`groups-group-${i}`} className="text-xs">
                {tgp.group}
              </Label>
              <NativeSelect
                id={`groups-group-${i}`}
                value={e.groupId}
                onChange={(ev) => setRow(i, { groupId: ev.target.value })}
              >
                <option value="">{tgp.chooseGroup}</option>
                {e.groupId !== '' && !choices.some((c) => c.id === e.groupId) && (
                  <option value={e.groupId}>{labelOf(e.groupId)}</option>
                )}
                {choices.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </NativeSelect>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label={`${tgp.removeRow}: ${e.value}`}
              onClick={() => up({ map: g.map.filter((_, j) => j !== i) })}
            >
              <Trash2 aria-hidden="true" />
              {tgp.removeRow}
            </Button>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => up({ map: [...g.map, { value: '', groupId: '' }] })}
          >
            <Plus aria-hidden="true" />
            {tgp.addRow}
          </Button>
        </div>
      </div>
    </fieldset>
  )
}

function Check({
  label,
  help,
  checked,
  onChange,
}: {
  label: string
  help?: string
  checked: boolean
  onChange: (on: boolean) => void
}) {
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
function ReadOnly({
  label,
  value,
  help,
  multiline,
}: {
  label: string
  value: string
  help?: string
  multiline?: boolean
}) {
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
          <Textarea
            readOnly
            value={value}
            aria-label={label}
            className="min-w-0 flex-1 basis-56 font-mono text-xs break-all"
            rows={4}
            onFocus={(e) => e.currentTarget.select()}
          />
        ) : (
          <Input
            readOnly
            value={value}
            aria-label={label}
            className="min-w-0 flex-1 basis-56 font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
        )}
        <Button type="button" variant="outline" size="sm" onClick={() => void copy()}>
          <Copy aria-hidden="true" />
          {copied ? tp.copied : tp.copy}
        </Button>
      </div>
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  )
}

// A secret the server never shows again: saved · change, clear.
function WriteOnly({
  label,
  help,
  stored,
  value,
  onChange,
  required,
}: {
  label: string
  help?: string
  stored: boolean
  value: string | null
  onChange: (v: string | null) => void
  required?: boolean
}) {
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
          <Eraser aria-hidden="true" />
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
    body = (
      <Input
        type="password"
        autoComplete="new-password"
        aria-label={label}
        aria-required={required ? true : undefined}
        placeholder={stored ? tp.secretKeep : tp.secretNone}
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}
      />
    )
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
  // A template's issuer and addresses are its own; Apple's secret is signed by the server with the team's key.
  const fromTemplate = !!initial?.template
  const apple = initial?.template === 'apple'
  return (
    <fieldset className="grid min-w-0 grid-cols-1 gap-4">
      <legend className="pb-1 text-sm font-semibold">{tp.kinds.oidc}</legend>
      {fromTemplate ? (
        <ReadOnly label={tp.oidc.issuer} value={o.issuer} help={tp.templateFixed} />
      ) : (
        <Field
          label={tp.oidc.issuer}
          requiredMark
          help={tp.oidc.issuerHelp}
          value={o.issuer}
          onChange={(e) => up({ issuer: e.target.value })}
          autoComplete="off"
          spellCheck={false}
          className="break-all"
        />
      )}
      <Field
        label={tp.oidc.clientId}
        requiredMark
        help={apple ? tp.apple.clientIdHelp : undefined}
        value={o.clientId}
        onChange={(e) => up({ clientId: e.target.value })}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      {apple ? (
        <AppleFields
          value={f.apple}
          stored={initial?.apple?.hasKey === true}
          onChange={(a) => set({ apple: { ...f.apple, ...a } })}
        />
      ) : (
        <WriteOnly
          label={tp.oidc.clientSecret}
          help={fromTemplate ? undefined : tp.oidc.clientSecretHelp}
          stored={initial?.oidc?.hasSecret === true}
          value={o.secret}
          onChange={(v) => up({ secret: v })}
        />
      )}
      <Field
        label={tp.oidc.scopes}
        help={tp.oidc.scopesHelp}
        value={o.scopes}
        onChange={(e) => up({ scopes: e.target.value })}
        autoComplete="off"
        spellCheck={false}
      />
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
      <Field
        label={tp.saml.metadataUrl}
        help={tp.saml.metadataUrlHelp}
        value={s.metadataUrl}
        onChange={(e) => up({ metadataUrl: e.target.value })}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      <div className="grid gap-1.5">
        <Label htmlFor="saml-xml">{tp.saml.metadataXml}</Label>
        <Textarea
          id="saml-xml"
          value={s.metadataXml}
          onChange={(e) => up({ metadataXml: e.target.value })}
          spellCheck={false}
          className="min-w-0 font-mono text-xs break-all"
          rows={4}
        />
      </div>
      <Field
        label={tp.saml.entityId}
        help={tp.saml.entityIdHelp}
        value={s.entityId}
        onChange={(e) => up({ entityId: e.target.value })}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      <div className="grid min-w-0 gap-3">
        <div>
          <h4 className="text-sm font-medium">{tp.saml.attributes}</h4>
          <p className="text-xs text-muted-foreground">{tp.saml.attributesHelp}</p>
        </div>
        <Field
          label={tp.saml.attrEmail}
          value={s.email}
          onChange={(e) => up({ email: e.target.value })}
          autoComplete="off"
          spellCheck={false}
        />
        <Field
          label={tp.saml.attrName}
          value={s.name}
          onChange={(e) => up({ name: e.target.value })}
          autoComplete="off"
          spellCheck={false}
        />
        <Field
          label={tp.saml.attrUsername}
          value={s.username}
          onChange={(e) => up({ username: e.target.value })}
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <Field
        label={tp.saml.nameIdFormat}
        help={tp.saml.nameIdFormatHelp}
        value={s.nameIdFormat}
        onChange={(e) => up({ nameIdFormat: e.target.value })}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      <Check label={tp.saml.signRequests} checked={s.signRequests} onChange={(on) => up({ signRequests: on })} />
      <Check
        label={tp.saml.allowIdpInitiated}
        help={tp.saml.allowIdpInitiatedHelp}
        checked={s.allowIdpInitiated}
        onChange={(on) => up({ allowIdpInitiated: on })}
      />
      {saved && (
        <div className="grid min-w-0 gap-3 rounded-md border p-3" data-slot="saml-readonly">
          <ReadOnly label={tp.saml.idpEntityId} value={saved.idpEntityId} />
          <ReadOnly label={tp.saml.entityId} value={saved.entityId} />
          <ReadOnly label={tp.saml.acsUrl} value={saved.acsUrl} help={tp.saml.acsHelp} />
          <ReadOnly label={tp.saml.metadataPath} value={saved.metadataPath} />
          <ReadOnly
            label={tp.saml.certificate}
            value={saved.certificate}
            multiline
            help={`${tp.saml.certificateHelp} ${tp.saml.certificateExpires(absoluteTime(saved.certificateExpires))}`}
          />
        </div>
      )}
    </fieldset>
  )
}

function GithubFields({ f, set, initial }: Sub) {
  const g = f.github
  const up = (p: Partial<F['github']>) => set({ github: { ...g, ...p } })
  // The row of the GitHub App: its client is the App's, set where the App is.
  const app = initial?.github?.app === true
  return (
    <fieldset className="grid min-w-0 grid-cols-1 gap-4">
      <legend className="pb-1 text-sm font-semibold">{tp.kinds.github}</legend>
      {app && <p className="text-xs text-muted-foreground">{tp.github.appClient}</p>}
      <Field
        label={tp.github.baseUrl}
        help={tp.github.baseUrlHelp}
        value={g.baseUrl}
        onChange={(e) => up({ baseUrl: e.target.value })}
        disabled={app}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      <Field
        label={tp.github.clientId}
        requiredMark={!app}
        value={g.clientId}
        onChange={(e) => up({ clientId: e.target.value })}
        disabled={app}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      {!app && (
        <WriteOnly
          label={tp.github.clientSecret}
          required
          stored={initial?.github?.hasSecret === true}
          value={g.secret}
          onChange={(v) => up({ secret: v })}
        />
      )}
      {initial?.github && <ReadOnly label={tp.github.redirectUrl} value={initial.github.redirectUrl} />}
    </fieldset>
  )
}

// --- adding one: a template, or a provider of one's own -----------------------------------

const TEMPLATE_NAMES: Record<string, string> = {
  google: 'Google',
  microsoft: 'Microsoft',
  apple: 'Apple',
  github: 'GitHub',
  'gitlab-com': 'GitLab.com',
}
const templateName = (id: string) => TEMPLATE_NAMES[id] ?? id

// "Add a sign-in method": first a template (the common ones, the rest under "More templates") or a custom provider,
// then the few fields that one needs.
function NewProvider({ onDone }: { onDone: () => void }) {
  const org = useProviderApi().tenant !== null
  // Templates are the site's social sign-in: an organisation goes straight to its own provider.
  const [step, setStep] = useState<ProviderTemplate | 'pick' | 'custom'>(org ? 'custom' : 'pick')
  const title = step === 'pick' ? tp.pickTitle : step === 'custom' ? tp.createTitle : tp.templateTitle(step.name)
  return (
    <>
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription className={step === 'pick' ? '' : 'sr-only'}>{tp.pickHelp}</DialogDescription>
      </DialogHeader>
      {step === 'pick' ? (
        <TemplatePicker onPick={setStep} onCustom={() => setStep('custom')} />
      ) : (
        <>
          {!org && (
            <div>
              <Button type="button" variant="ghost" size="sm" onClick={() => setStep('pick')}>
                <ArrowLeft aria-hidden="true" />
                {tp.back}
              </Button>
            </div>
          )}
          {step === 'custom' ? (
            <ProviderForm initial={null} onDone={onDone} />
          ) : (
            <TemplateForm key={step.id} tm={step} onDone={onDone} />
          )}
        </>
      )}
    </>
  )
}

function TemplateTile({
  id,
  icon,
  name,
  help,
  onClick,
}: {
  id: string
  icon: string
  name: string
  help: string
  onClick: () => void
}) {
  return (
    <li className="min-w-0">
      <button
        type="button"
        onClick={onClick}
        data-template={id}
        className="flex h-full w-full min-w-0 items-start gap-3 rounded-lg border p-3 text-left transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        <ProviderIcon icon={icon} className="mt-0.5 size-6 shrink-0" />
        <span className="min-w-0">
          <span className="block text-sm font-medium break-words">{name}</span>
          <span className="block text-xs break-words text-muted-foreground">{help}</span>
        </span>
      </button>
    </li>
  )
}

function TemplatePicker({ onPick, onCustom }: { onPick: (t: ProviderTemplate) => void; onCustom: () => void }) {
  const q = useProviderTemplates()
  const [more, setMore] = useState(false)
  if (q.isPending) return <div aria-busy="true" className="h-32 animate-pulse rounded-lg bg-muted" />
  if (q.isError)
    return <ErrorState message={t.fmt.labelled(tp.loadFailed, q.error.message)} onRetry={() => void q.refetch()} />
  const list = q.data.templates
  const tile = (tm: ProviderTemplate) => (
    <TemplateTile
      key={tm.id}
      id={tm.id}
      icon={tm.icon}
      name={tm.name}
      help={tp.templateHelp[tm.id] ?? tm.issuer}
      onClick={() => onPick(tm)}
    />
  )
  const rest = list.filter((x) => !x.common)
  const regions = [...new Set(rest.map((x) => x.region))]
  return (
    <div className="grid min-w-0 gap-4" data-slot="template-picker">
      <ul className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2">{list.filter((x) => x.common).map(tile)}</ul>
      {rest.length > 0 && (
        <div className="grid min-w-0 gap-2">
          <div>
            <Button type="button" variant="outline" size="sm" aria-expanded={more} onClick={() => setMore((m) => !m)}>
              {more ? <ChevronUp aria-hidden="true" /> : <ChevronDown aria-hidden="true" />}
              {tp.moreTemplates}
            </Button>
          </div>
          {more &&
            regions.map((r) => (
              <section key={r} className="grid min-w-0 gap-2" data-region={r}>
                <h4 className="text-xs font-medium text-muted-foreground">{tp.regions[r] ?? r}</h4>
                <ul className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2">
                  {rest.filter((x) => x.region === r).map(tile)}
                </ul>
              </section>
            ))}
        </div>
      )}
      <ul className="grid min-w-0 grid-cols-1">
        <TemplateTile id="custom" icon="preset:key" name={tp.custom} help={tp.customHelp} onClick={onCustom} />
      </ul>
    </div>
  )
}

// A provider from a template: its slug and name (the template's by default), and its client.
function TemplateForm({ tm, onDone }: { tm: ProviderTemplate; onDone: () => void }) {
  const changed = useChanged()
  const apple = tm.secret === 'apple'
  const [slug, setSlug] = useState<string>(tm.id)
  const [name, setName] = useState(tm.name)
  const [clientId, setClientId] = useState('')
  const [secret, setSecret] = useState('')
  const [keys, setKeys] = useState<F['apple']>({ teamId: '', keyId: '', privateKey: null })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const messageRef = useRef<HTMLParagraphElement>(null)
  const redirect = `${window.location.origin}/api/auth/${tm.kind}/${slug.trim() || tm.id}/callback`
  const ready =
    slug.trim() !== '' &&
    name.trim() !== '' &&
    clientId.trim() !== '' &&
    (apple ? keys.teamId.trim() !== '' && keys.keyId.trim() !== '' && !!keys.privateKey : secret !== '')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || !ready) return
    setBusy(true)
    setError('')
    const input: ProviderInput = { template: tm.id, slug: slug.trim(), name: name.trim(), clientId: clientId.trim() }
    if (apple) input.apple = { teamId: keys.teamId.trim(), keyId: keys.keyId.trim(), privateKey: keys.privateKey ?? '' }
    else input.clientSecret = secret
    try {
      const saved = await adminApi.createAuthProvider(input)
      changed()
      toast.success(tp.created)
      if (!saved.status.ok) toast.warning(tp.unreachable)
      onDone()
      return
    } catch (err) {
      setError(refusal(err))
      setTimeout(() => messageRef.current?.focus(), 0)
    }
    setBusy(false)
  }

  return (
    <form
      className="grid min-w-0 grid-cols-1 gap-4"
      onSubmit={(e) => void submit(e)}
      noValidate
      data-slot="template-form"
      data-template={tm.id}
    >
      <div className="flex min-w-0 items-start gap-3">
        <ProviderIcon icon={tm.icon} className="mt-0.5 size-6 shrink-0" />
        <p className="min-w-0 text-xs break-words text-muted-foreground">{tp.templateHelp[tm.id] ?? tm.issuer}</p>
      </div>
      <FormMessage focusRef={messageRef}>{error}</FormMessage>
      <Field
        label={tp.slug}
        requiredMark
        help={tp.slugHelp}
        value={slug}
        onChange={(e) => setSlug(e.target.value)}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        maxLength={32}
      />
      <Field
        label={tp.name}
        requiredMark
        help={tp.nameHelp}
        value={name}
        onChange={(e) => setName(e.target.value)}
        maxLength={60}
        autoComplete="off"
      />
      <ReadOnly label={tp.oidc.redirectUrl} value={redirect} help={tp.redirectHint} />
      <Field
        label={tp.oidc.clientId}
        requiredMark
        help={apple ? tp.apple.clientIdHelp : undefined}
        value={clientId}
        onChange={(e) => setClientId(e.target.value)}
        autoComplete="off"
        spellCheck={false}
        className="break-all"
      />
      {apple ? (
        <AppleFields value={keys} stored={false} onChange={(a) => setKeys((k) => ({ ...k, ...a }))} />
      ) : (
        <div className="grid min-w-0 gap-1.5">
          <Label htmlFor={`template-secret-${tm.id}`}>
            {tp.oidc.clientSecret}
            <RequiredMark />
          </Label>
          <Input
            id={`template-secret-${tm.id}`}
            type="password"
            autoComplete="new-password"
            aria-required
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
          />
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy || !ready}>
          {busy ? tp.creating : tp.create}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {tp.cancel}
        </Button>
      </div>
    </form>
  )
}

// Sign in with Apple's client secret is signed by the server: the team id, the key id and the key (.p8), which is
// write-only once stored.
function AppleFields({
  value,
  stored,
  onChange,
}: {
  value: F['apple']
  stored: boolean
  onChange: (a: Partial<F['apple']>) => void
}) {
  const ta = tp.apple
  const [replacing, setReplacing] = useState(!stored)
  const fileRef = useRef<HTMLInputElement>(null)
  const read = async (file: File | undefined) => {
    if (file) onChange({ privateKey: await file.text() })
  }
  return (
    <div className="grid min-w-0 gap-4" data-slot="apple-fields">
      <Field
        label={ta.teamId}
        requiredMark
        help={ta.teamIdHelp}
        value={value.teamId}
        onChange={(e) => onChange({ teamId: e.target.value })}
        autoComplete="off"
        autoCapitalize="characters"
        spellCheck={false}
        maxLength={10}
      />
      <Field
        label={ta.keyId}
        requiredMark
        help={ta.keyIdHelp}
        value={value.keyId}
        onChange={(e) => onChange({ keyId: e.target.value })}
        autoComplete="off"
        autoCapitalize="characters"
        spellCheck={false}
        maxLength={10}
      />
      <div className="grid min-w-0 gap-1.5">
        <Label htmlFor="apple-key">
          {ta.privateKey}
          <RequiredMark />
        </Label>
        {!replacing ? (
          <div className="flex flex-wrap items-center gap-2 text-sm" data-slot="secret-saved">
            <span>
              {ta.keySaved} ·{' '}
              <button type="button" className="underline underline-offset-4" onClick={() => setReplacing(true)}>
                {ta.keyReplace}
              </button>
            </span>
          </div>
        ) : (
          <>
            <Textarea
              id="apple-key"
              value={value.privateKey ?? ''}
              onChange={(e) => onChange({ privateKey: e.target.value === '' ? null : e.target.value })}
              spellCheck={false}
              autoComplete="off"
              placeholder="-----BEGIN PRIVATE KEY-----"
              className="min-w-0 font-mono text-xs break-all"
              rows={4}
            />
            <div>
              <input
                ref={fileRef}
                type="file"
                accept=".p8,.pem,.txt,text/plain"
                className="sr-only"
                tabIndex={-1}
                aria-hidden="true"
                onChange={(e) => void read(e.target.files?.[0])}
              />
              <Button type="button" variant="outline" size="sm" onClick={() => fileRef.current?.click()}>
                <FileKey aria-hidden="true" />
                {ta.chooseFile}
              </Button>
            </div>
          </>
        )}
        <p className="text-xs text-muted-foreground">{ta.privateKeyHelp}</p>
      </div>
    </div>
  )
}
