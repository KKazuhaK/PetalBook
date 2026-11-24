import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import {
  useAdminForge,
  useAdminConnectionProviders,
  useCheckAdminForge,
  useCreateAdminForge,
  useDeleteAdminForge,
  usePatchAdminForge,
} from '@/api/admin'
import { ApiError } from '@/api/client'
import type { AdminForge, AdminProviderRow } from '@/api/types'
import { BrandIcon } from '@/components/brand/BrandIcon'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
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
import { Label } from '@/components/ui/label'
import { absoluteTime } from '@/lib/time'
import { t } from '@/i18n'
import { tf } from '@/i18n/forge'

// The hosts an administrator sets up with an OAuth application (GitLab, Gitea, Forgejo, Codeberg), one card
// each, and the form to add one (docs/api.md, "Connections to GitLab and Gitea"). GitHub's App and a plain Git
// address have their own place in "Storage & sync"; every other provider of the server is a card here.
export function ForgeCards() {
  const list = useAdminConnectionProviders()
  const [adding, setAdding] = useState(false)
  if (list.isPending) return <PageFallback />
  if (list.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(tf.admin.loadFailed, list.error.message)}
        onRetry={() => void list.refetch()}
      />
    )
  const hosts = list.data.providers.filter((p) => p.type !== 'github' && p.type !== 'git')
  return (
    <section aria-labelledby="forge-title" className="space-y-4" data-testid="forge-cards">
      <div className="space-y-1">
        <h3 id="forge-title" className="text-sm font-medium">
          {tf.admin.title}
        </h3>
        <p className="text-sm text-muted-foreground">{tf.admin.intro}</p>
      </div>
      {hosts.length === 0 && !adding && <p className="text-sm text-muted-foreground">{tf.admin.none}</p>}
      {hosts.map((h) => (
        <ForgeCard key={h.id} row={h} />
      ))}
      {adding ? (
        <AddForge onDone={() => setAdding(false)} />
      ) : (
        <div>
          <Button type="button" variant="outline" onClick={() => setAdding(true)}>
            {tf.admin.add}
          </Button>
        </div>
      )}
    </section>
  )
}

// The way an administrator's request failed, in words: the reason of a refused configuration first, then the
// code, then what the server said.
function explain(e: unknown): string {
  if (!(e instanceof ApiError)) return tf.admin.errors.failed
  const body = (e.body ?? {}) as { reason?: unknown }
  const reasons = tf.admin.errors.reasons as Record<string, string>
  const codes = tf.admin.errors.codes as Record<string, string>
  if (typeof body.reason === 'string' && Object.hasOwn(reasons, body.reason)) return reasons[body.reason]
  const code = e.code as string
  return Object.hasOwn(codes, code) ? codes[code] : e.message || tf.admin.errors.failed
}

function ForgeCard({ row }: { row: AdminProviderRow }) {
  const view = useAdminForge(row.id)
  if (view.isPending) return <PageFallback />
  if (view.isError)
    return (
      <ErrorState
        message={t.fmt.labelled(tf.admin.loadFailed, view.error.message)}
        onRetry={() => void view.refetch()}
      />
    )
  return <ForgeDetails forge={view.data} />
}

function partialText(p: AdminForge['partialSync']): string {
  return p === 'yes' ? tf.admin.partialYes : p === 'no' ? tf.admin.partialNo : tf.admin.partialUnknown
}

