// The administration console: people, invites, spaces, storage, usage, the recycle bin, tenants, plans, settings,
// secrets, groups, mail, sign-in security, the audit log, tokens and the daily text. Each route reads or writes the
// records in data/admin.ts. None checks who asks, what a value may be, a quota or a name's form: a test that needs the
// server's refusal says it with `fx.fail`.

import type {
  AdminSpace,
  AdminStorageBody,
  AuditItem,
  ConfirmResult,
  DailyApi,
  Group,
  GroupPerson,
  PersonUsage,
  RecycleItem,
  RestoreResult,
  SiteSetting,
  StoragePerson,
  SpaceDeletionPreview,
  Tenant,
  TenantSetting,
  User,
  UserDeletionPlan,
} from '../../types.ts'
import { TENANT_KEYS } from '../data/settingDefs.ts'
import {
  changeProvider,
  makeProvider,
  AUDIT_CATEGORIES,
  DEFAULT_TENANT,
  type GroupRec,
  type PersonRec,
} from '../data/admin.ts'
import type { FixtureData, SpaceData } from '../data/model.ts'
import type { UserRec } from '../data/people.ts'
import { add, find, newId, paged, remove, reply, rfc3339, type Ctx, type Route } from '../kit.ts'

const now = (c: Ctx) => rfc3339(c.now())
const A = (c: Ctx) => c.data.admin
// Every space of the server: the ones the signed-in person belongs to, and the ones they only administer.
const spacesOf = (c: Ctx) => [...c.data.spaces, ...A(c).otherSpaces]
const signedIn = (d: FixtureData) => d.auth.users.find((u) => u.id === d.auth.signedIn) ?? d.auth.users[0]
const who = (u: Pick<UserRec, 'id' | 'username' | 'name'>) => ({ id: u.id, username: u.username, name: u.name })
const has = (text: string, q: string | null) => !q || text.toLowerCase().includes(q.toLowerCase())
const page = <T>(c: Ctx, rows: T[]) => paged(rows, c.query, 50)

// --- people ---------------------------------------------------------------------------------------------------------

function person(c: Ctx, id: string): PersonRec {
  return (A(c).people[id] ??= { sessions: [], identities: [], passkeys: [] })
}
function userOf(c: Ctx, id: string): UserRec {
  return find(c.data.auth.users, (u) => u.id === id || u.username === id, 'the person')
}
// The list has the counts and when the person was last active, one person's page the counts, a change neither.
function userView(c: Ctx, u: UserRec, shown: 'list' | 'one' | 'change' = 'list'): User {
  const { password: _p, passkeys, permissions: _perm, ...rest } = u
  void _p
  void _perm
  const rec = person(c, u.id)
  return {
    ...rest,
    quota: rec.quota ?? { spaces: null, bytes: null, pages: null },
    ...(shown !== 'change' ? { passkeys: passkeys ?? rec.passkeys.length, sso: rec.identities.length } : {}),
    ...(shown === 'list' ? { lastActive: rec.lastActive ?? u.created } : {}),
  }
}

// --- spaces ---------------------------------------------------------------------------------------------------------

function adminSpace(c: Ctx, id: string): AdminSpace {
  const s = find(spacesOf(c), (x) => x.info.id === id, 'the space')
  const owner = who(c.data.auth.users.find((u) => u.id === c.data.auth.signedIn) ?? c.data.auth.users[0] ?? DEAD)
  const extra = A(c).spaceExtra[id] ?? {}
  return {
    id: s.info.id,
    name: s.info.name,
    personal: s.info.personal,
    inbox: s.info.inbox,
    created: now(c),
    disabled: !!s.info.disabled,
    owner,
    members: 1,
    sizeBytes: null,
    sizeAt: null,
    sync: s.info.sync ?? 'local',
    location: 'server',
    counted: true,
    view: A(c).views[id] ?? null,
    ...({ tenant: DEFAULT_TENANT } as object),
    ...extra,
    storage: {
      cache: 'local',
      pinned: false,
      exempt: false,
      lastUsed: now(c),
      cloneBytes: 0,
      indexBytes: 0,
      assetCacheBytes: 0,
      assets: 'space',
      remote: null,
      ...extra.storage,
    },
  }
}
const DEAD = { id: '', username: '', name: '' }

function recycled(c: Ctx, init: Partial<RecycleItem>): RecycleItem {
  const me = signedIn(c.data)
  const at = c.now()
  return add(A(c).recycle, {
    id: newId(c.data, '01RC'),
    kind: 'space',
    tenant: DEFAULT_TENANT,
    space: null,
    user: null,
    with: [],
    bytes: 0,
    deletedAt: rfc3339(at),
    deletedBy: { id: me.id, username: me.username },
    purgeAt: rfc3339(at + 30 * 86_400_000),
    reason: '',
    state: 'ready',
    restorable: true,
    problems: null as unknown as string[],
    ...init,
  })
}

// --- settings and tenants -------------------------------------------------------------------------------------------

const siteOf = (c: Ctx, key: string): SiteSetting => find(A(c).settings, (s) => s.key === key, 'the setting')

function tenantSetting(c: Ctx, tenant: string, def: SiteSetting): TenantSetting {
  const own = A(c).tenantRules[tenant]?.[def.key] ?? null
  return {
    key: def.key,
    kind: def.kind,
    layers: def.layers,
    ...(def.choices ? { choices: def.choices } : {}),
    site: { value: def.site.value, lower: def.site.lower },
    tenant: own ?? { value: null, lower: null, inEffect: true, siteLocked: false },
    // A rule that is not in effect leaves the site's value.
    effective: own && own.inEffect && own.value !== null && own.value !== undefined ? own.value : def.site.value,
  }
}

function tenantView(c: Ctx, t: Tenant): Tenant {
  if (!t.default) return t
  return { ...t, usage: { ...t.usage, people: c.data.auth.users.length, spaces: spacesOf(c).length } }
}

function secretsOf(c: Ctx, tenant?: string) {
  const rows = tenant
    ? (A(c).secrets.tenants[tenant] ??= { 'mail.password': { name: 'mail.password', set: false } })
    : A(c).secrets.site
  return rows
}

