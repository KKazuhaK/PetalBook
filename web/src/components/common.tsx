import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n/zh-CN'

export function PageFallback() {
  return (
    <div className="mx-auto w-full max-w-[720px] space-y-4 px-4 py-10" aria-busy="true">
      <Skeleton className="h-10 w-2/3" />
      <Skeleton className="h-5 w-1/3" />
      <Skeleton className="mt-8 h-4 w-full" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-5/6" />
    </div>
  )
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={`size-4 animate-spin ${className ?? ''}`} />
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 py-16 text-center">
      <p className="text-sm text-muted-foreground">{message}</p>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          {t.list.retry}
        </Button>
      )}
    </div>
  )
}
