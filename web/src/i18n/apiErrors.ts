// The words for the error codes the server sends, in the language of the page. The list of codes is
// errorCodes.json, which a Go test keeps equal to the codes in the server's source; apiErrors.test.ts fails when a
// code on it has no text in either language.

import { apiErrorsEn } from './apiErrors/en.ts'
import { apiErrorsZhCN } from './apiErrors/zh-CN.ts'
import type { ApiErrorTexts, ErrorParams } from './apiErrorsTypes.ts'
import { lang } from './index.ts'

export type { ApiErrorTexts, ErrorParams }
const texts: ApiErrorTexts = lang === 'en' ? apiErrorsEn : apiErrorsZhCN

// Codes whose server sentence says what exactly is wrong with a request ("limit is a number from 1 to 200"): the
// client cannot list those. The English page shows the sentence; the Chinese page shows the text of the code and
// the sentence after it, so the detail is not lost.
export const DETAIL_CODES: ReadonlySet<string> = new Set([
  'invalid',
  'conflict',
  'forbidden',
  'provider_refused',
  'provider_error',
])

// What to tell a person about an error of this code, or undefined when the client has no text for it (the caller then
// shows what the server said).
export function apiErrorText(code: string, params: ErrorParams, serverMessage?: string): string | undefined {
  const text = Object.hasOwn(texts, code) ? texts[code] : undefined
  if (text === undefined) return undefined
  const own = typeof text === 'function' ? text(params) : text
  if (!serverMessage || !DETAIL_CODES.has(code)) return own
  return lang === 'en' ? serverMessage : `${own}（${serverMessage}）`
}