// What deleting a person does, worked out from the spaces they own: the ones nobody else is in go to the bin with
// them, the shared ones go to the heir named (and need one when none is).
function deletionPlan(c: Ctx, u: UserRec, heir?: string): UserDeletionPlan {
  const owned = spacesOf(c)
    .map((s) => adminSpace(c, s.info.id))
    .filter((s) => s.owner?.id === u.id)
  const ref = (s: AdminSpace) => ({ id: s.id, name: s.name, personal: s.personal })
  const shared = owned.filter((s) => !s.personal && s.members > 1)
  const to = heir ? c.data.auth.users.find((x) => x.id === heir) : undefined
  return {
    user: { id: u.id, username: u.username },
    recycle: owned.filter((s) => !shared.includes(s)).map((s) => ({ space: ref(s), bytes: 0 })),
    transfer: to ? shared.map((s) => ({ space: ref(s), to: { id: to.id, username: to.username }, why: 'heir' })) : [],
    leave: [],
    revoke: { tokens: 0, grants: 0, passkeys: 0, identities: 0, github: false, sessions: 0, connections: [] },
    groups: 0,
    needsHeir: to ? [] : shared.map(ref),
    blockers: u.id === c.data.auth.signedIn ? [{ reason: 'self' }] : [],
    bytes: 0,
    recycleDays: 30,
  }
}

function spacePreview(c: Ctx, s: SpaceData): SpaceDeletionPreview {
  return {
    space: { id: s.info.id, name: s.info.name },
    pages: s.pages.length,
    bytes: 0,
    heldBytes: 0,
    attachments: { inSpace: null, repository: false },
    members: adminSpace(c, s.info.id).members,
    groups: 0,
    drafts: 0,
    remote: { url: '', kept: false },
    assetsRemote: { url: '', kept: false },
    cache: 'local',
    exportable: true,
    recycleDays: 30,
  }
}

// --- a confirmed action ---------------------------------------------------------------------------------------------

function confirmPlan(c: Ctx): ConfirmResult {
  const b = c.body as { action: string; spaces?: string[]; users?: string[]; items?: string[]; heir?: string }
  const token = `c_${newId(c.data, '')}`
  A(c).confirms[token] = { action: b.action, spaces: b.spaces ?? [], users: b.users ?? [], items: b.items ?? [] }
  const spaces = (b.spaces ?? []).map((id) => {
    const s = spacesOf(c).find((x) => x.info.id === id)
    if (s && b.action === 'delete_spaces') return spacePreview(c, s)
    return { space: { id, name: s?.info.name ?? id }, action: b.action }
  })
  const needsHeir: ConfirmResult['needsHeir'] = []
  const blockers: ConfirmResult['blockers'] = []
  const users = (b.users ?? []).map((id) => {
    const u = c.data.auth.users.find((x) => x.id === id)
    if (u && b.action === 'delete_users') {
      const p = deletionPlan(c, u, b.heir)
      if (p.needsHeir.length) needsHeir.push({ user: id, spaces: p.needsHeir })
      for (const k of p.blockers) blockers.push({ user: id, reason: k.reason })
      return p
    }
    return { user: { id, username: u?.username ?? id }, action: b.action }
  })
  const items = (b.items ?? []).map((id) => {
    const r = A(c).recycle.find((x) => x.id === id)
    return {
      item: id,
      kind: r?.kind ?? 'space',
      name: r?.space?.name ?? r?.user?.username ?? id,
      bytes: 0,
      action: b.action,
      deletedAt: r?.deletedAt ?? now(c),
    }
  })
  const plan = [...spaces, ...users, ...items] as ConfirmResult['plan']
  return {
    confirm: token,
    expires: rfc3339(c.now() + 300_000),
    count: plan.length,
    plan,
    bytes: 0,
    needsHeir,
    blockers,
  }
}

// What a confirmed action does to the things it named: the job is done at once, and the records show the result.
function applyBulk(c: Ctx, plan: { action: string; spaces: string[]; users: string[]; items: string[] }) {
  for (const id of plan.spaces) {
    const s = spacesOf(c).find((x) => x.info.id === id)
    if (!s) continue
    const extra = A(c).spaceExtra[id] ?? {}
    const storage = (patch: object) =>
      (A(c).spaceExtra[id] = { ...extra, storage: { ...adminSpace(c, id).storage, ...patch } })
    if (plan.action === 'disable_spaces') s.info.disabled = true
    else if (plan.action === 'enable_spaces') s.info.disabled = false
    else if (plan.action === 'pin') storage({ pinned: true })
    else if (plan.action === 'unpin') storage({ pinned: false })
    else if (plan.action === 'evict') storage({ cache: 'evicted' })
    else if (plan.action === 'delete_spaces') {
      for (const list of [c.data.spaces, A(c).otherSpaces]) if (list.includes(s)) remove(list, s)
      recycled(c, { kind: 'space', space: { id, name: s.info.name, personal: s.info.personal } })
    }
  }
  for (const id of plan.users) {
    const u = c.data.auth.users.find((x) => x.id === id)
    if (!u) continue
    if (plan.action === 'disable_users') u.disabled = true
    else if (plan.action === 'enable_users') u.disabled = false
    else if (plan.action === 'delete_users') {
      remove(c.data.auth.users, u)
      recycled(c, { kind: 'user', user: { id, username: u.username } })
    }
  }
  if (plan.action === 'restore' || plan.action === 'purge')
    A(c).recycle = A(c).recycle.filter((r) => !plan.items.includes(r.id))
}

