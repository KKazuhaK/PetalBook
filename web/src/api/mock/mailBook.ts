// The mock's side of milestone 3.3, second part (docs/api.md, "Mail", "Registration", "Email verification" and
// "Password reset"): the mail settings, registration, the verification and reset links. THE MOCK NEVER SENDS: a
// message goes into `outbox`, which tests and the dev page read; nothing leaves the process.

import { HttpError, isRecord } from './store.ts'
import type { MockResponse } from './server.ts'
import type { MailSecurity, MailSettings, RegisterInfo, User } from '../types.ts'

export interface OutboxMessage {
  to: string
  kind: 'verify' | 'register' | 'register_exists' | 'reset' | 'reset_sso' | 'email_changed' | 'test'
  // The link of the message (absent for a note with no link).
  link?: string
  token?: string
  at: string
}

// What the book needs from the rest of the mock.
export interface MailUser {
  id: string
  username: string
  name: string
  email: string
  emailVerified: boolean
  role: 'admin' | 'member'
  disabled: boolean
  passwordHash?: string
  siteAdmin?: boolean
  identities: { providerSlug: string }[]
}
export interface MailBookOptions {
  now: () => number
  publicUrl: string | null
  random: (n: number) => string
  hash: (s: string) => string
  users: () => Iterable<MailUser>
  user: (id: string) => MailUser | undefined
  isSiteAdmin: (id: string) => boolean
  usernameTaken: (name: string) => boolean
  checkUsername: (raw: unknown) => string
  checkPassword: (raw: unknown) => string
  checkName: (raw: unknown) => string
  checkCaptcha: (ctx: 'forgot' | 'register', key: string, sent: unknown) => void
  noteCaptchaFailure: (ctx: 'forgot' | 'register', key: string) => void
  // Makes the account and returns the cookie of its session.
  makeAccount: (p: { username: string; name: string; email: string; passwordHash: string; terms: string }) => {
    user: User
    cookie: string
  }
  setPassword: (id: string, hash: string) => void
  endSessions: (id: string) => void
  ssoOnlyInEffect: () => boolean
  ssoOnlyStored: () => boolean
  providerName: (u: MailUser) => string
  // The effective rules of the default tenant (the mock has one world).
  rules: () => { open: boolean; domains: string[]; termsUrl: string; privacyUrl: string; perHour: number }
  record: (kind: string, o: { user?: string; detail?: Record<string, unknown> }) => void
}

interface Pending {
  username: string
  name: string
  email: string
  passwordHash: string
  terms: string
  expires: number
}
interface Token {
  kind: 'verify' | 'reset'
  userId: string
  email: string
  expires: number
  used: boolean
}

const HOUR = 3_600_000
const bad = (m: string): never => {
  throw new HttpError(400, 'invalid', m)
}
const err = (status: number, code: string, m: string): never => {
  throw new HttpError(status, code, m)
}
const LOOPBACK = /^(localhost|127(\.\d+){3}|\[?::1\]?)$/i
const ADDRESS = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/

