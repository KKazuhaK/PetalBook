import { AlertTriangle, Check, CircleDashed, CircleDot, CloudOff, Loader2, LogIn, RotateCw } from 'lucide-react'
import { ApiError } from '@/api/client'
import type { SaveSnapshot } from '@/editor/saveController'
import { t } from '@/i18n/zh-CN'

const quiet = 'inline-flex items-center gap-1 text-xs text-muted-foreground'

// Where the person's text is: typed and only in this browser (unsaved), on the
// server as a draft, written to the page as a version, or not getting through
// (offline or error, with a retry), or in conflict. Each state has its own
// icon and words, so it does not depend on colour.
export function SaveIndicator({ snapshot, onRetry, onOpenConflict }: { snapshot: SaveSnapshot; onRetry: () => void; onOpenConflict: () => void }) {
  const s = t.editor.save
  switch (snapshot.state) {
    case 'saved':
      return (
        <span role="status" className={quiet}>
          <Check className="size-3.5" />
          {s.saved}
        </span>
      )
    case 'drafted':
      return (
        <span role="status" className={quiet} title={s.draftedHint}>
          <CircleDashed className="size-3.5" />
          {s.drafted}
        </span>
      )
    case 'storing':
      return (
        <span role="status" className={quiet}>
          <Loader2 className="size-3.5 animate-spin" />
          {s.storing}
        </span>
      )
    case 'committing':
      return (
        <span role="status" className={quiet}>
          <Loader2 className="size-3.5 animate-spin" />
          {s.committing}
        </span>
      )
    case 'dirty':
      return (
        <span role="status" className={quiet}>
          <CircleDot className="size-3.5" />
          {s.dirty}
        </span>
      )
    case 'error': {
      const e = snapshot.error
      const signedOut = e instanceof ApiError && e.status === 401
      const offline = e instanceof ApiError && e.code === 'network'
      const Icon = signedOut ? LogIn : offline ? CloudOff : RotateCw
      return (
        <button
          type="button"
          onClick={onRetry}
          title={e?.message}
          role="alert"
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-destructive hover:bg-destructive/10"
        >
          <Icon className="size-3.5" />
          {signedOut ? s.signedOut : offline ? s.offline : s.error}（{s.retry}）
        </button>
      )
    }
    case 'conflict':
      return (
        <button type="button" onClick={onOpenConflict} className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-warning hover:bg-warning/10">
          <AlertTriangle className="size-3.5" />
          {s.conflict}
        </button>
      )
  }
}
