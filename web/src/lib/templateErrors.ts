// What a person is told about the refusals of a template's request that have a code of their own. Imports the words of
// templates, so only the code that is loaded with them uses it.

import { ApiError } from '@/api/client'
import { tt } from '@/i18n/templates'

// The words of the refusal when it has a code of its own, else `fallback`.
export function templateMessage(e: unknown, fallback: string): string {
  if (e instanceof ApiError) {
    const code = e.code as string
    if (code === 'template_limit' || code === 'template_too_large' || code === 'template_not_found')
      return tt.errors[code]
  }
  return fallback
}