export function createMailBook(o: MailBookOptions) {
  let mail = {
    enabled: false,
    host: '',
    port: 587,
    security: 'starttls' as MailSecurity,
    username: '',
    password: '',
    from: '',
    fromName: '',
    replyTo: '',
  }
  let status: MailSettings['status'] = { ok: false, checked: null, message: '' }
  let sent = 0
  let testAt: number[] = []
  const outbox: OutboxMessage[] = []
  const pending = new Map<string, Pending>() // by token hash
  const tokens = new Map<string, Token>()
  const caps = new Map<string, number[]>() // address -> times of the last hour
  const regTimes: number[] = []
  const stamp = () => new Date(o.now()).toISOString().replace(/\.\d+Z$/, 'Z')
  const ready = () => mail.enabled && o.publicUrl !== null

  const take = (key: string, limit: number): boolean => {
    const list = (caps.get(key) ?? []).filter((t) => o.now() - t < HOUR)
    if (list.length >= limit) {
      caps.set(key, list)
      return false
    }
    list.push(o.now())
    caps.set(key, list)
    return true
  }
  const post = (m: Omit<OutboxMessage, 'at'>) => {
    if (!ready()) return
    outbox.push({ ...m, at: stamp() })
    sent++
  }
  const link = (path: string, token: string) => `${(o.publicUrl ?? '').replace(/\/+$/, '')}${path}#${token}`
  const newToken = (kind: Token['kind'], userId: string, email: string, ttl: number): string => {
    for (const [h, t] of tokens) if (t.userId === userId && t.kind === kind) tokens.delete(h)
    const raw = o.random(43)
    tokens.set(o.hash(raw), { kind, userId, email, expires: o.now() + ttl, used: false })
    return raw
  }
  const find = (kind: Token['kind'], raw: unknown): { hash: string; t: Token } | null => {
    if (typeof raw !== 'string' || raw === '' || raw.length > 256) return null
    const h = o.hash(raw)
    const t = tokens.get(h)
    return t && t.kind === kind && !t.used && t.expires > o.now() ? { hash: h, t } : null
  }
  const open = () => ready() && o.rules().open
  const cancelClosed = () => {
    if (!open()) pending.clear()
  }
  const canUsePassword = (u: MailUser) => u.passwordHash !== undefined && !(o.ssoOnlyInEffect() && u.role !== 'admin')
  const resetLink = (u: MailUser): string => link('/reset-password', newToken('reset', u.id, u.email, HOUR))
  const badLink = (): never => err(404, 'not_found', '链接无效或已过期')

  const view = (): MailSettings => ({
    enabled: mail.enabled,
    host: mail.host,
    port: mail.port,
    security: mail.security,
    username: mail.username,
    hasPassword: mail.password !== '',
    from: mail.from,
    fromName: mail.fromName,
    replyTo: mail.replyTo,
    status,
    queue: { waiting: 0, sentLastHour: sent, failedLastHour: 0 },
  })

  const put = (body: Record<string, unknown>): MailSettings => {
    const next = { ...mail }
    const str = (k: string, max: number): string | undefined => {
      if (body[k] === undefined) return undefined
      if (typeof body[k] !== 'string' || [...(body[k] as string)].length > max) bad(`${k} 不合法`)
      return (body[k] as string).trim()
    }
    for (const k of Object.keys(body))
      if (!['enabled', 'host', 'port', 'security', 'username', 'password', 'from', 'fromName', 'replyTo'].includes(k))
        bad(`不认识 ${k}`)
    if (body.enabled !== undefined) {
      if (typeof body.enabled !== 'boolean') bad('enabled 要是布尔值')
      next.enabled = body.enabled as boolean
    }
    const host = str('host', 253)
    if (host !== undefined) {
      if (/[\s/@:?#]/.test(host.replace(/^\[|\]$/g, '')) && !host.includes(':')) bad('host 不合法')
      next.host = host
    }
    if (body.port !== undefined) {
      if (!Number.isInteger(body.port) || (body.port as number) < 1 || (body.port as number) > 65535)
        bad('port 是 1 到 65535')
      next.port = body.port as number
    }
    if (body.security !== undefined) {
      if (!['starttls', 'tls', 'none'].includes(body.security as string)) bad('security 是 starttls、tls 或 none')
      next.security = body.security as MailSecurity
    }
    next.username = str('username', 255) ?? next.username
    next.fromName = str('fromName', 60) ?? next.fromName
    const from = str('from', 254)
    if (from !== undefined) {
      if (from !== '' && !ADDRESS.test(from)) bad('from 要是一个邮箱地址')
      next.from = from
    }
    const reply = str('replyTo', 254)
    if (reply !== undefined) {
      if (reply !== '' && !ADDRESS.test(reply)) bad('replyTo 要是一个邮箱地址')
      next.replyTo = reply
    }
    if (body.password !== undefined) {
      if (typeof body.password !== 'string' || body.password.length > 1024) bad('password 不合法')
      next.password = body.password as string
    }
    if (next.security === 'none' && next.host !== '' && !LOOPBACK.test(next.host))
      err(400, 'mail_insecure', 'security none 只能用于回环地址的主机')
    if (next.enabled) {
      if (next.host === '') bad('启用发信需要 host')
      if (next.from === '') err(400, 'mail_from_required', '启用发信需要发件人地址')
      if (o.publicUrl === null) err(400, 'mail_needs_public_url', '启用发信需要 --public-url')
    }
    mail = next
    cancelClosed()
    return view()
  }

  const test = (body: Record<string, unknown>): MockResponse => {
    const to = typeof body.to === 'string' ? body.to.trim() : ''
    if (!ADDRESS.test(to) || /[\r\n]/.test(to)) bad('to 要是一个邮箱地址')
    if (mail.host === '') err(409, 'mail_not_configured', '先填好发信设置')
    if (mail.from === '') err(400, 'mail_from_required', '先填发件人地址')
    testAt = testAt.filter((t) => o.now() - t < HOUR)
    if (testAt.length >= 10) throw new HttpError(429, 'rate_limited', '测试邮件一小时最多 10 封', { __retryAfter: 60 })
    testAt.push(o.now())
    // The mock sends nothing: the message goes to the outbox, as a message to this address would.
    outbox.push({ to, kind: 'test', at: stamp() })
    status = { ok: true, checked: stamp(), message: '' }
    return { status: 200, body: { ok: true } }
  }

  const domainOk = (email: string, domains: string[]) =>
    domains.length === 0 ||
    domains.some((d) => email.toLowerCase().endsWith(`@${d}`) || email.toLowerCase().endsWith(`.${d}`))

  const registerInfo = (): RegisterInfo => {
    const r = o.rules()
    return open()
      ? {
          open: true,
          tenant: { slug: 'default', name: 'Default' },
          domains: r.domains,
          termsUrl: r.termsUrl,
          privacyUrl: r.privacyUrl,
          password: { minLength: 8 },
        }
      : { open: false }
  }

  const register = (body: Record<string, unknown>): MockResponse => {
    if (!open()) err(403, 'registration_closed', '这里不开放注册')
    try {
      o.checkCaptcha('register', 'ip', body.captcha)
    } catch (e) {
      o.noteCaptchaFailure('register', 'ip')
      throw e
    }
    const username = o.checkUsername(body.username)
    const name = o.checkName(body.name)
    const email = typeof body.email === 'string' ? body.email.trim() : ''
    if (!ADDRESS.test(email) || email.length > 254) bad('邮箱不合法')
    const hashed = o.hash(o.checkPassword(body.password))
    const r = o.rules()
    if (!domainOk(email, r.domains)) err(400, 'email_domain_not_allowed', '这个邮箱域名不在允许的范围内')
    const needsTerms = r.termsUrl !== '' || r.privacyUrl !== ''
    if (needsTerms && body.acceptTerms !== true) err(400, 'terms_required', '要同意条款才能注册')
    const live = [...pending.values()].filter((p) => p.expires > o.now())
    if (o.usernameTaken(username) || live.some((p) => p.username === username))
      err(409, 'exists', '这个用户名已经有人用了')
    regTimes.splice(0, regTimes.length, ...regTimes.filter((t) => o.now() - t < HOUR))
    if (regTimes.length >= r.perHour || live.length >= 1000)
      throw new HttpError(429, 'rate_limited', '注册太多了，请稍后再试', { __retryAfter: 600 })
    regTimes.push(o.now())
    const ok = { status: 202, body: { sent: true } }
    const have = [...o.users()].find(
      (u) => !u.disabled && u.emailVerified && u.email.toLowerCase() === email.toLowerCase(),
    )
    if (have) {
      if (take(`to:${email.toLowerCase()}`, 3)) {
        if (canUsePassword(have)) post({ to: email, kind: 'register_exists', link: resetLink(have) })
        else post({ to: email, kind: 'reset_sso' })
      }
      return ok
    }
    if (!take(`to:${email.toLowerCase()}`, 3)) return ok
    const raw = o.random(43)
    pending.set(o.hash(raw), {
      username,
      name,
      email,
      passwordHash: hashed,
      terms: needsTerms ? JSON.stringify({ at: stamp(), termsUrl: r.termsUrl, privacyUrl: r.privacyUrl }) : '',
      expires: o.now() + 24 * HOUR,
    })
    post({ to: email, kind: 'register', link: link('/register/confirm', raw), token: raw })
    return ok
  }

  const registerConfirm = (body: Record<string, unknown>): MockResponse => {
    const h = typeof body.token === 'string' ? o.hash(body.token) : ''
    const p = pending.get(h)
    if (!p || p.expires <= o.now()) return badLink()
    if (!open()) err(403, 'registration_closed', '这里不开放注册')
    if (o.ssoOnlyInEffect()) err(403, 'local_login_refused', '这个服务器只允许通过单点登录进入')
    if (o.usernameTaken(p.username)) err(409, 'exists', '这个用户名已经有人用了')
    pending.delete(h)
    const made = o.makeAccount({
      username: p.username,
      name: p.name,
      email: p.email,
      passwordHash: p.passwordHash,
      terms: p.terms,
    })
    o.record('registered', { user: made.user.id, detail: { email: p.email } })
    return { status: 201, body: { user: made.user }, headers: { 'Set-Cookie': made.cookie } }
  }

  const requestVerify = (u: MailUser): MockResponse => {
    if (!ready()) err(409, 'mail_not_configured', '没有启用发信')
    if (u.email === '') bad('账号没有邮箱')
    if (u.emailVerified) err(409, 'email_verified', '邮箱已经验证过了')
    if (!take(`to:${u.email.toLowerCase()}`, 3))
      throw new HttpError(429, 'rate_limited', '发得太多了', { __retryAfter: 600 })
    const raw = newToken('verify', u.id, u.email, 24 * HOUR)
    post({ to: u.email, kind: 'verify', link: link('/verify-email', raw), token: raw })
    return { status: 202, body: { sent: true } }
  }

  const confirmEmail = (body: Record<string, unknown>, apply: (id: string) => void): MockResponse => {
    const f = find('verify', body.token)
    const u = f ? o.user(f.t.userId) : undefined
    if (!f || !u || u.disabled || u.email !== f.t.email) return badLink()
    f.t.used = true
    apply(u.id)
    o.record('email_verified', { user: u.id, detail: { email: f.t.email } })
    return { status: 204 }
  }

  const forgot = (body: Record<string, unknown>): MockResponse => {
    if (!ready()) err(403, 'password_reset_off', '没有开通找回密码')
    try {
      o.checkCaptcha('forgot', 'ip', body.captcha)
    } catch (e) {
      o.noteCaptchaFailure('forgot', 'ip')
      throw e
    }
    const login = typeof body.login === 'string' ? body.login.trim() : ''
    if (login === '' || login.length > 254) bad('login 是用户名或邮箱')
    const all = [...o.users()].filter((u) => !u.disabled && u.emailVerified && u.email !== '')
    const accounts = (
      login.includes('@')
        ? all.filter((u) => u.email.toLowerCase() === login.toLowerCase())
        : all.filter((u) => u.username === login.toLowerCase())
    ).slice(0, 5)
    const ok = { status: 202, body: { sent: true } }
    if (accounts.length === 0 || !take(`to:${accounts[0].email.toLowerCase()}`, 3)) return ok
    const links = accounts.filter(canUsePassword)
    if (links.length > 0) {
      const urls = links.map(resetLink)
      for (const u of links) o.record('password_reset_requested', { user: u.id, detail: { by: 'user' } })
      post({ to: accounts[0].email, kind: 'reset', link: urls[0], token: urls[0].split('#')[1] })
    } else post({ to: accounts[0].email, kind: 'reset_sso' })
    return ok
  }
  const usable = (raw: unknown) => {
    const f = find('reset', raw)
    const u = f ? o.user(f.t.userId) : undefined
    return f && u && !u.disabled && canUsePassword(u) ? { f, u } : null
  }
  const resetCheck = (body: Record<string, unknown>): MockResponse => {
    const x = usable(body.token)
    if (!x) return badLink()
    return {
      status: 200,
      body: { username: x.u.username, expires: new Date(x.f.t.expires).toISOString().replace(/\.\d+Z$/, 'Z') },
    }
  }
  const reset = (body: Record<string, unknown>): MockResponse => {
    const x = usable(body.token)
    if (!x) return badLink()
    const hash = o.hash(o.checkPassword(body.password))
    x.f.t.used = true
    o.setPassword(x.u.id, hash)
    o.endSessions(x.u.id)
    o.record('password_reset', { user: x.u.id, detail: { by: 'user' } })
    return { status: 204 }
  }
  const adminReset = (actor: MailUser, id: string): MockResponse => {
    const u = o.user(id)
    if (!u) return badLink()
    if (o.isSiteAdmin(u.id) && !o.isSiteAdmin(actor.id)) err(403, 'forbidden', '只有全站管理员能重置全站管理员的密码')
    if (!ready()) err(409, 'mail_not_configured', '没有启用发信')
    if (u.email === '' || !u.emailVerified) err(409, 'email_unverified', '这个账号没有已验证的邮箱')
    if (!canUsePassword(u) || u.disabled) err(409, 'conflict', '这个账号没有可用的密码')
    if (!take(`to:${u.email.toLowerCase()}`, 3))
      throw new HttpError(429, 'rate_limited', '发得太多了', { __retryAfter: 600 })
    const url = resetLink(u)
    o.record('password_reset_requested', { user: u.id, detail: { by: 'admin' } })
    post({ to: u.email, kind: 'reset', link: url, token: url.split('#')[1] })
    return { status: 202, body: { sent: true } }
  }
  // A note to the old address when a verified address is changed: no link.
  const noteChange = (old: { email: string; emailVerified: boolean }) => {
    if (old.email !== '' && old.emailVerified && ready() && take(`to:${old.email.toLowerCase()}`, 3))
      post({ to: old.email, kind: 'email_changed' })
  }
  const cleanUserTokens = (userId: string, kind?: Token['kind']) => {
    for (const [h, t] of tokens) if (t.userId === userId && (!kind || t.kind === kind)) tokens.delete(h)
  }

  return {
    outbox,
    ready,
    open,
    view,
    put,
    test,
    registerInfo,
    register,
    registerConfirm,
    requestVerify,
    confirmEmail,
    forgot,
    resetCheck,
    reset,
    adminReset,
    noteChange,
    cleanUserTokens,
    cancelClosed,
    // The SMTP password, as the secrets endpoints change it (write-only).
    setPassword: (v: string) => {
      mail.password = v
    },
    pendingCount: () => pending.size,
    isRecord,
  }
}
export type MailBook = ReturnType<typeof createMailBook>
