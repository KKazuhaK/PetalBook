// Settings of one space: where it keeps its Git repository. A space either
// lives on the server only, or follows a remote repository; this page connects
// it, shows how the connection is doing and disconnects it.

import { useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useParams } from 'react-router'
import { AlertTriangle, Check, CircleAlert, CirclePause, Cloud, CloudOff, Copy, KeyRound, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError, spaceApi } from '@/api/client'
import { useAcceptRemote, useConnectRemote, useDisconnectRemote, useRemote, useSession, useSpaces, useSyncRemote, useUpdateSpace } from '@/api/hooks'
import type { ConnectRemoteInput, Remote, RemoteKey, RemoteState, SpaceInfo } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
import { TopBar } from '@/components/layout/TopBar'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { checkFolderPath } from '@/lib/folders'
import { absoluteTime, relativeTime } from '@/lib/time'
import { spaceUrls } from '@/lib/urls'
import { useDocumentTitle } from '@/lib/useDocumentTitle'
import { t } from '@/i18n'

export default function SpaceSettingsPage() {
  const { space = '' } = useParams()
  const session = useSession().data
  const spaces = useSpaces()
  const current = spaces.data?.spaces.find((s) => s.id === space)
  useDocumentTitle(`${current ? t.remote.pageTitle(current.name) : t.remote.spaceSettings} · ${t.appName}`)

  let body: ReactNode
  if (session?.mode === 'single')
    body = (
      <>
        {current && <InboxSection space={current} readOnly />}
        <ImportSection space={space} />
        <p className="rounded-lg border p-4 text-sm text-muted-foreground">{t.remote.singleUser}</p>
      </>
    )
  else if (current && current.role !== 'owner') body = <p className="rounded-lg border p-4 text-sm text-muted-foreground">{t.remote.ownerOnly}</p>
  else if (!session || !current) body = <PageFallback />
  else
    body = (
      <>
        <InboxSection space={current} />
        <ImportSection space={space} />
        <RemoteSection space={space} />
      </>
    )

  return (
    <>
      <TopBar>
        <h1 className="truncate text-sm font-medium">{current ? t.remote.pageTitle(current.name) : t.remote.spaceSettings}</h1>
      </TopBar>
      <div className="mx-auto w-full max-w-[640px] flex-1 space-y-6 px-4 py-6 sm:px-5">{body}</div>
    </>
  )
}

function Section({ title, children, id }: { title: string; children: ReactNode; id?: string }) {
  return (
    <section aria-labelledby={id} className="grid gap-4 rounded-lg border p-4">
      <h2 id={id} className="text-sm font-medium">
        {title}
      </h2>
      {children}
    </section>
  )
}

// A way into the import page; the import itself has a page of its own.
function ImportSection({ space }: { space: string }) {
  return (
    <Section title={t.import.settingsTitle} id="import-title">
      <p className="text-sm text-muted-foreground">{t.import.settingsHelp}</p>
      <div>
        <Button variant="outline" render={<Link to={spaceUrls(space).import()} />}>
          {t.import.settingsLink}
        </Button>
      </div>
    </Section>
  )
}

// Where quick notes go. Single-user mode sets it with `--inbox`, so it is only
// shown there; in accounts mode the owner can change it, and nothing is moved.
function InboxSection({ space, readOnly = false }: { space: SpaceInfo; readOnly?: boolean }) {
  const update = useUpdateSpace()
  const [value, setValue] = useState(space.inbox)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (update.isPending) return
    const checked = checkFolderPath(value)
    if ('error' in checked) return setError(checked.error === t.folderRules.empty ? t.inboxSetting.invalid : checked.error)
    setError('')
    if (checked.path === space.inbox) return setValue(checked.path)
    try {
      const saved = await update.mutateAsync({ id: space.id, inbox: checked.path })
      setValue(saved.inbox)
      toast.success(t.inboxSetting.saved)
    } catch (err) {
      setError(err instanceof ApiError && err.code === 'invalid' ? err.message : t.fmt.labelled(t.inboxSetting.failed, err instanceof Error ? err.message : ''))
    }
  }

  return (
    <Section title={t.inboxSetting.title} id="inbox-title">
      <form className="grid gap-3" onSubmit={(e) => void submit(e)} noValidate>
        <Field label={t.inboxSetting.label} help={readOnly ? t.inboxSetting.readOnly : t.inboxSetting.help} value={readOnly ? space.inbox : value} onChange={(e) => setValue(e.target.value)} readOnly={readOnly} error={error} autoComplete="off" spellCheck={false} />
        {!readOnly && (
          <div>
            <Button type="submit" disabled={update.isPending || value === space.inbox}>
              {update.isPending && <Spinner />}
              {t.inboxSetting.save}
            </Button>
          </div>
        )}
      </form>
    </Section>
  )
}

