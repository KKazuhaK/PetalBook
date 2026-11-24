import { Star } from 'lucide-react'
import { toast } from 'sonner'
import { useAddFavorite, useCanFavorite, useFavorite, useRemoveFavorite } from '@/api/hooks'
import type { FavoriteRef } from '@/api/types'
import { ApiError } from '@/api/client'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'

// Adds a page, a folder or a view to the favourites and takes it off again.
export function useToggleFavorite(ref: FavoriteRef | null) {
  const current = useFavorite(ref)
  const add = useAddFavorite()
  const remove = useRemoveFavorite()
  const on = current !== undefined
  const toggle = () => {
    if (!ref) return
    const fail = (e: unknown) =>
      void toast.error(e instanceof ApiError && e.code === 'invalid' ? e.message : t.fav.failed)
    if (on) remove.mutate(ref, { onSuccess: () => void toast.success(t.fav.removed), onError: fail })
    else add.mutate(ref, { onSuccess: () => void toast.success(t.fav.added), onError: fail })
  }
  return { on, toggle, busy: add.isPending || remove.isPending }
}

// The star in the top bar of a page, and by a saved view: pressed when it is a favourite.
export function StarButton({ target, labels }: { target: FavoriteRef; labels: { add: string; remove: string } }) {
  const allowed = useCanFavorite()
  const { on, toggle, busy } = useToggleFavorite(allowed ? target : null)
  if (!allowed) return null
  const label = on ? labels.remove : labels.add
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      title={label}
      aria-pressed={on}
      disabled={busy}
      onClick={toggle}
      data-slot="favorite-star"
    >
      <Star className={on ? 'fill-amber-400 text-amber-500' : ''} />
    </Button>
  )
}
