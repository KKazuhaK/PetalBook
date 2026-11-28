// The person's own account, as the settings read and change it: the profile, password, linked sign-ins, passkeys,
// addresses, API tokens and connected apps. No limit, no recency of the sign-in and no check of a password: a test that
// needs a refusal says so with `fx.fail`.

import type {
  AccountEmail,
  AccountInfo,
  AccountMethod,
  AccountUsage,
  ApiToken,
  CreatedToken,
  Grant,
  Link,
  Passkey,
} from '../../types.ts'
import { rfc3339, newId, Refusal, reply, type Ctx, type Route } from '../kit.ts'
import { current, view } from './auth.ts'

// The person as the account's answers say them: without the permissions the session carries.
const plain = (u: Parameters<typeof view>[0]) => {
  const { permissions: _permissions, ...user } = view(u)
  return (void _permissions, user)
}

const me = (c: Ctx) => {
  const u = current(c.data.auth)
  if (!u) throw new Refusal(401, 'unauthenticated', 'sign in first')
  return u
}

// The account's addresses, the primary first.
function emailsOf(c: Ctx): AccountEmail[] {
  const u = me(c)
  const own: AccountEmail[] = u.email
    ? [{ email: u.email, primary: true, verified: u.emailVerified, created: u.created }]
    : []
  return [...own, ...c.data.area.account.emails.filter((e) => !e.primary)]
}

// A token as the list says it: its state follows the clock.
function tokenView(c: Ctx, t: ApiToken): ApiToken {
  const expired = t.expires !== null && Date.parse(t.expires) <= c.now()
  return { ...t, state: t.revoked ? 'revoked' : expired ? 'expired' : 'active' }
}

// The id of the link a person's connection at a provider is: one per provider, made when they connect.
const connectionLinkId = (provider: string) => `link-${provider}`

// The person's links as the list says them: the sign-ins they linked, and an account at each provider they connected
// repositories at (the same link when it also signs them in).
function linksOf(c: Ctx): Link[] {
  const sign = c.data.area.account.links.map((l) => ({
    login: '',
    repos: false,
    state: 'ok' as const,
    spaces: 0,
    ...l,
  }))
  for (const p of c.data.area.connections.providers) {
    if (!p.connection) continue
    const repos = {
      login: p.connection.login,
      repos: true,
      state: p.connection.state,
      spaces: p.connection.spaces,
      ...(p.connection.refreshExpires ? { refreshExpires: p.connection.refreshExpires } : {}),
    }
    const own = sign.find((l) => l.providerId === p.id)
    if (own) Object.assign(own, repos)
    else
      sign.push({
        id: connectionLinkId(p.id),
        providerId: p.id,
        provider: p.name,
        slug: p.id,
        kind: p.type as Link['kind'],
        label: p.connection.login,
        signin: false,
        created: p.connection.connected,
        ...repos,
      })
  }
  return sign
}

// The provider a link is the person's account at: a link made by connecting, or a sign-in link that also holds repositories.
function providerOfLink(c: Ctx, id: string) {
  const own = c.data.area.account.links.find((l) => l.id === id)
  return c.data.area.connections.providers.find((p) => connectionLinkId(p.id) === id || p.id === own?.providerId)
}

// Drops the tokens of the link at a provider.
function disconnect(c: Ctx, id: string) {
  const p = providerOfLink(c, id)
  if (!p?.connection) throw new Refusal(404, 'not_found', 'No repositories are connected through this link.')
  p.connection = null
  const f = c.data.area.connections.forges.find((x) => x.id === p.id)
  if (f) f.connections = 0
}