export const adminRoutes: Route[] = [
  // --- people ---
  [
    'GET',
    '/api/users',
    (c) => {
      const q = c.query
      const rows = c.data.auth.users
        .filter((u) => has(`${u.username} ${u.name}`, q.get('q')))
        .filter((u) => !q.get('role') || (q.get('role') === 'site' ? u.siteAdmin : u.role === q.get('role')))
        .filter((u) => !q.get('state') || (q.get('state') === 'disabled') === u.disabled)
        .filter((u) => !q.get('enabled') || !u.disabled)
        .map((u) => userView(c, u))
      // The order the list was asked in (the oldest first when it was not).
      const by = c.query.get('sort') === 'name' ? 'name' : 'created'
      const sign = c.query.get('order') === 'desc' ? -1 : 1
      rows.sort((a, b) => sign * String(a[by]).localeCompare(String(b[by])))
      const p = page(c, rows)
      return { users: p.items, next: p.next }
    },
  ],
  [
    'GET',
    '/api/users/:id',
    (c) => {
      const u = userOf(c, c.params.id)
      const v = userView(c, u, 'one')
      return {
        user: {
          ...v,
          emails: u.email ? [{ email: u.email, primary: true, verified: u.emailVerified, created: u.created }] : [],
        },
      }
    },
  ],
  [
    'PATCH',
    '/api/users/:id',
    (c) => {
      const u = userOf(c, c.params.id)
      const { quota, emailMode: _mode, ...rest } = c.body
      void _mode
      Object.assign(u, rest)
      if (quota) person(c, u.id).quota = { spaces: null, bytes: null, pages: null, ...person(c, u.id).quota, ...quota }
      return { user: userView(c, u, 'change') }
    },
  ],
  [
    'DELETE',
    '/api/users/:id',
    (c) => {
      const u = userOf(c, c.params.id)
      remove(c.data.auth.users, u)
      return recycled(c, { kind: 'user', user: { id: u.id, username: u.username } })
    },
  ],
  [
    'GET',
    '/api/users/:id/memberships',
    (c) => ({
      // The spaces a person owns (the console's records know no other membership).
      spaces: spacesOf(c)
        .filter((s) => adminSpace(c, s.info.id).owner?.id === userOf(c, c.params.id).id)
        .map((s) => ({ id: s.info.id, name: s.info.name, role: 'owner', personal: s.info.personal })),
      groups: A(c)
        .groups.filter((g) => g.members.includes(c.params.id))
        .map((g) => ({ id: g.id, name: g.name, source: g.source })),
    }),
  ],
  ['GET', '/api/users/:id/sessions', (c) => ({ sessions: person(c, c.params.id).sessions })],
  [
    'DELETE',
    '/api/users/:id/sessions',
    (c) => {
      const p = person(c, c.params.id)
      const ended = p.sessions.length
      p.sessions = []
      return { ended }
    },
  ],
  [
    'DELETE',
    '/api/users/:id/sessions/:session',
    (c) => {
      const p = person(c, c.params.id)
      p.sessions = p.sessions.filter((s) => s.id !== c.params.session)
    },
  ],
  ['GET', '/api/users/:id/identities', (c) => ({ identities: person(c, c.params.id).identities })],
  [
    'DELETE',
    '/api/users/:id/identities/:identity',
    (c) => {
      const p = person(c, c.params.id)
      p.identities = p.identities.filter((i) => i.id !== c.params.identity)
    },
  ],
  ['GET', '/api/users/:id/passkeys', (c) => ({ passkeys: person(c, c.params.id).passkeys })],
  [
    'DELETE',
    '/api/users/:id/passkeys/:passkey',
    (c) => {
      const p = person(c, c.params.id)
      p.passkeys = p.passkeys.filter((k) => k.id !== c.params.passkey)
    },
  ],
  ['POST', '/api/users/:id/password', () => undefined],
  [
    'POST',
    '/api/users/:id/password-reset',
    (c) => {
      const u = userOf(c, c.params.id)
      c.data.sent.mail.push({ to: u.email, kind: 'reset' })
      return { sent: true }
    },
  ],
  ['GET', '/api/invites', (c) => ({ invites: A(c).invites })],
  [
    'POST',
    '/api/invites',
    (c) => {
      const me = signedIn(c.data)
      const inv = add(A(c).invites, {
        id: newId(c.data, '01IV'),
        role: c.body.role ?? 'member',
        created: now(c),
        expires: rfc3339(c.now() + (c.body.days ?? 7) * 86_400_000),
        createdBy: me.username,
        tenant: DEFAULT_TENANT,
      })
      const { createdBy: _by, ...made } = inv
      void _by
      return reply(201, { ...made, token: `invite-${inv.id}`, siteAdmin: false })
    },
  ],
  [
    'DELETE',
    '/api/invites/:id',
    (c) =>
      remove(
        A(c).invites,
        find(A(c).invites, (i) => i.id === c.params.id),
      ),
  ],
  [
    'GET',
    '/api/admin/users/:id/deletion',
    (c) => deletionPlan(c, userOf(c, c.params.id), c.query.get('heir') ?? undefined),
  ],
  [
    'POST',
    '/api/admin/users/:id/view-as',
    (c) => {
      const id = `va_${newId(c.data, '')}`
      A(c).views[`as:${c.params.id}`] = { since: now(c), until: rfc3339(c.now() + 3_600_000) }
      return { id, until: rfc3339(c.now() + 3_600_000) }
    },
  ],
  ['DELETE', '/api/admin/users/:id/view-as', (c) => void delete A(c).views[`as:${c.params.id}`]],

  // --- spaces ---
  [
    'GET',
    '/api/admin/spaces',
    (c) => {
      const q = c.query
      const rows = spacesOf(c)
        .map((s) => adminSpace(c, s.info.id))
        .filter((s) => has(s.name, q.get('q')))
        .filter((s) => !q.get('cache') || s.storage.cache === q.get('cache'))
        .filter((s) => !q.get('owner') || s.owner?.id === q.get('owner'))
        .filter((s) => !q.get('overdue') || s.storage.remote?.overdue)
        .filter((s) => !q.get('pinned') || s.storage.pinned)
        .filter((s) => !q.get('exempt') || s.storage.exempt)
      return page(c, rows)
    },
  ],
  ['GET', '/api/admin/spaces/:id', (c) => adminSpace(c, c.params.id)],
  [
    'PUT',
    '/api/admin/spaces/:id/storage',
    (c) => {
      const cur = adminSpace(c, c.params.id)
      const storage = { ...cur.storage }
      if ('pin' in c.body) storage.pinned = !!c.body.pin
      if ('exempt' in c.body) storage.exempt = !!c.body.exempt
      A(c).spaceExtra[c.params.id] = { ...A(c).spaceExtra[c.params.id], storage }
      return adminSpace(c, c.params.id)
    },
  ],
  [
    'POST',
    '/api/admin/spaces/:id/evict',
    (c) => {
      const cur = adminSpace(c, c.params.id)
      A(c).spaceExtra[c.params.id] = { ...A(c).spaceExtra[c.params.id], storage: { ...cur.storage, cache: 'evicted' } }
    },
  ],
  [
    'POST',
    '/api/admin/spaces/:id/size',
    (c) => {
      // What measuring finds is what a test put in the space's record (nothing: an empty directory).
      const had = A(c).spaceExtra[c.params.id]
      const out = { sizeBytes: had?.sizeBytes ?? 0, sizeAt: had?.sizeAt ?? now(c) }
      A(c).spaceExtra[c.params.id] = { ...had, ...out }
      return out
    },
  ],
  [
    'POST',
    '/api/admin/spaces/:id/transfer',
    (c) => {
      const u = userOf(c, c.body.userId)
      A(c).spaceExtra[c.params.id] = { ...A(c).spaceExtra[c.params.id], owner: who(u) }
    },
  ],
  [
    'POST',
    '/api/admin/spaces/:id/takeover',
    (c) => {
      A(c).spaceExtra[c.params.id] = { ...A(c).spaceExtra[c.params.id], owner: who(signedIn(c.data)) }
    },
  ],
  [
    'POST',
    '/api/admin/spaces/:id/disable',
    (c) => void (find(spacesOf(c), (s) => s.info.id === c.params.id).info.disabled = true),
  ],
  [
    'POST',
    '/api/admin/spaces/:id/enable',
    (c) => void (find(spacesOf(c), (s) => s.info.id === c.params.id).info.disabled = false),
  ],
  [
    'DELETE',
    '/api/admin/spaces/:id',
    (c) => {
      const s = find(spacesOf(c), (x) => x.info.id === c.params.id, 'the space')
      for (const list of [c.data.spaces, A(c).otherSpaces]) if (list.includes(s)) remove(list, s)
      return recycled(c, {
        kind: 'space',
        space: { id: s.info.id, name: s.info.name, personal: s.info.personal },
        reason: c.body.reason ?? '',
      })
    },
  ],
  [
    'GET',
    '/api/admin/spaces/:id/deletion',
    (c) =>
      spacePreview(
        c,
        find(spacesOf(c), (x) => x.info.id === c.params.id, 'the space'),
      ),
  ],
  [
    'POST',
    '/api/admin/spaces/:id/view',
    (c) => {
      const v = { since: now(c), until: rfc3339(c.now() + 3_600_000) }
      A(c).views[c.params.id] = v
      return { space: c.params.id, until: v.until }
    },
  ],
  ['DELETE', '/api/admin/spaces/:id/view', (c) => void delete A(c).views[c.params.id]],
  [
    'GET',
    '/api/admin/status',
    (c) => ({
      version: 'fixture',
      startedAt: now(c),
      uptimeSeconds: 0,
      database: 'sqlite',
      spaces: spacesOf(c).length,
      users: c.data.auth.users.length,
      openSpaces: spacesOf(c).length,
      diskFreeBytes: 50 * 1024 ** 3,
      goVersion: 'go1.27.4',
      build: { commit: '3f9c2ab', time: '2026-11-25T22:48:00Z', timeKind: 'commit', modified: false },
      storage: {
        spaces: { local: spacesOf(c).length, full: 0, partial: 0, evicted: 0, restoring: 0 },
        cloneBytes: 0,
        indexBytes: 0,
        assetCacheBytes: 0,
        clonesBudget: null,
        assetsBudget: 5 * 1024 ** 3,
        restoresWaiting: 0,
        remotes: { inGrace: 0, overdue: 0 },
      },
    }),
  ],
  ['GET', '/api/admin/permissions', () => ({ permissions: [] })],

  // --- storage and usage ---
  [
    'GET',
    '/api/admin/storage',
    (c): AdminStorageBody => ({
      remotes: { since: null },
      git: { version: '2.53.0', partialClone: true },
      affected: { inGrace: 0, overdue: 0, exempt: 0 },
      summary: {
        tenant: null,
        spaces: { local: spacesOf(c).length, full: 0, partial: 0, evicted: 0, archived: 0, restoring: 0 },
        people: c.data.auth.users.length,
        dormant: 0,
        heldBytes: 0,
        usageBytes: 0,
        assetCacheBytes: 0,
        recycle: { items: A(c).recycle.length, bytes: 0 },
        exports: { items: A(c).exports.length, bytes: 0 },
        remotes: { inGrace: 0, overdue: 0 },
        recycleDays: 30,
        diskFreeBytes: 50 * 1024 ** 3,
        measuredAt: now(c),
      },
    }),
  ],
  [
    'GET',
    '/api/admin/storage/people',
    (c) => {
      const rows = c.data.auth.users
        .filter((u) => has(`${u.username} ${u.name}`, c.query.get('q')))
        .map((u): StoragePerson => storagePerson(c, u))
      return page(c, rows)
    },
  ],
  [
    'GET',
    '/api/admin/storage/people/:id',
    (c) => {
      const u = userOf(c, c.params.id)
      return {
        ...storagePerson(c, u),
        items: spacesOf(c)
          .map((s) => adminSpace(c, s.info.id))
          .filter((s) => s.owner?.id === u.id),
      }
    },
  ],
  [
    'GET',
    '/api/admin/usage',
    (c) => {
      const num = (key: string) => (siteOf(c, key).site.value as number | null) ?? null
      const rows = c.data.auth.users
        .map((u): PersonUsage => {
          const rec = person(c, u.id)
          const own = rec.quota ?? { spaces: null, bytes: null, pages: null }
          return {
            user: who(u),
            tenant: u.tenant?.id ?? DEFAULT_TENANT,
            spaces: spacesOf(c).length,
            bytes: 0,
            pages: 0,
            limits: {
              spaces: own.spaces ?? num('quota.spacesPerPerson'),
              bytes: own.bytes ?? num('quota.bytesPerPerson'),
              pages: own.pages ?? num('quota.pagesPerPerson'),
            },
            own,
            ownInEffect: { spaces: true, bytes: true, pages: true },
            over: [],
            measuredAt: null,
            ...rec.usage,
          }
        })
        .filter((r) => !c.query.get('over') || r.over.length > 0)
        .sort((a, b) => b.bytes - a.bytes)
      return page(c, rows)
    },
  ],

  // --- the recycle bin, confirmed actions, jobs, exports ---
  [
    'GET',
    '/api/admin/recycle',
    (c) =>
      page(
        c,
        A(c).recycle.filter((r) => !c.query.get('kind') || r.kind === c.query.get('kind')),
      ),
  ],
  [
    'POST',
    '/api/admin/recycle/:id/restore',
    (c): RestoreResult => {
      const item = find(A(c).recycle, (r) => r.id === c.params.id, 'the item')
      remove(A(c).recycle, item)
      return {
        restored: { item: item.id, ...(item.space ? { space: { id: item.space.id, name: item.space.name } } : {}) },
        skipped: [],
      }
    },
  ],
  [
    'DELETE',
    '/api/admin/recycle/:id',
    (c) => {
      const item = find(A(c).recycle, (r) => r.id === c.params.id, 'the item')
      remove(A(c).recycle, item)
      return reply(202, item)
    },
  ],
  ['POST', '/api/admin/confirm', (c) => confirmPlan(c)],
  [
    'POST',
    '/api/admin/bulk',
    (c) => {
      const plan = find(Object.entries(A(c).confirms), ([k]) => k === c.body.confirm, 'the confirmation')[1]
      const total = plan.spaces.length + plan.users.length + plan.items.length
      applyBulk(c, plan)
      const job = add(A(c).jobs, {
        id: newId(c.data, '01JB'),
        action: plan.action as never,
        total,
        done: total,
        failed: [],
        state: 'done' as const,
      })
      return reply(202, { job })
    },
  ],
  ['GET', '/api/admin/jobs/:id', (c) => ({ job: find(A(c).jobs, (j) => j.id === c.params.id, 'the job') })],
  ['GET', '/api/admin/exports', (c) => page(c, A(c).exports)],
  [
    'POST',
    '/api/admin/exports',
    (c) => {
      const me = signedIn(c.data)
      const ex = add(A(c).exports, {
        id: newId(c.data, '01EX'),
        tenant: DEFAULT_TENANT,
        madeBy: { id: me.id, username: me.username },
        spaces: [
          ...((c.body.spaces ?? []) as string[]).map((id) => ({
            space: id,
            name: spacesOf(c).find((s) => s.info.id === id)?.info.name ?? id,
            spaceId: id,
            open: true,
          })),
          ...((c.body.items ?? []) as string[]).map((id) => ({ item: id, name: id, spaceId: id, open: false })),
        ],
        history: !!c.body.history,
        state: 'ready' as const,
        warnings: [],
        bytes: 0,
        created: now(c),
        expires: rfc3339(c.now() + 7 * 86_400_000),
      })
      return reply(202, ex)
    },
  ],
  ['GET', '/api/admin/exports/:id', (c) => find(A(c).exports, (e) => e.id === c.params.id, 'the export')],
  [
    'DELETE',
    '/api/admin/exports/:id',
    (c) =>
      remove(
        A(c).exports,
        find(A(c).exports, (e) => e.id === c.params.id),
      ),
  ],

  // --- tenants, plans, settings, secrets ---
  [
    'GET',
    '/api/admin/tenants',
    (c) => {
      const rows = A(c)
        .tenants.filter((t) => has(`${t.name} ${t.slug}`, c.query.get('q')))
        .filter((t) => !c.query.get('origin') || t.origin === c.query.get('origin'))
        .map((t) => tenantView(c, t))
      return { ...page(c, rows), count: A(c).tenants.length }
    },
  ],
  [
    'POST',
    '/api/admin/tenants',
    (c) => {
      const at = now(c)
      const { admin, ...rest } = c.body
      const t = add(A(c).tenants, {
        id: newId(c.data, '01TN'),
        slug: rest.slug,
        name: rest.name,
        default: false,
        disabled: false,
        registration: { open: false },
        quota: { people: null, spaces: null, bytes: null, pages: null },
        plan: null,
        origin: 'admin' as const,
        createdBy: '',
        usage: { people: 0, spaces: 0, bytes: 0, pages: 0, measuredAt: null, over: [] },
        created: at,
        updated: at,
      })
      // A first administrator named: the invite they accept (the token is shown once).
      if (admin) {
        const inv = {
          id: newId(c.data, '01IV'),
          token: `invite-${t.id}`,
          role: 'admin' as const,
          created: at,
          expires: rfc3339(c.now() + 7 * 86_400_000),
          username: admin.username,
          you: false,
        }
        return reply(201, { ...t, invite: inv })
      }
      return reply(201, t)
    },
  ],
  [
    'GET',
    '/api/admin/tenants/:id',
    (c) =>
      tenantView(
        c,
        find(A(c).tenants, (t) => t.id === c.params.id, 'the tenant'),
      ),
  ],
  [
    'GET',
    '/api/admin/tenants/:id/overview',
    (c) => {
      const t = tenantView(
        c,
        find(A(c).tenants, (x) => x.id === c.params.id, 'the tenant'),
      )
      const admins = c.data.auth.users.filter((u) => u.role === 'admin' && (u.tenant?.id ?? DEFAULT_TENANT) === t.id)
      return {
        tenant: t,
        createdByName: '',
        groups: A(c).groups.length,
        admins: admins.map((u) => ({ ...who(u), siteAdmin: !!u.siteAdmin, disabled: u.disabled })),
        adminCount: admins.length,
        recentAdminActivity: [],
        featuresOff: Object.entries(c.data.features)
          .filter(([, on]) => !on)
          .map(([k]) => k),
      }
    },
  ],
  [
    'PATCH',
    '/api/admin/tenants/:id',
    (c) =>
      Object.assign(
        find(A(c).tenants, (t) => t.id === c.params.id, 'the tenant'),
        c.body,
        { updated: now(c) },
      ),
  ],
  [
    'DELETE',
    '/api/admin/tenants/:id',
    (c) =>
      remove(
        A(c).tenants,
        find(A(c).tenants, (t) => t.id === c.params.id, 'the tenant'),
      ),
  ],
  [
    'PUT',
    '/api/admin/tenants/:id/plan',
    (c) => {
      const t = find(A(c).tenants, (x) => x.id === c.params.id, 'the tenant')
      const plan = c.body.plan ? find(A(c).plans, (p) => p.slug === c.body.plan, 'the plan') : null
      t.plan = plan ? { slug: plan.slug, name: plan.name } : null
      return { plan: t.plan, cleared: [] }
    },
  ],
  [
    'GET',
    '/api/admin/tenants/:id/settings',
    (c) => ({
      settings: A(c)
        .settings.filter((s) => TENANT_KEYS.includes(s.key))
        .map((s) => tenantSetting(c, c.params.id, s)),
    }),
  ],
  [
    'PUT',
    '/api/admin/tenants/:id/settings/:key',
    (c) => {
      const def = siteOf(c, c.params.key)
      const rules = (A(c).tenantRules[c.params.id] ??= {})
      const own = (rules[def.key] ??= { value: null, lower: null, inEffect: true, siteLocked: false })
      const b = c.body
      if ('value' in b) own.value = b.value
      if ('lower' in b) own.lower = b.lower
      if ('siteLock' in b) {
        own.siteLocked = !!b.siteLock
        // The site's lock is a manual rule that forces the value.
        if (own.siteLocked)
          own.rules = [
            {
              source: 'manual',
              value: own.value,
              lower: { mode: 'lock' },
              inEffect: true,
              setBy: signedIn(c.data).username,
            },
          ]
        else delete own.rules
      }
      if ('siteRule' in b) {
        if (b.siteRule === null) {
          own.value = null
          delete own.rules
        } else {
          own.value = b.siteRule.value ?? null
          own.rules = [
            {
              source: 'manual',
              value: b.siteRule.value,
              lower: b.siteRule.lower ?? null,
              inEffect: true,
              setBy: signedIn(c.data).username,
              ...b.siteRule,
            },
          ]
        }
      }
      return tenantSetting(c, c.params.id, def)
    },
  ],
  ['GET', '/api/admin/tenants/:id/secrets', (c) => ({ secrets: Object.values(secretsOf(c, c.params.id)) })],
  [
    'PUT',
    '/api/admin/tenants/:id/secrets/:name',
    (c) => {
      secretsOf(c, c.params.id)[c.params.name] = { name: c.params.name, set: true, updatedAt: now(c) }
      return { secrets: Object.values(secretsOf(c, c.params.id)) }
    },
  ],
  [
    'DELETE',
    '/api/admin/tenants/:id/secrets/:name',
    (c) => {
      secretsOf(c, c.params.id)[c.params.name] = { name: c.params.name, set: false }
      return { secrets: Object.values(secretsOf(c, c.params.id)) }
    },
  ],
  ['GET', '/api/admin/secrets', (c) => ({ secrets: Object.values(secretsOf(c)) })],
  [
    'PUT',
    '/api/admin/secrets/:name',
    (c) => {
      secretsOf(c)[c.params.name] = { name: c.params.name, set: true, updatedAt: now(c) }
      return { secrets: Object.values(secretsOf(c)) }
    },
  ],
  [
    'DELETE',
    '/api/admin/secrets/:name',
    (c) => {
      secretsOf(c)[c.params.name] = { name: c.params.name, set: false }
      return { secrets: Object.values(secretsOf(c)) }
    },
  ],
  ['GET', '/api/admin/settings', (c) => ({ settings: A(c).settings })],
  [
    'PUT',
    '/api/admin/settings/:key',
    (c) => {
      const s = siteOf(c, c.params.key)
      if ('value' in c.body) s.site.value = c.body.value
      if ('lower' in c.body) s.site.lower = c.body.lower ?? { mode: 'allow' }
      // A feature's switch is what GET /api/settings tells the pages.
      if (typeof s.site.value === 'boolean' && s.key in c.data.features) c.data.features[s.key] = s.site.value
      return s
    },
  ],
  [
    'GET',
    '/api/admin/plans',
    (c) => ({
      plans: A(c).plans.map((p) => ({ ...p, tenants: A(c).tenants.filter((t) => t.plan?.slug === p.slug).length })),
    }),
  ],
  [
    'POST',
    '/api/admin/plans',
    (c) => {
      const { slug, name, description = '', rules = {} } = c.body
      return reply(201, add(A(c).plans, { slug, name, description, rules, tenants: 0 }))
    },
  ],
  [
    'PATCH',
    '/api/admin/plans/:slug',
    (c) => {
      const p = find(A(c).plans, (x) => x.slug === c.params.slug, 'the plan')
      const { rules, ...rest } = c.body
      Object.assign(p, rest)
      for (const [k, v] of Object.entries((rules ?? {}) as Record<string, unknown>)) {
        if (v === null) delete p.rules[k]
        else p.rules[k] = v as never
      }
      return {
        ...p,
        affected: A(c)
          .tenants.filter((t) => t.plan?.slug === p.slug)
          .map((t) => t.id),
      }
    },
  ],
  [
    'DELETE',
    '/api/admin/plans/:slug',
    (c) =>
      remove(
        A(c).plans,
        find(A(c).plans, (x) => x.slug === c.params.slug, 'the plan'),
      ),
  ],

  // --- groups ---
  [
    'GET',
    '/api/admin/groups',
    (c) => {
      const rows = A(c)
        .groups.filter((g) => has(g.name, c.query.get('q')))
        .filter((g) => !c.query.get('source') || g.source === c.query.get('source'))
        .filter((g) => !c.query.get('user') || g.members.includes(c.query.get('user')!))
        .map(groupView)
      return page(c, rows)
    },
  ],
  [
    'POST',
    '/api/admin/groups',
    (c) => {
      const at = now(c)
      const g = add(A(c).groups, {
        id: newId(c.data, '01GP'),
        name: c.body.name,
        description: c.body.description ?? '',
        source: c.body.source ?? 'local',
        people: 0,
        spaces: 0,
        mappedFrom: [],
        created: at,
        updated: at,
        members: [],
        spaceRefs: [],
      })
      return reply(201, groupView(g))
    },
  ],
  ['GET', '/api/admin/groups/:id', (c) => groupView(find(A(c).groups, (g) => g.id === c.params.id, 'the group'))],
  [
    'PATCH',
    '/api/admin/groups/:id',
    (c) =>
      groupView(
        Object.assign(
          find(A(c).groups, (g) => g.id === c.params.id, 'the group'),
          c.body,
          { updated: now(c) },
        ),
      ),
  ],
  [
    'DELETE',
    '/api/admin/groups/:id',
    (c) =>
      remove(
        A(c).groups,
        find(A(c).groups, (g) => g.id === c.params.id, 'the group'),
      ),
  ],
  [
    'GET',
    '/api/admin/groups/:id/members',
    (c) => {
      const g = find(A(c).groups, (x) => x.id === c.params.id, 'the group')
      const rows = g.members
        .map((id) => c.data.auth.users.find((u) => u.id === id))
        .filter((u): u is UserRec => !!u && has(`${u.username} ${u.name}`, c.query.get('q')))
        .map((u): GroupPerson => ({
          userId: u.id,
          username: u.username,
          name: u.name,
          disabled: u.disabled,
          from: [],
          added: g.created,
        }))
      return page(c, rows)
    },
  ],
  [
    'POST',
    '/api/admin/groups/:id/members',
    (c) => {
      const g = find(A(c).groups, (x) => x.id === c.params.id, 'the group')
      const ids = c.body.userIds as string[]
      const fresh = ids.filter((id) => !g.members.includes(id))
      g.members.push(...fresh)
      return { added: fresh.length, already: ids.length - fresh.length }
    },
  ],
  [
    'POST',
    '/api/admin/groups/:id/members/batch',
    (c) => {
      const g = find(A(c).groups, (x) => x.id === c.params.id, 'the group')
      const summary: Record<string, number> = {}
      const results = (c.body.rows as { user: string }[]).map((r, i) => {
        const u = c.data.auth.users.find((x) => x.id === r.user || x.username === r.user)
        const status = !u ? 'not_found' : g.members.includes(u.id) ? 'already_member' : 'ok'
        if (u && status === 'ok' && !c.body.dryRun) g.members.push(u.id)
        summary[status] = (summary[status] ?? 0) + 1
        return { row: i + 1, user: r.user, status, ...(u ? { userId: u.id, username: u.username, name: u.name } : {}) }
      })
      return { dryRun: !!c.body.dryRun, results, summary }
    },
  ],
  [
    'DELETE',
    '/api/admin/groups/:id/members/:user',
    (c) => {
      const g = find(A(c).groups, (x) => x.id === c.params.id, 'the group')
      g.members = g.members.filter((id) => id !== c.params.user)
    },
  ],
  [
    'GET',
    '/api/admin/groups/:id/spaces',
    (c) => page(c, find(A(c).groups, (x) => x.id === c.params.id, 'the group').spaceRefs),
  ],

  // --- mail ---
  ['GET', '/api/admin/mail', (c) => A(c).mail],
  [
    'PUT',
    '/api/admin/mail',
    (c) => {
      const { password, ...rest } = c.body
      Object.assign(A(c).mail, rest)
      if (password !== undefined) A(c).mail.hasPassword = password !== ''
      return A(c).mail
    },
  ],
  [
    'POST',
    '/api/admin/mail/test',
    (c) => {
      c.data.sent.mail.push({ to: c.body.to, kind: 'test' })
      return { ok: true }
    },
  ],

  // --- sign-in security and providers ---
  ['GET', '/api/admin/security', (c) => securityBody(c)],
  [
    'PUT',
    '/api/admin/security',
    (c) => {
      const cap = c.body.captcha ?? {}
      if ('siteKey' in cap) A(c).captcha.siteKey = cap.siteKey
      if ('secret' in cap) A(c).captcha.hasSecret = cap.secret !== ''
      return securityBody(c)
    },
  ],
  ['GET', '/api/admin/auth/providers', (c) => ({ providers: A(c).providers })],
  [
    'POST',
    '/api/admin/auth/providers',
    (c) => reply(201, add(A(c).providers, makeProvider(c.body, now(c), A(c).providers.length + 1))),
  ],
  [
    'PATCH',
    '/api/admin/auth/providers/:slug',
    (c) =>
      changeProvider(
        find(A(c).providers, (p) => p.slug === c.params.slug, 'the provider'),
        c.body,
        now(c),
      ),
  ],
  [
    'DELETE',
    '/api/admin/auth/providers/:slug',
    (c) =>
      remove(
        A(c).providers,
        find(A(c).providers, (p) => p.slug === c.params.slug, 'the provider'),
      ),
  ],
  [
    'POST',
    '/api/admin/auth/providers/:slug/refresh',
    (c) => {
      const p = find(A(c).providers, (x) => x.slug === c.params.slug, 'the provider')
      p.status = { ok: true, checked: now(c), message: '' }
      return p
    },
  ],

  // --- the audit log ---
  [
    'GET',
    '/api/admin/audit',
    (c) => {
      const cats = (c.query.get('category') ?? '').split(',').filter(Boolean)
      const rows = A(c)
        .audit.filter((a: AuditItem) => !cats.length || cats.includes(a.category))
        .filter((a) => !c.query.get('kind') || a.kind === c.query.get('kind'))
        .filter((a) => !c.query.get('user') || a.user === c.query.get('user'))
      return page(c, rows)
    },
  ],
  ['GET', '/api/admin/audit/kinds', () => ({ categories: AUDIT_CATEGORIES })],

  // --- machine access ---
  ['GET', '/api/admin/tokens', (c) => page(c, A(c).tokens)],
  [
    'DELETE',
    '/api/admin/tokens/:id',
    (c) => {
      const t = find(A(c).tokens, (x) => x.id === c.params.id, 'the token')
      t.state = 'revoked'
      t.revoked = now(c)
    },
  ],
  ['GET', '/api/admin/grants', (c) => page(c, A(c).grants)],
  [
    'DELETE',
    '/api/admin/grants/:id',
    (c) =>
      remove(
        A(c).grants,
        find(A(c).grants, (g) => g.id === c.params.id, 'the grant'),
      ),
  ],

  // --- the daily text of the server ---
  ['GET', '/api/admin/daily', (c) => dailyBody(c)],
  [
    'PUT',
    '/api/admin/daily/apis/:id',
    (c) => {
      const d = A(c).daily
      const at = d.apis.findIndex((a) => a.id === c.params.id)
      const api = {
        langMap: {},
        ...c.body,
        headers: ((c.body.headers ?? []) as { name: string }[]).map((h) => ({ name: h.name, set: true })),
      }
      if (at >= 0) d.apis[at] = api
      else d.apis.push(api)
      return dailyBody(c)
    },
  ],
  [
    'DELETE',
    '/api/admin/daily/apis/:id',
    (c) => {
      const d = A(c).daily
      d.apis = d.apis.filter((a) => a.id !== c.params.id)
      return dailyBody(c)
    },
  ],
  ['GET', '/api/admin/daily/lines', (c) => ({ lines: A(c).dailyLines[c.query.get('lang') ?? ''] ?? [] })],
  [
    'PUT',
    '/api/admin/daily/lines',
    (c) => {
      const d = A(c).daily
      const given = c.body.lines as { text: string; author?: string; source?: string }[] | undefined
      const lines = given
        ? given.map((l) => ({ text: l.text, author: l.author ?? '', source: l.source ?? '' }))
        : linesOf(c.body.text ?? '')
      A(c).dailyLines[c.body.lang] = lines
      d.lines = countLines(A(c).dailyLines, d.lines.max)
      return dailyBody(c)
    },
  ],
  ['POST', '/api/admin/daily/lines/preview', (c) => ({ lines: linesOf(c.body.text ?? '') })],
  [
    'DELETE',
    '/api/admin/daily/lines',
    (c) => {
      const d = A(c).daily
      delete A(c).dailyLines[c.query.get('lang') ?? '']
      d.lines = countLines(A(c).dailyLines, d.lines.max)
      return dailyBody(c)
    },
  ],
  ['POST', '/api/admin/daily/try', (c) => ({ ok: true, message: '', url: c.body.api?.url ?? '' })],
  [
    'PUT',
    '/api/admin/daily/providers/:id',
    (c) => {
      const p = find(A(c).daily.providers, (x) => x.id === c.params.id, 'the provider')
      Object.assign(p, c.body)
      return dailyBody(c)
    },
  ],
]