function ForgeDetails({ forge }: { forge: AdminForge }) {
  const patch = usePatchAdminForge(forge.id)
  const check = useCheckAdminForge(forge.id)
  const remove = useDeleteAdminForge(forge.id)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(forge.name)
  const [clientId, setClientId] = useState(forge.clientId)
  const [secret, setSecret] = useState('')
  const [error, setError] = useState('')
  const [confirm, setConfirm] = useState(false)
  const inUse = forge.spaces > 0 || forge.connections > 0

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (patch.isPending) return
    setError('')
    try {
      await patch.mutateAsync({
        ...(name.trim() !== forge.name ? { name: name.trim() } : {}),
        ...(clientId.trim() !== forge.clientId ? { clientId: clientId.trim() } : {}),
        ...(secret ? { clientSecret: secret } : {}),
      })
      setSecret('')
      setEditing(false)
      toast.success(tf.admin.saved)
    } catch (err) {
      setError(explain(err))
    } finally {
      // The mutation holds the request, the secret included: let go of it.
      patch.reset()
    }
  }
  const recheck = async () => {
    try {
      await check.mutateAsync()
      toast.success(tf.admin.checked)
    } catch (err) {
      toast.error(explain(err))
    }
  }
  const doRemove = async () => {
    try {
      await remove.mutateAsync()
      setConfirm(false)
      toast.success(tf.admin.removed)
    } catch (err) {
      setConfirm(false)
      toast.error(explain(err))
    }
  }

  return (
    <SettingsSection
      title={forge.name}
      id={`forge-${forge.id}-title`}
      icon={<BrandIcon type={forge.type} className="size-5" />}
    >
      <div className="grid grid-cols-1 gap-2 rounded-lg border p-3 text-sm" data-provider={forge.id}>
        <p className="flex flex-wrap items-center gap-2 font-medium break-all">
          {forge.host}
          <Badge variant={forge.configured ? 'secondary' : 'destructive'}>
            {forge.configured ? tf.admin.stateOk : tf.admin.stateDown}
          </Badge>
        </p>
        <dl className="grid grid-cols-1 gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
          <dt className="text-muted-foreground">{tf.admin.partial}</dt>
          <dd className="break-words">{partialText(forge.partialSync)}</dd>
          <dt className="text-muted-foreground">{tf.admin.versionLabel}</dt>
          <dd className="break-words">{forge.version || tf.admin.versionUnknown}</dd>
          <dt className="text-muted-foreground">{tf.admin.clientId}</dt>
          <dd className="font-mono break-all">{forge.clientId}</dd>
          <dt className="text-muted-foreground">{tf.admin.scopesLabel}</dt>
          <dd className="break-words">{tf.admin.scopes(forge.scopes)}</dd>
        </dl>
        <p className="text-xs">{tf.admin.usage(forge.spaces, forge.connections)}</p>
        {!forge.configured && (
          <p role="alert" className="rounded-md border border-warning/50 bg-warning/5 px-3 py-2 text-xs break-words">
            {tf.admin.notConfigured}
          </p>
        )}
        <div className="grid grid-cols-1 gap-1 text-xs text-muted-foreground">
          <p>{tf.admin.callbackHelp}</p>
          <code className="font-mono break-all select-all">{forge.callbackUrl || tf.admin.noPublicUrl}</code>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" size="sm" disabled={check.isPending} onClick={() => void recheck()}>
            {check.isPending && <Spinner />}
            {check.isPending ? tf.admin.checking : tf.admin.check}
          </Button>
          {forge.checked && (
            <span className="text-xs text-muted-foreground">
              {t.fmt.labelled(tf.admin.checkedAt, absoluteTime(forge.checked))}
            </span>
          )}
        </div>
      </div>

      {editing ? (
        <form
          className="grid grid-cols-1 gap-4"
          onSubmit={(e) => void submit(e)}
          noValidate
          data-testid="forge-edit-form"
        >
          <Field
            label={tf.admin.name}
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoComplete="off"
            className="w-full min-w-0"
          />
          <Field
            label={tf.admin.clientId}
            value={clientId}
            onChange={(e) => setClientId(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="w-full min-w-0"
          />
          <Field
            label={tf.admin.clientSecret}
            help={forge.hasSecret ? tf.admin.secretKept : tf.admin.secretHelp}
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder={forge.hasSecret ? '••••••••' : ''}
            autoComplete="new-password"
            className="w-full min-w-0"
          />
          <FormMessage>{error}</FormMessage>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" disabled={patch.isPending}>
              {patch.isPending && <Spinner />}
              {patch.isPending ? tf.admin.saving : tf.admin.save}
            </Button>
            <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
              {tf.admin.cancel}
            </Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" onClick={() => setEditing(true)}>
            {tf.admin.edit}
          </Button>
          <Button type="button" variant="outline" onClick={() => setConfirm(true)}>
            {tf.admin.remove}
          </Button>
        </div>
      )}

      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tf.admin.removeTitle(forge.name)}</AlertDialogTitle>
            <AlertDialogDescription>
              {inUse ? tf.admin.removeBlocked(forge.spaces, forge.connections) : tf.admin.removeDescription}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tf.admin.cancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending || inUse}
              onClick={() => void doRemove()}
            >
              {tf.admin.remove}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

// The kinds of host an administrator can add; the server names no other.
const KINDS = [
  { type: 'gitlab', label: () => tf.admin.typeGitLab },
  { type: 'gitea', label: () => tf.admin.typeGitea },
] as const

function AddForge({ onDone }: { onDone: () => void }) {
  const create = useCreateAdminForge()
  const [type, setType] = useState<string>(KINDS[0].type)
  const [id, setId] = useState('')
  const [host, setHost] = useState('')
  const [name, setName] = useState('')
  const [clientId, setClientId] = useState('')
  const [secret, setSecret] = useState('')
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (create.isPending) return
    setError('')
    if (!id.trim() || !host.trim() || !clientId.trim() || !secret) return setError(tf.admin.needFields)
    try {
      await create.mutateAsync({
        id: id.trim(),
        type,
        host: host.trim(),
        ...(name.trim() ? { name: name.trim() } : {}),
        clientId: clientId.trim(),
        clientSecret: secret,
      })
      setSecret('')
      toast.success(tf.admin.saved)
      onDone()
    } catch (err) {
      setError(explain(err))
    } finally {
      create.reset()
    }
  }
  return (
    <form
      className="grid grid-cols-1 gap-4 rounded-lg border p-3"
      onSubmit={(e) => void submit(e)}
      noValidate
      data-testid="forge-add-form"
    >
      <div className="grid gap-1">
        <h4 className="text-sm font-medium">{tf.admin.addTitle}</h4>
        <p className="text-xs text-muted-foreground">{tf.admin.addHelp}</p>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="forge-type">{tf.admin.typeLabel}</Label>
        <select
          id="forge-type"
          value={type}
          onChange={(e) => setType(e.target.value)}
          className="h-9 w-full min-w-0 rounded-md border bg-background px-2 text-sm"
        >
          {KINDS.map((k) => (
            <option key={k.type} value={k.type}>
              {k.label()}
            </option>
          ))}
        </select>
      </div>
      <Field
        label={tf.admin.host}
        help={tf.admin.hostHelp}
        value={host}
        onChange={(e) => setHost(e.target.value)}
        autoComplete="off"
        spellCheck={false}
        className="w-full min-w-0"
      />
      <Field
        label={tf.admin.id}
        help={tf.admin.idHelp}
        value={id}
        onChange={(e) => setId(e.target.value.toLowerCase())}
        autoComplete="off"
        spellCheck={false}
        className="w-full min-w-0"
      />
      <Field
        label={tf.admin.name}
        help={tf.admin.nameHelp}
        value={name}
        onChange={(e) => setName(e.target.value)}
        autoComplete="off"
        className="w-full min-w-0"
      />
      <Field
        label={tf.admin.clientId}
        value={clientId}
        onChange={(e) => setClientId(e.target.value)}
        autoComplete="off"
        spellCheck={false}
        className="w-full min-w-0"
      />
      <Field
        label={tf.admin.clientSecret}
        help={tf.admin.secretHelp}
        type="password"
        value={secret}
        onChange={(e) => setSecret(e.target.value)}
        autoComplete="new-password"
        className="w-full min-w-0"
      />
      <div className="grid grid-cols-1 gap-1 text-xs text-muted-foreground">
        <p>{tf.admin.callbackHelp}</p>
        <code className="font-mono break-all select-all">{`${window.location.origin}/api/connections/${id || '<id>'}/authorization/callback`}</code>
      </div>
      <FormMessage>{error}</FormMessage>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={create.isPending}>
          {create.isPending && <Spinner />}
          {create.isPending ? tf.admin.saving : tf.admin.save}
        </Button>
        <Button type="button" variant="ghost" onClick={onDone}>
          {tf.admin.cancel}
        </Button>
      </div>
    </form>
  )
}
