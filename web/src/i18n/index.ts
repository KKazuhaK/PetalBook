// The interface language. It is decided once, when the page loads: a choice
// saved in this browser, else the browser's own languages. Switching saves the
// choice and reloads the page, so nothing has to re-render in place and `t`
// can be a plain import everywhere.

import { en } from './en.ts'
import type { Messages } from './types.ts'
import { zhCN } from './zh-CN.ts'

export type { Messages }
export type Lang = 'zh-CN' | 'en'

// Each language is named in its own language, so it can be found by someone
// who cannot read the current one.
export const languages: readonly { code: Lang; name: string }[] = [
  { code: 'zh-CN', name: '中文' },
  { code: 'en', name: 'English' },
]

export const LANG_KEY = 'pb.lang'

const dictionaries: Record<Lang, Messages> = { 'zh-CN': zhCN, en }

export const isLang = (v: unknown): v is Lang => v === 'zh-CN' || v === 'en'

// A saved choice wins when it is one we have; anything else (missing, a value
// from another version, garbage) is ignored. Without one, the browser's
// languages decide: if any of them is Chinese the page is Chinese, otherwise English.
export function decideLanguage(stored: string | null | undefined, browser: readonly string[]): Lang {
  if (isLang(stored)) return stored
  return browser.some((l) => /^zh(-|_|$)/i.test(l)) ? 'zh-CN' : 'en'
}

function readStored(): string | null {
  try {
    return localStorage.getItem(LANG_KEY)
  } catch {
    return null
  }
}

function browserLanguages(): readonly string[] {
  // Tests run in Chinese so their expectations do not depend on the machine.
  if ((import.meta as { env?: { MODE?: string } }).env?.MODE === 'test') return ['zh-CN']
  try {
    if (navigator.languages?.length) return navigator.languages
    return navigator.language ? [navigator.language] : []
  } catch {
    return []
  }
}

export const lang: Lang = decideLanguage(readStored(), browserLanguages())
export const t: Messages = dictionaries[lang]

// Outside a browser (the mock server in Node) there is no document or location.
const dom = globalThis as { document?: { documentElement: { lang: string } }; location?: { reload(): void } }

try {
  if (dom.document) dom.document.documentElement.lang = lang
} catch {
  // No document (a build step or a server): nothing to mark.
}

// Saves the choice and reloads. When storage is blocked the choice cannot be
// kept, and a reload would only bring the same language back (and cost
// whatever the page held): nothing happens, and false says so.
export function setLanguage(next: Lang): boolean {
  try {
    localStorage.setItem(LANG_KEY, next)
  } catch {
    return false
  }
  dom.location?.reload()
  return true
}