// A line of text, with who said it after a tab; blank lines are not lines.
const linesOf = (text: string): { text: string; author: string; source: string }[] =>
  text
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map((l) => {
      const [t, author] = l.split('\t')
      return { text: t, author: author ?? '', source: '' }
    })
const countLines = (byLang: Record<string, unknown[]>, max: number) => ({
  total: Object.values(byLang).reduce((n, l) => n + l.length, 0),
  byLang: Object.fromEntries(Object.entries(byLang).map(([k, v]) => [k, v.length])),
  max,
})

// What the sign-in page tells the console: the mode that works now (a single-sign-on mode with no provider to sign in
// through falls back to the password), who must have a passkey, and the keys of the captcha.
function securityBody(c: Ctx) {
  const mode = siteOf(c, 'signin.mode').site.value as string
  const providers = A(c).providers.filter((p) => p.enabled).length
  const required = c.data.auth.users.filter((u) => u.passkeyRequired)
  return {
    login: {
      effectiveMode: mode !== 'dual' && providers === 0 ? 'local_only' : mode,
      ssoOnlyInEffect: false,
      ssoAvailable: providers > 0,
    },
    passkeys: {
      available: A(c).security.passkeysAvailable,
      required: required.length,
      requiredWithout: required.filter((u) => !(u.passkeys ?? person(c, u.id).passkeys.length)).length,
    },
    captcha: A(c).captcha,
    passwordAdmins: c.data.auth.users.filter((u) => u.role === 'admin').length,
    notices: A(c).security.notices,
  }
}

