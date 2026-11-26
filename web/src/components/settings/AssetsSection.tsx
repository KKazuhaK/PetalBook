import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { CircleAlert, CirclePause, Cloud, CloudOff, Link2, RefreshCw, Unlink } from 'lucide-react'
import { useAssetsRemote, useConnectAssets, useDisconnectAssets, useMoveAssets, useRemote } from '@/api/hooks'
import type { AssetMove, AssetsRemote, RemoteState } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ErrorState, PageFallback, Spinner } from '@/components/common'
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
import { formatBytes } from '@/lib/bytes'
import { t } from '@/i18n'
import { ts } from '@/i18n/storage'
import { SettingsSection } from './SettingsParts'

const STATE_ICON: Record<RemoteState, typeof Cloud> = {
  ok: Cloud,
  offline: CloudOff,
  rewritten: CirclePause,
  error: CircleAlert,
}

// The attachment repository of a space (docs/api.md, "Server storage"): a second
// repository for the files of assets/, with the move of the attachments that are
// in the space already. Shown for a space that follows a remote.
export function AssetsSection({ space }: { space: string }) {
  const remote = useRemote(space)
  const assets = useAssetsRemote(space, remote.data !== null && remote.data !== undefined)
  const a = ts.assets
  if (!remote.data) return null
  return (
    <SettingsSection title={a.title} id="assets-title">
      {assets.isPending ? (
        <PageFallback />
      ) : assets.isError ? (
        <ErrorState
          message={t.fmt.labelled(a.connectFailed, assets.error.message)}
          onRetry={() => void assets.refetch()}
        />
      ) : assets.data === null ? (
        <>
          <p className="text-sm text-muted-foreground">{a.intro}</p>
          <ConnectAssets space={space} />
        </>
      ) : (
        <Connected space={space} v={assets.data} />
      )}
    </SettingsSection>
  )
}

function ConnectAssets({ space }: { space: string }) {
  const connect = useConnectAssets(space)
  const a = ts.assets
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (connect.isPending) return
    if (url.trim() === '') return setError(t.remote.urlRequired)
    setError('')
    try {
      await connect.mutateAsync({ url: url.trim(), credential: { type: 'token', ...(token ? { token } : {}) } })
      setToken('')
      toast.success(a.connected)
    } catch (err) {
      setError(t.fmt.labelled(a.connectFailed, err instanceof Error ? err.message : ''))
    }
  }
  return (
    <form className="grid max-w-xl gap-3" onSubmit={(e) => void submit(e)} noValidate>
      <Field
        label={a.address}
        value={url}
        onChange={(e) => setUrl(e.target.value)}
        placeholder="https://github.com/owner/notes-assets.git"
        autoComplete="off"
        spellCheck={false}
      />
      <Field
        label={a.token}
        type="password"
        value={token}
        onChange={(e) => setToken(e.target.value)}
        autoComplete="off"
      />
      <FormMessage>{error}</FormMessage>
      <div>
        <Button type="submit" disabled={connect.isPending}>
          {connect.isPending ? <Spinner /> : <Link2 aria-hidden="true" />}
          {connect.isPending ? a.connecting : a.connect}
        </Button>
      </div>
    </form>
  )
}

function Connected({ space, v }: { space: string; v: AssetsRemote }) {
  const a = ts.assets
  const disconnect = useDisconnectAssets(space)
  const [confirm, setConfirm] = useState(false)
  const Icon = STATE_ICON[v.state]
  const moving = v.move?.state === 'copying' || v.move?.state === 'switching'
  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync()
      toast.success(a.disconnected)
    } catch (e) {
      toast.error(t.fmt.labelled(a.disconnectFailed, e instanceof Error ? e.message : ''))
    }
    setConfirm(false)
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={v.state === 'ok' ? 'secondary' : 'outline'} className="gap-1">
          <Icon className="size-3.5" aria-hidden />
          {a.state[v.state]}
        </Badge>
        <span className="min-w-0 break-all font-mono text-xs">{v.url}</span>
      </div>
      {v.state !== 'ok' && <p className="text-sm text-muted-foreground">{v.error || a.offlineBody}</p>}
      {v.state === 'offline' && <p className="text-xs text-muted-foreground">{a.offlineBody}</p>}

      <div className="grid gap-2 rounded-lg border p-3">
        <h3 className="text-sm font-medium">{a.move}</h3>
        <p className="text-xs text-muted-foreground">{v.move?.state === 'done' ? a.states.done : a.moveHelp}</p>
        {!v.move && <p className="text-xs text-muted-foreground">{a.keepsOld}</p>}
        {v.move && v.move.state !== 'done' && <Progress move={v.move} />}
        {v.move?.state === 'failed' && <FormMessage>{v.move.error}</FormMessage>}
        {v.move?.state !== 'done' && <MoveButton space={space} running={moving} failed={v.move?.state === 'failed'} />}
      </div>

      {!v.move && (
        <Button variant="outline" onClick={() => setConfirm(true)}>
          <Unlink aria-hidden="true" />
          {a.disconnect}
        </Button>
      )}
      <AlertDialog open={confirm} onOpenChange={setConfirm}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{a.disconnectTitle}</AlertDialogTitle>
            <AlertDialogDescription>{a.disconnectHelp}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t.remote.disconnectCancel}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={disconnect.isPending}
              onClick={() => void doDisconnect()}
            >
              {a.disconnect}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Progress({ move }: { move: AssetMove }) {
  const a = ts.assets
  const pct =
    move.bytes > 0
      ? Math.min(100, Math.round((move.bytesDone / move.bytes) * 100))
      : move.files > 0
        ? Math.round((move.filesDone / move.files) * 100)
        : 0
  return (
    <div className="grid gap-1" data-slot="assets-progress">
      <div className="text-sm">{a.states[move.state]}</div>
      <progress className="h-2 w-full accent-[var(--brand)]" max={100} value={pct} aria-label={a.states[move.state]} />
      <div className="text-xs text-muted-foreground">
        {a.progress(move.filesDone, move.files)} · {formatBytes(move.bytesDone)} / {formatBytes(move.bytes)}
      </div>
    </div>
  )
}

function MoveButton({ space, running, failed }: { space: string; running: boolean; failed: boolean }) {
  const move = useMoveAssets(space)
  const a = ts.assets
  const start = async () => {
    try {
      const r = await move.mutateAsync()
      if (!r.move) toast(a.moveNothing)
      else toast(a.moveStarted)
    } catch (e) {
      toast.error(t.fmt.labelled(a.moveFailed, e instanceof Error ? e.message : ''))
    }
  }
  return (
    <div>
      <Button variant="outline" disabled={running || move.isPending} onClick={() => void start()}>
        <RefreshCw aria-hidden="true" />
        {(running || move.isPending) && <Spinner />}
        {failed ? a.retry : a.moveStart}
      </Button>
    </div>
  )
}
