import { isUnavailable } from '@/api/client'
import { t } from '@/i18n/zh-CN'

// What to tell a person about a write that failed. A remote that cannot be
// reached (503) gets the explanation that nothing was lost and what to do;
// anything else shows the server's own words.
export function failureText(e: unknown): string {
  if (isUnavailable(e)) return `${t.remote.unavailable.write}${e.message ? `（${e.message}）` : ''}`
  return e instanceof Error ? e.message : ''
}