export const accountRoutes: Route[] = [
  [
    'GET',
    '/api/account',
    (c): AccountInfo => {
      const u = me(c)
      const { permissions: _permissions, ...user } = view(u)
      void _permissions
      const a = c.data.area.account
      const passkeys = a.passkeys.length
        ? a.passkeys
        : Array.from({ length: u.passkeys ?? 0 }, (_, i) => ({
            id: `pk${i}`,
            name: 'passkey',
            created: user.created,
            synced: false,
          }))
      // The one list of sign-in methods, as the server's: the password, the links that sign in, the passkeys.
      const sign = linksOf(c).filter((l) => l.signin)
      const count = 1 + sign.length + passkeys.length
      const methods: AccountMethod[] = [
        { type: 'password', added: user.created, lastUsed: user.created, removable: false },
        ...sign.map((l) => ({
          type: 'identity' as const,
          id: l.id,
          provider: l.provider,
          providerSlug: l.slug,
          kind: l.kind,
          label: l.label,
          added: l.created,
          lastUsed: null,
          removable: count >= 2,
        })),
        ...passkeys.map((p) => ({
          type: 'passkey' as const,
          id: p.id,
          name: p.name,
          synced: p.synced,
          added: p.created,
          lastUsed: (p as { lastUsed?: string | null }).lastUsed ?? null,
          removable: count >= 2,
        })),
      ]
      return {
        user,
        hasPassword: true,
        passkeys,
        methods,
        canUnlink: count >= 2,
        groups: c.data.admin.groups
          .filter((g) => g.members.includes(u.id))
          .map((g) => ({ id: g.id, name: g.name, source: g.source })),
        emails: emailsOf(c),
      }
    },
  ],
  [
    'PATCH',
    '/api/account',
    (c) => {
      const u = me(c)
      for (const k of ['name', 'username', 'email'] as const) if (c.body[k] !== undefined) u[k] = c.body[k]
      // A new address is not verified until its link is followed.
      if (c.body.email !== undefined) u.emailVerified = false
      return { user: plain(u) }
    },
  ],
  ['POST', '/api/account/password', () => undefined],
  [
    'DELETE',
    '/api/account/links/:id/repos',
    (c) => {
      disconnect(c, c.params.id)
    },
  ],
  [
    'DELETE',
    '/api/account/links/:id',
    (c) => {
      const a = c.data.area.account
      const p = providerOfLink(c, c.params.id)
      if (p?.connection) disconnect(c, c.params.id)
      a.links = a.links.filter((l) => l.id !== c.params.id)
    },
  ],
  ['GET', '/api/account/links', (c) => ({ links: linksOf(c), providers: c.data.area.connections.providers })],
  [
    'POST',
    '/api/account/links/:provider/start',
    (c) => {
      if (c.body.purpose === 'repos') {
        const p = c.data.area.connections.providers.find((x) => x.id === c.params.provider)
        if (!p) throw new Refusal(404, 'not_found', 'no such provider')
        return { url: `/api/account/links/${p.id}/callback?state=fixture` }
      }
      if (c.data.area.account.links.some((l) => l.providerId === c.params.provider))
        throw new Refusal(409, 'already_linked', 'already linked at this provider')
      return { url: `/api/auth/oidc/${c.params.provider}/callback?link=1` }
    },
  ],

  // passkeys
  [
    'POST',
    '/api/account/passkeys/begin',
    (c) => {
      c.data.area.account.passkeyName = String(c.body.name ?? '')
      return {
        ceremony: 'ceremony-register',
        options: {
          challenge: 'Y2hhbGxlbmdl',
          rp: { name: 'PetalBook', id: 'localhost' },
          user: { id: 'dXNlcg', name: 'user', displayName: 'user' },
          pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
        },
      }
    },
  ],
  [
    'POST',
    '/api/account/passkeys/finish',
    (c) => {
      const a = c.data.area.account
      c.data.auth.hold = null
      const passkey: Passkey = {
        id: newId(c.data),
        name: a.passkeyName,
        created: rfc3339(c.now()),
        lastUsed: null,
        synced: c.body.synced === true,
      }
      a.passkeys.push(passkey)
      return { passkeys: a.passkeys }
    },
  ],
  [
    'PATCH',
    '/api/account/passkeys/:id',
    (c) => {
      const p = c.data.area.account.passkeys.find((x) => x.id === c.params.id)
      if (!p) throw new Refusal(404, 'not_found', 'no such passkey')
      p.name = c.body.name
      return p
    },
  ],
  [
    'DELETE',
    '/api/account/passkeys/:id',
    (c) => {
      const a = c.data.area.account
      a.passkeys = a.passkeys.filter((x) => x.id !== c.params.id)
    },
  ],

  // addresses
  [
    'POST',
    '/api/account/emails',
    (c) => {
      const a = c.data.area.account
      a.emails.push({ email: c.body.email, primary: false, verified: false, created: rfc3339(c.now()) })
      c.data.sent.mail.push({ to: c.body.email, kind: 'verify', token: `verify-${c.body.email}` })
      return reply(202, { sent: true })
    },
  ],
  [
    'POST',
    '/api/account/emails/verify',
    (c) => {
      c.data.sent.mail.push({ to: c.body.email, kind: 'verify', token: `verify-${c.body.email}` })
      return { sent: true }
    },
  ],
  [
    'POST',
    '/api/account/emails/primary',
    (c) => {
      const u = me(c)
      const a = c.data.area.account
      const next = a.emails.find((e) => e.email === c.body.email)
      if (next) {
        // The old primary becomes one of the others.
        if (u.email) a.emails.push({ email: u.email, primary: false, verified: u.emailVerified, created: u.created })
        a.emails = a.emails.filter((e) => e !== next)
        u.email = next.email
        u.emailVerified = next.verified
      }
      return { user: plain(u) }
    },
  ],
  [
    'DELETE',
    '/api/account/emails/:email',
    (c) => {
      const a = c.data.area.account
      a.emails = a.emails.filter((e) => e.email !== c.params.email)
    },
  ],
  [
    'POST',
    '/api/account/email/verify',
    (c) => {
      const u = me(c)
      c.data.sent.mail.push({ to: u.email, kind: 'verify', token: `verify-${u.email}` })
      return { sent: true }
    },
  ],

  // API tokens
  ['GET', '/api/account/tokens', (c) => ({ tokens: c.data.area.account.tokens.map((t) => tokenView(c, t)) })],
  [
    'POST',
    '/api/account/tokens',
    (c): unknown => {
      const a = c.data.area.account
      const now = c.now()
      const id = newId(c.data, '01FT')
      const secret = `pbk_${hashed(id)}`
      const days = c.body.expiresInDays === undefined ? 90 : c.body.expiresInDays
      const token: ApiToken = {
        id,
        name: String(c.body.name ?? '').trim(),
        prefix: secret.slice(0, 12),
        access: c.body.access ?? 'read',
        spaces: (c.body.spaces as string[]).map((sid) => ({
          id: sid,
          name: c.data.spaces.find((s) => s.info.id === sid)?.info.name ?? '',
        })),
        created: rfc3339(now),
        expires: days === 0 ? null : rfc3339(now + days * 86_400_000),
        lastUsed: null,
        revoked: null,
        state: 'active',
      }
      a.tokens.push(token)
      const out: CreatedToken = { token, secret }
      return reply(201, out)
    },
  ],
  [
    'DELETE',
    '/api/account/tokens/:id',
    (c) => {
      const t = c.data.area.account.tokens.find((x) => x.id === c.params.id)
      if (!t) throw new Refusal(404, 'not_found', 'no such token')
      t.revoked = rfc3339(c.now())
    },
  ],

  // what the person has used: stored numbers, never measured here
  [
    'GET',
    '/api/account/usage',
    (c): AccountUsage => ({
      spaces: { used: c.data.spaces.length, limit: 20 },
      bytes: { used: 0, limit: null },
      pages: { used: c.data.spaces.reduce((n, s) => n + s.pages.length, 0), limit: null },
      measuredAt: null,
      notices: [],
    }),
  ],

  // connected apps
  [
    'GET',
    '/api/account/grants',
    (c): { grants: Grant[] } => {
      const g = c.data.area.account.grants
      if (!g) throw new Refusal(404, 'not_found', 'no such endpoint')
      return { grants: g }
    },
  ],
  [
    'DELETE',
    '/api/account/grants/:id',
    (c) => {
      const a = c.data.area.account
      a.grants = (a.grants ?? []).filter((g) => g.id !== c.params.id)
    },
  ],
]

// 43 characters of base 62 that depend only on the id: a secret that is repeatable.
function hashed(id: string): string {
  const alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
  let h = 0x811c9dc5
  let out = ''
  for (let i = 0; i < 43; i++) {
    h = Math.imul(h ^ (id.charCodeAt(i % id.length) + i), 0x01000193) >>> 0
    out += alphabet[h % 62]
  }
  return out
}
