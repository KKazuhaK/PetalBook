import { ApiError } from '@/api/client'
import { ts } from '@/i18n/storage'

// The refusals of server storage (docs/api.md, "Server storage") in the page's
// language, for the places that show a failure of a read or a write: an old
// version that is not on the server, a space being restored, a space that is
// read only, an upload over the limit. Undefined for any other failure, which
// keeps the server's own message.
export function storageErrorMessage(e: unknown): string | undefined {
  if (!(e instanceof ApiError)) return undefined
  const body = (e.body ?? {}) as { fetching?: boolean; phase?: string; deadline?: string }
  const text = ts.errors
  switch (e.code as string) {
    case 'not_cached':
      return body.fetching ? text.fetching : text.notCached
    case 'restoring': {
      // Waiting for its turn (a restore or a rebuild of the index), or being rebuilt.
      if (body.phase === 'waiting' || body.phase === 'rebuilding') return text.preparing[body.phase]
      const phases = text.phases as Record<string, string>
      return text.restoring(phases[body.phase ?? ''] ?? body.phase ?? '')
    }
    case 'remote_required':
      return text.remoteRequired(body.deadline)
    case 'space_full':
      return text.spaceFull
    default:
      return undefined
  }
}

// The text of an error, the storage kind in the page's language and any other as the server wrote it.
export const errorText = (e: unknown, fallback = ''): string =>
  storageErrorMessage(e) ?? (e instanceof Error ? e.message : fallback)
