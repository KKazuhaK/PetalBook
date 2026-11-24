import { AlertTriangle, Check, CircleDashed, CircleDot, CloudOff, Loader2, LogIn, RotateCw } from 'lucide-react'
import { ApiError } from '@/api/client'
import type { SaveSnapshot } from '@/editor/saveController'
import { t } from '@/i18n'
import { fileTooLargeShort, fileTooLargeText } from '@/lib/fileTooLarge'

const quiet = 'inline-flex items-center gap-1 text-xs text-muted-foreground'

// Where the person's text is: typed and only in this browser (unsaved), on the
// server as a draft, written to the page as a version, or not getting through
// (offline or error, with a retry), or in conflict. Each state has its own
// icon and words, so it does not depend on colour.
// `draftSafe`: the server holds everything that was typed (nothing is only in this browser).
export function SaveIndicator({
  snapshot,
  draftSafe,
  onRetry,
  onOpenConflict,
}: {
  snapshot: SaveSnapshot
  draftSafe: boolean
  onRetry: () => void
  onOpenConflict: () => void
}) {
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
      // The space follows a remote that cannot be reached: the commit was refused,
      // the draft is untouched. Said as what it is, not as a failed save.
      if (e instanceof ApiError && e.code === 'unavailable') {
        const u = t.remote.unavailable
        return (
          <button
            type="button"
            onClick={onRetry}
            title={
              e.message
                ? t.fmt.note(draftSafe ? u.commitDraftHint : u.commitLocalHint, e.message)
                : draftSafe
                  ? u.commitDraftHint
                  : u.commitLocalHint
            }
            role="alert"
            className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-warning hover:bg-warning/10"
          >
            <CloudOff className="size-3.5" />
            {t.fmt.note(draftSafe ? u.commitDraft : u.commitLocal, s.retry)}
          </button>
        )
      }
      // A file over the limit: the text is not lost, it stays here; saying so is the message.
      const big = fileTooLargeShort(e)
      if (big) {
        return (
          <button
            type="button"
            onClick={onRetry}
            title={fileTooLargeText(e)}
            role="alert"
            data-slot="file-too-large"
            // On a phone the bar is narrow: the words are cut short there rather than pushing the page sideways, and
            // the button is tall enough for a finger.
            className="inline-flex max-w-[45vw] min-w-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-destructive hover:bg-destructive/10 max-md:min-h-10 pointer-coarse:min-h-10 sm:max-w-none"
          >
            <AlertTriangle className="size-3.5 shrink-0" />
            <span className="truncate">{t.fmt.note(big, s.retry)}</span>
          </button>
        )
      }
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
          {t.fmt.note(signedOut ? s.signedOut : offline ? s.offline : s.error, s.retry)}
        </button>
      )
    }
    case 'conflict':
      return (
        <button
          type="button"
          onClick={onOpenConflict}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-warning hover:bg-warning/10"
        >
          <AlertTriangle className="size-3.5" />
          {s.conflict}
        </button>
      )
  }
}