// The daily-text page's body: the interfaces people saved, and each built-in provider that is switched on, which the
// page lists among them (and cannot edit).
function dailyBody(c: Ctx) {
  const d = A(c).daily
  const builtin = d.providers
    .filter((p) => p.enabled)
    .map((p): DailyApi => ({
      id: p.id,
      name: p.name,
      lang: p.languages[0] ?? '',
      url: '',
      headers: [],
      text: '',
      source: '',
      author: '',
      link: '',
      refresh: p.refresh,
      refreshMinutes: p.minutes,
      skipNew: p.skipNew,
      rps: p.rps,
      enabled: true,
      builtin: true,
      languages: p.languages,
    }))
  return { ...d, apis: [...d.apis, ...builtin] }
}

function groupView({ members, spaceRefs, ...g }: GroupRec): Group {
  return { ...g, people: members.length, spaces: spaceRefs.length }
}

function storagePerson(c: Ctx, u: UserRec): StoragePerson {
  return {
    user: { ...who(u), disabled: u.disabled },
    tenant: u.tenant?.id ?? DEFAULT_TENANT,
    created: u.created,
    lastActive: u.created,
    firstWrite: null,
    dormant: false,
    connected: false,
    spaces: spacesOf(c).length,
    remoteSpaces: 0,
    evictedSpaces: 0,
    pinnedSpaces: 0,
    graceSpaces: 0,
    overdueSpaces: 0,
    usageBytes: 0,
    heldBytes: 0,
    measuredAt: null,
  }
}
