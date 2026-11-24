// The security page's form: what is on screen (numbers as text, so a field can be half typed), the
// patch it makes against what the server has (only the blocks and fields that changed), and where
// each refusal of the server belongs.

import type { CaptchaContext, CaptchaProvider, CaptchaWhen, LockoutScope, LoginMode, Role, SecurityPatch, SecuritySettings } from '@/api/types'

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

export const rolesToChoice = (roles: Role[]): RoleChoice => (roles.includes('member') ? 'everyone' : roles.includes('admin') ? 'admins' : 'none')
export const choiceToRoles = (c: RoleChoice): Role[] => (c === 'everyone' ? ['admin', 'member'] : c === 'admins' ? ['admin'] : [])

export function draftOf(s: SecuritySettings): Draft {
  return {
    mode: s.login.mode,
    ssoOnly: s.login.ssoOnly,
    pkEnabled: s.passkeys.enabled,
    pkRoles: rolesToChoice(s.passkeys.requireRoles),
    provider: s.captcha.provider,
    siteKey: s.captcha.siteKey,
    secret: null,
    contexts: Object.fromEntries(CONTEXTS.map((c) => [c, { on: s.captcha.contexts[c].on, when: s.captcha.contexts[c].when, failures: String(s.captcha.contexts[c].failures) }])) as Draft['contexts'],
    lockEnabled: s.lockout.enabled,
    lockMinutes: String(s.lockout.minutes),
    scope: s.lockout.scope,
    limits: Object.fromEntries(LIMIT_FIELDS.map((f) => [f, String(s.limits[f])])) as Draft['limits'],
  }
}

// A whole number from what was typed, or NaN.
const num = (v: string) => (/^\s*\d+\s*$/.test(v) ? Number(v) : NaN)

// What to send: only what differs from what the server has. A number that is not a number is sent as it is
// (NaN becomes null in JSON), so the server's own refusal tells the person which field.
export function buildPatch(server: SecuritySettings, d: Draft): SecurityPatch {
  const patch: SecurityPatch = {}
  const login: NonNullable<SecurityPatch['login']> = {}
  if (d.mode !== server.login.mode) login.mode = d.mode
  if (d.ssoOnly !== server.login.ssoOnly) login.ssoOnly = d.ssoOnly
  if (Object.keys(login).length) patch.login = login

  const passkeys: NonNullable<SecurityPatch['passkeys']> = {}
  if (d.pkEnabled !== server.passkeys.enabled) passkeys.enabled = d.pkEnabled
  if (d.pkRoles !== rolesToChoice(server.passkeys.requireRoles)) passkeys.requireRoles = choiceToRoles(d.pkRoles)
  if (Object.keys(passkeys).length) patch.passkeys = passkeys

  const captcha: NonNullable<SecurityPatch['captcha']> = {}
  if (d.provider !== server.captcha.provider) captcha.provider = d.provider
  if (d.siteKey !== server.captcha.siteKey) captcha.siteKey = d.siteKey
  if (d.secret !== null) captcha.secret = d.secret
  const contexts: NonNullable<NonNullable<SecurityPatch['captcha']>['contexts']> = {}
  for (const c of CONTEXTS) {
    const was = server.captcha.contexts[c]
    const now = d.contexts[c]
    const one: { on?: boolean; when?: CaptchaWhen; failures?: number } = {}
    if (now.on !== was.on) one.on = now.on
    if (now.when !== was.when) one.when = now.when
    if (num(now.failures) !== was.failures) one.failures = num(now.failures)
    if (Object.keys(one).length) contexts[c] = one
  }
  if (Object.keys(contexts).length) captcha.contexts = contexts
  if (Object.keys(captcha).length) patch.captcha = captcha

  const lockout: NonNullable<SecurityPatch['lockout']> = {}
  if (d.lockEnabled !== server.lockout.enabled) lockout.enabled = d.lockEnabled
  if (num(d.lockMinutes) !== server.lockout.minutes) lockout.minutes = num(d.lockMinutes)
  if (d.scope !== server.lockout.scope) lockout.scope = d.scope
  if (Object.keys(lockout).length) patch.lockout = lockout

  const limits: NonNullable<SecurityPatch['limits']> = {}
  for (const f of LIMIT_FIELDS) if (num(d.limits[f]) !== server.limits[f]) limits[f] = num(d.limits[f])
  if (Object.keys(limits).length) patch.limits = limits
  return patch
}

export const isEmptyPatch = (p: SecurityPatch) => Object.keys(p).length === 0

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
// `invalid` names the field in its message; the field's own block says where it belongs.
export function partOf(code: string, message: string): Part {
  if (PARTS[code]) return PARTS[code]
  const m = /\b(login|passkeys|captcha|lockout|limits)\b/.exec(message)
  if (code === 'invalid' && m) return m[1] as Part
  return 'page'
}