function RemoteSection({ space }: { space: string }) {
  const remote = useRemote(space)
  return (
    <Section title={t.remote.title} id="remote-title">
      {remote.isPending ? (
        <PageFallback />
      ) : remote.isError ? (
        <ErrorState message={t.fmt.labelled(t.remote.loadFailed, remote.error.message)} onRetry={() => void remote.refetch()} />
      ) : remote.data === null ? (
        <>
          <p className="text-sm text-muted-foreground">{t.remote.intro}</p>
          <ConnectForm space={space} existing={null} />
        </>
      ) : (
        <Following space={space} remote={remote.data} />
      )}
    </Section>
  )
}

// --- the state of the connection ----------------------------------------------------

const STATE_ICON: Record<RemoteState, typeof Cloud> = { ok: Cloud, offline: CloudOff, rewritten: CirclePause, error: CircleAlert }

function StateBadge({ state }: { state: RemoteState }) {
  const Icon = STATE_ICON[state]
  return (
    <Badge variant={state === 'ok' ? 'secondary' : state === 'error' ? 'destructive' : 'outline'} className={state === 'offline' || state === 'rewritten' ? 'border-warning text-warning' : ''}>
      <Icon />
      {t.remote.state[state]}
    </Badge>
  )
}

function Following({ space, remote }: { space: string; remote: Remote }) {
  const sync = useSyncRemote(space)
  const disconnect = useDisconnectRemote(space)
  const [editing, setEditing] = useState(false)
  const [confirmDisconnect, setConfirmDisconnect] = useState(false)

  const syncNow = async () => {
    try {
      const r = await sync.mutateAsync()
      if (r.state === 'ok') toast.success(t.remote.synced)
      else toast.warning(t.fmt.labelled(t.remote.syncFailed, r.error || t.remote.state[r.state]))
    } catch (e) {
      toast.error(t.fmt.labelled(t.remote.syncFailed, e instanceof Error ? e.message : ''))
    }
  }
  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync()
      setConfirmDisconnect(false)
      toast.success(t.remote.disconnected)
    } catch (e) {
      setConfirmDisconnect(false)
      toast.error(t.fmt.labelled(t.remote.disconnectFailed, e instanceof Error ? e.message : ''))
    }
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge state={remote.state} />
        <span className="text-sm text-muted-foreground" role="status">
          {remote.lastSync ? t.fmt.labelled(t.remote.lastSync, t.fmt.note(relativeTime(remote.lastSync), absoluteTime(remote.lastSync))) : t.remote.never}
        </span>
      </div>

      {remote.state === 'offline' && <Notice icon={CloudOff} title={t.remote.offlineTitle} body={t.remote.offlineBody} error={remote.error} />}
      {remote.state === 'error' && <Notice icon={AlertTriangle} title={t.remote.errorTitle} body={t.remote.errorBody} error={remote.error} tone="error" />}
      {remote.state === 'rewritten' && <Rewritten space={space} remote={remote} />}

      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">{t.remote.address}</dt>
        <dd className="font-mono text-xs break-all">{remote.url}</dd>
        <dt className="text-muted-foreground">{t.remote.branch}</dt>
        <dd>{remote.branch}</dd>
        <dt className="text-muted-foreground">{t.remote.credentialLabel}</dt>
        <dd>{remote.credential.type === 'ssh' ? t.remote.ssh : t.remote.tokenOf(remote.credential.username)}</dd>
        {remote.hostKey && (
          <>
            <dt className="text-muted-foreground">{t.remote.hostKey}</dt>
            <dd>
              <code className="font-mono text-xs break-all">{remote.hostKey}</code>
              <p className="mt-0.5 text-xs text-muted-foreground">{t.remote.hostKeyHelp}</p>
            </dd>
          </>
        )}
      </dl>

      {remote.credential.type === 'ssh' && <PublicKey publicKey={remote.credential.publicKey} fingerprint={remote.credential.fingerprint} title={t.remote.sshKeyOf} />}

      <div className="flex flex-wrap gap-2">
        <Button onClick={() => void syncNow()} disabled={sync.isPending}>
          {sync.isPending ? <Spinner /> : <RefreshCw />}
          {sync.isPending ? t.remote.syncing : t.remote.syncNow}
        </Button>
        <Button variant="outline" onClick={() => setEditing((v) => !v)} aria-expanded={editing}>
          {editing ? t.remote.cancelEdit : t.remote.editConnection}
        </Button>
        <Button variant="outline" onClick={() => setConfirmDisconnect(true)}>
          {t.remote.disconnect}
        </Button>
      </div>

      {editing && (
        <div className="rounded-lg border bg-muted/20 p-4">
          <ConnectForm space={space} existing={remote} onDone={() => setEditing(false)} />
        </div>
      )}

      <AlertDialog open={confirmDisconnect} onOpenChange={setConfirmDisconnect}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.remote.disconnectTitle}</AlertDialogTitle>
            <AlertDialogDescription>{t.remote.disconnectDescription}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.remote.disconnectCancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={disconnect.isPending} onClick={() => void doDisconnect()}>
              {t.remote.disconnectConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Notice({ icon: Icon, title, body, error, tone = 'warning' }: { icon: typeof Cloud; title: string; body: string; error?: string; tone?: 'warning' | 'error' }) {
  return (
    <div role="alert" className={`grid gap-1 rounded-lg border p-3 text-sm ${tone === 'error' ? 'border-destructive/40 bg-destructive/5' : 'border-warning/50 bg-warning/5'}`}>
      <p className="flex items-center gap-1.5 font-medium">
        <Icon className="size-4 shrink-0" />
        {title}
      </p>
      {error && <p className="font-mono text-xs break-words">{error}</p>}
      <p className="text-muted-foreground">{body}</p>
    </div>
  )
}

// The remote's history no longer contains what the server last saw: writes are
// paused until the owner accepts the new history.
function Rewritten({ space, remote }: { space: string; remote: Remote }) {
  const accept = useAcceptRemote(space)
  const sync = useSyncRemote(space)
  const [open, setOpen] = useState(false)
  const r = remote.rewritten

  const doAccept = async () => {
    if (!r) return
    try {
      await accept.mutateAsync(r.remote)
      setOpen(false)
      toast.success(t.remote.accepted)
    } catch (e) {
      setOpen(false)
      // The remote moved on again: look at it afresh before deciding.
      if (e instanceof ApiError && e.code === 'conflict') {
        toast.warning(t.remote.acceptStale)
        void sync.mutateAsync().catch(() => undefined)
      } else toast.error(t.fmt.labelled(t.remote.acceptFailed, e instanceof Error ? e.message : ''))
    }
  }

  return (
    <div role="alert" className="grid gap-3 rounded-lg border border-warning/50 bg-warning/5 p-3 text-sm">
      <p className="flex items-center gap-1.5 font-medium">
        <CirclePause className="size-4 shrink-0" />
        {t.remote.rewrittenTitle}
      </p>
      <p className="text-muted-foreground">{t.remote.rewrittenBody}</p>
      {r && (
        <dl className="grid gap-2">
          <Commit label={t.remote.rewrittenLastGood} commit={r.lastGood} />
          <Commit label={t.remote.rewrittenRemote} commit={r.remote} />
        </dl>
      )}
      <p className="text-muted-foreground">{t.remote.rewrittenChoices}</p>
      <div>
        <Button variant="outline" onClick={() => setOpen(true)} disabled={!r}>
          {t.remote.accept}
        </Button>
      </div>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.remote.acceptTitle}</AlertDialogTitle>
            <AlertDialogDescription>{t.remote.acceptDescription}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.remote.acceptCancel}</AlertDialogCancel>
            <AlertDialogAction disabled={accept.isPending} onClick={() => void doAccept()}>
              {t.remote.acceptConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Commit({ label, commit }: { label: string; commit: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd>
        <code className="font-mono text-xs break-all select-all">{commit}</code>
      </dd>
    </div>
  )
}

// --- the public key ----------------------------------------------------------------

function PublicKey({ publicKey, fingerprint, title }: { publicKey: string; fingerprint: string; title: string }) {
  const area = useRef<HTMLTextAreaElement>(null)
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const copy = async () => {
    setFailed(false)
    try {
      await navigator.clipboard.writeText(publicKey)
      setCopied(true)
    } catch {
      // No clipboard permission: select it so Ctrl+C works.
      area.current?.select()
      setFailed(true)
    }
  }
  return (
    <div className="grid gap-2 rounded-lg border p-3" role="group" aria-label={title}>
      <h3 className="text-sm font-medium">{t.remote.addKeyTitle}</h3>
      <Textarea ref={area} readOnly rows={3} value={publicKey} aria-label={t.remote.publicKey} className="font-mono text-xs break-all" onFocus={(e) => e.currentTarget.select()} />
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" variant="outline" size="sm" onClick={() => void copy()}>
          {copied ? <Check /> : <Copy />}
          {copied ? t.remote.copied : t.remote.copyKey}
        </Button>
        <span role="status" className="text-xs text-destructive">
          {failed ? t.remote.copyFailed : ''}
        </span>
      </div>
      <p className="text-xs text-muted-foreground">{t.remote.addKeyHow}</p>
      <p className="text-xs text-muted-foreground">
        {t.remote.fingerprint}{t.fmt.colon}<code className="font-mono break-all">{fingerprint}</code>
      </p>
    </div>
  )
}

// --- connecting ------------------------------------------------------------------------

const looksSsh = (url: string) => /^(ssh:\/\/|[\w.-]+@[\w.-]+:)/.test(url.trim())
const looksHttps = (url: string) => /^https:\/\//i.test(url.trim())

// What the server said to a connect that did not go through, in plain words.
function Problem({ error }: { error: unknown }) {
  if (!error) return null
  const e = error instanceof ApiError ? error : null
  if (e?.status === 409)
    return (
      <div role="alert" className="grid gap-1 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm">
        <p className="font-medium">{t.remote.conflictTitle}</p>
        <p>{t.remote.conflictBody}</p>
        <p className="text-muted-foreground">{t.remote.conflictAdvice}</p>
        {e.message && <p className="font-mono text-xs break-words text-muted-foreground">{e.message}</p>}
      </div>
    )
  if (e?.code === 'unavailable')
    return (
      <div role="alert" className="grid gap-1 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm">
        <p className="font-medium">{t.remote.unreachableTitle}</p>
        <p className="break-words">{e.message}</p>
        <p className="text-muted-foreground">{t.remote.unreachableAdvice}</p>
      </div>
    )
  if (e?.code === 'forbidden')
    return (
      <div role="alert" className="grid gap-1 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm">
        <p className="font-medium">{t.remote.forbiddenTitle}</p>
        <p className="break-words">{e.message}</p>
      </div>
    )
  return <FormMessage>{t.fmt.labelled(t.remote.failed, error instanceof Error ? error.message : '')}</FormMessage>
}

function ConnectForm({ space, existing, onDone }: { space: string; existing: Remote | null; onDone?: () => void }) {
  const connect = useConnectRemote(space)
  const [url, setUrl] = useState(existing?.url ?? '')
  const [branch, setBranch] = useState(existing?.branch ?? '')
  const [kind, setKind] = useState<'ssh' | 'token'>(existing?.credential.type ?? 'ssh')
  // Until the person picks one, the address decides.
  const [chose, setChose] = useState(existing !== null)
  const [username, setUsername] = useState(existing?.credential.type === 'token' ? existing.credential.username : '')
  const [token, setToken] = useState('')
  const [key, setKey] = useState<RemoteKey | null>(existing?.credential.type === 'ssh' ? { publicKey: existing.credential.publicKey, fingerprint: existing.credential.fingerprint } : null)
  const [keyBusy, setKeyBusy] = useState(false)
  const [keyError, setKeyError] = useState('')
  const [rotateOpen, setRotateOpen] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [fieldError, setFieldError] = useState<{ url?: string; token?: string; key?: string }>({})

  const hadToken = existing?.credential.type === 'token'
  const pickByAddress = (next: string) => {
    setUrl(next)
    if (!chose) {
      if (looksSsh(next)) setKind('ssh')
      else if (looksHttps(next)) setKind('token')
    }
  }

  const makeKey = async (rotate: boolean) => {
    setKeyBusy(true)
    setKeyError('')
    try {
      setKey(await spaceApi(space).remoteKey(rotate))
      setFieldError((f) => ({ ...f, key: undefined }))
    } catch (e) {
      setKeyError(t.fmt.labelled(t.remote.keyFailed, e instanceof Error ? e.message : ''))
    }
    setKeyBusy(false)
    setRotateOpen(false)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (connect.isPending) return
    setError(null)
    const errs: typeof fieldError = {}
    const address = url.trim()
    if (address === '') errs.url = t.remote.urlRequired
    else if (kind === 'ssh' && !looksSsh(address)) errs.url = t.remote.mismatchSsh
    else if (kind === 'token' && !looksHttps(address)) errs.url = t.remote.mismatchToken
    if (kind === 'ssh' && !key) errs.key = t.remote.keyNeeded
    if (kind === 'token' && token === '' && !hadToken) errs.token = t.remote.tokenRequired
    setFieldError(errs)
    if (Object.keys(errs).length > 0) return
    const input: ConnectRemoteInput = {
      url: address,
      ...(branch.trim() ? { branch: branch.trim() } : {}),
      credential: kind === 'ssh' ? { type: 'ssh' } : { type: 'token', ...(username.trim() ? { username: username.trim() } : {}), ...(token ? { token } : {}) },
    }
    try {
      await connect.mutateAsync(input)
      setToken('')
      toast.success(existing ? t.remote.updated : t.remote.connected)
      onDone?.()
    } catch (err) {
      setError(err)
    } finally {
      // The mutation holds the request, token included: let go of it at once.
      connect.reset()
    }
  }

  return (
    <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
      <Field label={t.remote.url} help={t.remote.urlHelp} value={url} onChange={(e) => pickByAddress(e.target.value)} placeholder={t.remote.urlPlaceholder} error={fieldError.url} autoComplete="off" spellCheck={false} inputMode="url" />
      <Field label={t.remote.branch} help={t.remote.branchHelp} value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" autoComplete="off" spellCheck={false} />

      <fieldset className="grid gap-2">
        <legend className="mb-1 text-sm font-medium">{t.remote.credential}</legend>
        <div role="radiogroup" aria-label={t.remote.credential} className="flex flex-wrap gap-2">
          {(['ssh', 'token'] as const).map((k) => (
            <label key={k} className={`flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-1.5 text-sm has-[:focus-visible]:ring-3 has-[:focus-visible]:ring-ring/50 ${kind === k ? 'border-brand bg-brand/5' : ''}`}>
              <input
                type="radio"
                name={`credential-${space}`}
                className="accent-[var(--brand)]"
                checked={kind === k}
                onChange={() => {
                  setKind(k)
                  setChose(true)
                }}
              />
              {k === 'ssh' ? t.remote.ssh : t.remote.token}
            </label>
          ))}
        </div>
      </fieldset>

      {kind === 'ssh' ? (
        <div className="grid gap-2">
          <p className="text-xs text-muted-foreground">{t.remote.sshHelp}</p>
          {key ? (
            <>
              <PublicKey publicKey={key.publicKey} fingerprint={key.fingerprint} title={t.remote.ssh} />
              {/* A new key replaces the old one, which only a space not yet connected allows. */}
              {!existing && (
                <div>
                  <Button type="button" variant="ghost" size="sm" onClick={() => setRotateOpen(true)} disabled={keyBusy}>
                    {t.remote.rotateKey}
                  </Button>
                </div>
              )}
            </>
          ) : (
            <div>
              <Button type="button" variant="outline" onClick={() => void makeKey(false)} disabled={keyBusy}>
                {keyBusy ? <Spinner /> : <KeyRound />}
                {keyBusy ? t.remote.makingKey : t.remote.makeKey}
              </Button>
            </div>
          )}
          {fieldError.key && <p className="text-xs text-destructive">{fieldError.key}</p>}
          {keyError && <p role="alert" className="text-xs text-destructive">{keyError}</p>}
        </div>
      ) : (
        <div className="grid gap-4">
          <Field label={t.remote.username} help={t.remote.usernameHelp} value={username} onChange={(e) => setUsername(e.target.value)} placeholder="x-access-token" autoComplete="off" spellCheck={false} />
          <Field
            label={t.remote.tokenField}
            help={hadToken ? t.remote.tokenKeep : t.remote.tokenHelp}
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            // A saved token is never shown, so the box starts empty: leaving it so keeps it.
            placeholder={hadToken ? '••••••••' : ''}
            error={fieldError.token}
            autoComplete="new-password"
          />
        </div>
      )}

      <Problem error={error} />
      <div className="flex items-center gap-3">
        <Button type="submit" disabled={connect.isPending}>
          {connect.isPending && <Spinner />}
          {connect.isPending ? t.remote.connecting : existing ? t.remote.saveChanges : t.remote.connect}
        </Button>
      </div>

      <AlertDialog open={rotateOpen} onOpenChange={setRotateOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t.remote.rotateTitle}</AlertDialogTitle>
            <AlertDialogDescription>{t.remote.rotateDescription}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.remote.disconnectCancel}</AlertDialogCancel>
            <AlertDialogAction disabled={keyBusy} onClick={() => void makeKey(true)}>
              {t.remote.rotateConfirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </form>
  )
}
