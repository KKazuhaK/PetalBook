// The security page's form: what is on screen (numbers as text, so a field can be half typed), the
// patch it makes against what the server has (only the blocks and fields that changed), and where
// each refusal of the server belongs.

import type { SecurityStep } from '@/api/admin'
import { K } from '@/api/settingsFold'
import type {
  CaptchaContext,
  CaptchaContextSettings,
  CaptchaCredentials,
  CaptchaProvider,
  CaptchaWhen,
  LockoutScope,
  LoginMode,
  Role,
  SecuritySettings,
} from '@/api/types'

export const CONTEXTS: readonly CaptchaContext[] = ['login', 'invite', 'setup', 'forgot', 'register']
export const LIMIT_FIELDS = ['perMinute', 'maxFailures', 'windowMinutes', 'sessionIdleDays', 'sessionMaxDays'] as const
export type RoleChoice = 'none' | 'admins' | 'everyone'

export interface Draft {
  mode: LoginMode
  ssoOnly: boolean
  pkEnabled: boolean
  pkRoles: RoleChoice
  provider: CaptchaProvider
  siteKey: string
  // null: not touched. "": clear the stored one. Anything else: the new secret.
  secret: string | null
  contexts: Record<CaptchaContext, { on: boolean; when: CaptchaWhen; failures: string }>
  lockEnabled: boolean
  lockMinutes: string
  scope: LockoutScope
  limits: Record<(typeof LIMIT_FIELDS)[number], string>
}

export const rolesToChoice = (roles: Role[]): RoleChoice =>
  roles.includes('member') ? 'everyone' : roles.includes('admin') ? 'admins' : 'none'
export const choiceToRoles = (c: RoleChoice): Role[] =>
  c === 'everyone' ? ['admin', 'member'] : c === 'admins' ? ['admin'] : []

export function draftOf(s: SecuritySettings): Draft {
  return {
    mode: s.login.mode,
    ssoOnly: s.login.ssoOnly,
    pkEnabled: s.passkeys.enabled,
    pkRoles: rolesToChoice(s.passkeys.requireRoles),
    provider: s.captcha.provider,
    siteKey: s.captcha.siteKey,
    secret: null,
    contexts: Object.fromEntries(
      CONTEXTS.map((c) => [
        c,
        {
          on: s.captcha.contexts[c].on,
          when: s.captcha.contexts[c].when,
          failures: String(s.captcha.contexts[c].failures),
        },
      ]),
    ) as Draft['contexts'],
    lockEnabled: s.lockout.enabled,
    lockMinutes: String(s.lockout.minutes),
    scope: s.lockout.scope,
    limits: Object.fromEntries(LIMIT_FIELDS.map((f) => [f, String(s.limits[f])])) as Draft['limits'],
  }
}

// A whole number from what was typed, or NaN.
const num = (v: string) => (/^\s*\d+\s*$/.test(v) ? Number(v) : NaN)

