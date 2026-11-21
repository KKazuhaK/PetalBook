import { AlertTriangle, Check, CircleDot, Loader2, RotateCw } from 'lucide-react'
import type { SaveSnapshot } from '@/editor/saveController'
import { t } from '@/i18n/zh-CN'

export function SaveIndicator({ snapshot, onRetry, onOpenConflict }: { snapshot: SaveSnapshot; onRetry: () => void; onOpenConflict: () => void }) {
  const s = t.editor.save
  switch (snapshot.state) {
    case 'saved':
      return (
        <span role="status" className="inline-flex items-center gap-1 text-xs text-muted-foreground">
          <Check className="size-3.5" />
          {s.saved}
        </span>
      )
    case 'saving':
      return (
        <span role="status" className="inline-flex items-center gap-1 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          {s.saving}
        </span>
      )
    case 'dirty':
      return (
        <span role="status" className="inline-flex items-center gap-1 text-xs text-muted-foreground">
          <CircleDot className="size-3.5" />
          {s.dirty}
        </span>
      )
    case 'error':
      return (
        <button
          type="button"
          onClick={onRetry}
          title={snapshot.error?.message}
          className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-destructive hover:bg-destructive/10"
        >
          <RotateCw className="size-3.5" />
          {s.error}（{s.retry}）
        </button>
      )
    case 'conflict':
      return (
        <button type="button" onClick={onOpenConflict} className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-warning hover:bg-warning/10">
          <AlertTriangle className="size-3.5" />
          {s.conflict}
        </button>
      )
  }
}
