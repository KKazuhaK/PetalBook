import { ApiError } from '@/api/client'
import { formatBytes } from '@/lib/bytes'
import { tg } from '@/i18n/manage'

// A write refused for the size of one file (413 file_too_large, docs/api.md "The size of one file"): what it says, with
// the limit, and that what the person wrote is still there. Undefined for any other failure.
export function fileTooLargeText(e: unknown): string | undefined {
  if (!(e instanceof ApiError) || e.code !== 'file_too_large') return undefined
  const b = (e.body ?? {}) as { limit?: number; size?: number; name?: string }
  const limit = typeof b.limit === 'number' ? formatBytes(b.limit) : ''
  if (!b.name) return tg.size.tooLargeNoName(limit)
  return tg.size.tooLarge(b.name, limit, typeof b.size === 'number' ? formatBytes(b.size) : '')
}

// A short form for the place that shows the state of saving.
export function fileTooLargeShort(e: unknown): string | undefined {
  if (!(e instanceof ApiError) || e.code !== 'file_too_large') return undefined
  const limit = (e.body as { limit?: number } | undefined)?.limit
  return tg.size.saveShort(typeof limit === 'number' ? formatBytes(limit) : '')
}
