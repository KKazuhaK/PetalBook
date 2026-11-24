import { isUnavailable } from '@/api/client'
import { fileTooLargeText } from '@/lib/fileTooLarge'
import { t } from '@/i18n'

// What to tell a person about a write that failed. A remote that cannot be
// reached (503) gets the explanation that nothing was lost and what to do;
// anything else shows the server's own words.
export function failureText(e: unknown): string {
  const big = fileTooLargeText(e)
  if (big) return big
  if (isUnavailable(e))
    return e.message ? t.fmt.note(t.remote.unavailable.write, e.message) : t.remote.unavailable.write
  return e instanceof Error ? e.message : ''
}