// What to write: one step per setting that differs from what the server has, in an order the server accepts (a switch
// that makes a check harder after the one that makes it easier), and the captcha's credentials, which are not a
// setting. A number that is not a number is sent as it is (NaN becomes null in JSON), so the server's own refusal tells
// the person which field. The steps are written one after the other; the first refusal stops the rest.
export function buildSteps(server: SecuritySettings, d: Draft): SecurityStep[] {
  const steps: SecurityStep[] = []
  const put = (key: string, value: unknown) => steps.push({ key, value })

  // Turning single sign-on only off comes before a mode change, turning it on after it.
  if (!d.ssoOnly && d.ssoOnly !== server.login.ssoOnly) put(K.ssoOnly, false)
  if (d.mode !== server.login.mode) put(K.mode, d.mode)
  if (d.ssoOnly && d.ssoOnly !== server.login.ssoOnly) put(K.ssoOnly, true)

  // Lifting the requirement of passkeys comes before switching them off; switching on, before requiring.
  const rolesChanged = d.pkRoles !== rolesToChoice(server.passkeys.requireRoles)
  if (!d.pkEnabled && rolesChanged) put(K.passkeyRoles, choiceToRoles(d.pkRoles))
  if (d.pkEnabled !== server.passkeys.enabled) put(K.passkeys, d.pkEnabled)
  if (d.pkEnabled && rolesChanged) put(K.passkeyRoles, choiceToRoles(d.pkRoles))

  // The captcha: a context turned off goes first (and every one when another provider takes over, since its keys
  // start empty), then the provider, then its keys, and last the contexts that stay or come on.
  const providerChanged = d.provider !== server.captcha.provider
  const was = server.captcha.contexts
  const forcedOff = providerChanged && d.provider !== 'image'
  const off = CONTEXTS.filter((c) => was[c].on && (!d.contexts[c].on || forcedOff))
  const after = Object.fromEntries(
    CONTEXTS.map((c) => [c, off.includes(c) ? { ...was[c], on: false } : was[c]]),
  ) as Record<CaptchaContext, CaptchaContextSettings>
  if (off.length) put(K.captchaContexts, Object.fromEntries(off.map((c) => [c, { on: false }])))
  if (providerChanged) put(K.captchaProvider, d.provider)
  if (d.provider !== 'image') {
    const credentials: CaptchaCredentials = {}
    if (d.siteKey !== (providerChanged ? '' : server.captcha.siteKey)) credentials.siteKey = d.siteKey
    if (d.secret !== null) credentials.secret = d.secret
    if (Object.keys(credentials).length) steps.push({ credentials })
  }
  const doc: Record<string, Partial<CaptchaContextSettings>> = {}
  for (const c of CONTEXTS) {
    const now = d.contexts[c]
    const one: Partial<CaptchaContextSettings> = {}
    if (now.on !== after[c].on) one.on = now.on
    if (now.when !== after[c].when) one.when = now.when
    if (num(now.failures) !== after[c].failures) one.failures = num(now.failures)
    if (Object.keys(one).length) doc[c] = one
  }
  if (Object.keys(doc).length) put(K.captchaContexts, doc)

  if (d.lockEnabled !== server.lockout.enabled) put(K.lockoutEnabled, d.lockEnabled)
  if (num(d.lockMinutes) !== server.lockout.minutes) put(K.lockoutMinutes, num(d.lockMinutes))
  if (d.scope !== server.lockout.scope) put(K.lockoutScope, d.scope)

  const limits: [string, number, number][] = [
    [K.perMinute, num(d.limits.perMinute), server.limits.perMinute],
    [K.maxFailures, num(d.limits.maxFailures), server.limits.maxFailures],
    [K.windowMinutes, num(d.limits.windowMinutes), server.limits.windowMinutes],
  ]
  for (const [key, now, was] of limits) if (now !== was) put(key, now)
  // The session lifetimes are checked against each other: the longer one is raised first, and lowered last.
  const idle = num(d.limits.sessionIdleDays)
  const max = num(d.limits.sessionMaxDays)
  const sessions: [string, number, number][] =
    max >= server.limits.sessionMaxDays
      ? [
          [K.maxDays, max, server.limits.sessionMaxDays],
          [K.idleDays, idle, server.limits.sessionIdleDays],
        ]
      : [
          [K.idleDays, idle, server.limits.sessionIdleDays],
          [K.maxDays, max, server.limits.sessionMaxDays],
        ]
  for (const [key, now, was] of sessions) if (now !== was) put(key, now)
  return steps
}

// The part of the page a refusal is about, so it is shown where the person is looking.
export type Part = 'login' | 'passkeys' | 'captcha' | 'lockout' | 'limits' | 'page'
const PARTS: Record<string, Part> = {
  local_admin_required: 'login',
  sso_only_with_registration: 'login',
  captcha_site_key_required: 'captcha',
  captcha_secret_required: 'captcha',
  captcha_needs_public_url: 'captcha',
  passkey_unavailable: 'passkeys',
  passkey_requirement_unsatisfiable: 'passkeys',
}
// The block a setting is shown in.
const partOfKey = (key: string): Part | undefined => {
  if (key.startsWith('captcha.')) return 'captcha'
  if (key.startsWith('signin.lockout.')) return 'lockout'
  if (key === 'signin.perMinute' || key === 'signin.maxFailures' || key === 'signin.windowMinutes') return 'limits'
  if (key.startsWith('sessions.')) return 'limits'
  if (key === 'signin.mode' || key === 'signin.ssoOnly') return 'login'
  if (key === 'features.passkeys' || key === 'signin.passkeyRoles') return 'passkeys'
  return undefined
}
// A refusal belongs to the code's block when the code says (a captcha needs its key), else to the block of the setting
// that was being written.
export function partOf(code: string, key?: string): Part {
  if (PARTS[code]) return PARTS[code]
  return (key && partOfKey(key)) || 'page'
}
