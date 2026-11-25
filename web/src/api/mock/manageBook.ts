// The mock's side of milestone 3.5 (docs/api.md, "Storage management"): where each space's bytes live, the summary,
// the lists of spaces and people, the recycle bin, what deleting a space or a person would do, the confirmation
// token and the bulk jobs, the exports and the audited view of a space. The mock has one tenant, so the difference
// it keeps is the one between a site administrator and a tenant administrator: a tenant administrator is never
// shown a personal space, the disk's free room, nor can purge early. Time moves one step each time a running job,
// a purge or an export is asked about, so a test sees the states one after another.

import type { AdminSpace, CacheState, RestoreSkipped, SpaceLocation, SpaceRole } from '../types.ts'
import { HttpError } from './store.ts'
import type { MockResponse } from './server.ts'

// What the book needs to know of a space and of a person.
export interface BookSpace {
  id: string
  name: string
  personal: boolean
  created: number
  disabled: boolean
  members: Map<string, { role: SpaceRole; joined: number }>
  groups: Map<string, SpaceRole>
  size: { bytes: number; at: number; pages: number } | null
  remote: { url: string; credential: { type: string } } | null
  cache: 'full' | 'partial' | 'evicted'
  pinned: boolean
  exempt: boolean
  lastUsed: number
  assets: { url: string; cacheBytes: number } | null
  reachable: boolean
  store: { draftCount: () => number; ids: () => string[] }
  // Mock only: where the bytes live when it is not what the remote says.
  location?: SpaceLocation
}
export interface BookUser {
  id: string
  username: string
  name: string
  role: 'admin' | 'member'
  siteAdmin?: boolean
  disabled: boolean
  created: string
}

export interface ManageBookOptions<S extends BookSpace, U extends BookUser> {
  now: () => number
  rfc3339: (ms: number) => string
  tenant: string
  spaces: Map<string, S>
  users: Map<string, U>
  newId: (taken: (id: string) => boolean) => string
  token: () => string
  adminItem: (s: S) => Omit<AdminSpace, 'location' | 'counted' | 'view'>
  ownerOf: (s: S) => U | undefined
  isSiteAdmin: (u: U | undefined) => boolean
  cacheOf: (s: S) => CacheState
  notice: (s: S) => { overdue: boolean } | null
  dropSessions: (userId: string) => number
  accessOf: (userId: string) => {
    tokens: number
    grants: number
    passkeys: number
    identities: number
  }
  groupCount: (userId: string) => number
  // The first request after a deletion: the person's sessions are over.
  recycleDays: () => number
}

type Action =
  | 'delete_spaces'
  | 'delete_users'
  | 'purge'
  | 'restore'
  | 'evict'
  | 'pin'
  | 'unpin'
  | 'disable_spaces'
  | 'enable_spaces'
  | 'disable_users'
  | 'enable_users'
  | 'export'
const ACTIONS: Record<Action, 'spaces' | 'users' | 'items' | 'spaces+items'> = {
  delete_spaces: 'spaces',
  delete_users: 'users',
  purge: 'items',
  restore: 'items',
  evict: 'spaces',
  pin: 'spaces',
  unpin: 'spaces',
  disable_spaces: 'spaces',
  enable_spaces: 'spaces',
  disable_users: 'users',
  enable_users: 'users',
  export: 'spaces+items',
}
const MAX_IDS = 500
const DAY = 86_400_000

const bad = (message: string): never => {
  throw new HttpError(400, 'invalid', message)
}
const gone = (what: string): never => {
  throw new HttpError(404, 'not_found', what)
}
const sum = <T>(list: T[], f: (x: T) => number) => list.reduce((n, x) => n + f(x), 0)
const strs = (v: unknown, name: string): string[] => {
  if (v === undefined || v === null) return []
  if (!Array.isArray(v) || v.some((x) => typeof x !== 'string')) return bad(`${name} is a list of ids`)
  return v as string[]
}

interface ItemRec<S, U> {
  id: string
  kind: 'space' | 'user'
  space?: S
  user?: U
  // The items recycled with a person.
  parent?: string
  wasDisabled?: boolean
  // The transfers and the memberships left when the person was deleted, for the answer of a restore.
  transfers: { space: string; name: string; to: string; toUsername: string }[]
  left: { space: string; name: string; role: string }[]
  bytes: number
  deletedAt: number
  deletedBy: { id: string; username: string }
  reason: string
  // purging: the item is going; `ticks` counts the times it was looked at since.
  purging: number | null
}
interface ConfirmRec {
  by: string
  action: Action
  req: {
    spaces: string[]
    users: string[]
    items: string[]
    heir: string
    transfers: Record<string, string>
  }
  hash: string
  expires: number
}
interface JobRec {
  id: string
  by: string
  action: Action
  req: ConfirmRec['req']
  ids: string[]
  done: number
  failed: { id: string; error: string; code: string }[]
  state: 'queued' | 'running' | 'done' | 'interrupted' | 'failed'
  site: boolean
}
interface ExportRec2 {
  id: string
  by: string
  siteOnly: boolean
  sources: { space?: string; item?: string; name: string; spaceId: string }[]
  history: boolean
  state: 'queued' | 'running' | 'ready' | 'failed'
  error: string
  warnings: string[]
  bytes: number
  created: number
  expires: number
  looks: number
}

export function createManageBook<S extends BookSpace, U extends BookUser>(o: ManageBookOptions<S, U>) {
  const items = new Map<string, ItemRec<S, U>>()
  const confirms = new Map<string, ConfirmRec>()
  const jobs = new Map<string, JobRec>()
  const exportsMap = new Map<string, ExportRec2>()
  // (session, space) -> when the view started and when it ends.
  const views = new Map<string, { since: number; until: number }>()
  const activity = new Map<string, { lastActive?: number; firstWrite?: number }>()
  const busy = new Set<string>()
  const hooks = { holdJobs: false, exportsLimit: Infinity, breakJobs: false }

  // --- who sees what ---
  const siteOf = (me: U) => o.isSiteAdmin(me)
  const seesSpace = (me: U, s: S) => siteOf(me) || !s.personal
  const spaceFor = (me: U, id: string): S => {
    const s = o.spaces.get(id)
    if (!s || !seesSpace(me, s)) return gone('no such space')
    return s
  }
  const userFor = (id: string): U => {
    const u = o.users.get(id)
    if (!u) throw new HttpError(404, 'user_not_found', 'there is no such user')
    return u
  }

  // --- where the bytes live ---
  const locationOf = (s: S): SpaceLocation => s.location ?? (s.remote ? 'own' : 'server')
  const counted = (s: S) => locationOf(s) === 'server' || locationOf(s) === 'site'
  const viewOf = (token: string | null, space: string): AdminSpace['view'] => {
    const v = views.get(viewKey(token ?? '', space))
    return v && v.until > o.now() ? { since: o.rfc3339(v.since), until: o.rfc3339(v.until) } : null
  }
  const decorate = (s: S, token: string | null): AdminSpace => ({
    ...o.adminItem(s),
    location: locationOf(s),
    counted: counted(s),
    view: viewOf(token, s.id),
  })
  const heldOf = (s: S) =>
    (s.remote && s.cache === 'evicted' ? 0 : (s.size?.bytes ?? 0)) + (s.size ? Math.round(s.size.bytes / 8) : 0)
  const bytesOf = (s: S) => s.size?.bytes ?? 0

  // --- activity ---
  const touch = (userId: string) => {
    const a = activity.get(userId) ?? {}
    // At most once an hour, as the server writes it.
    if (!a.lastActive || o.now() - a.lastActive > 3_600_000) a.lastActive = o.now()
    activity.set(userId, a)
  }
  const firstWriteOf = (u: U): number | null => {
    const seeded = activity.get(u.id)?.firstWrite
    if (seeded) return seeded
    const wrote = [...o.spaces.values()].some((s) => o.ownerOf(s)?.id === u.id && s.store.ids().length > 0)
    return wrote ? Date.parse(u.created) : null
  }

  // --- the paging every list shares: `<sort>.<order>:<value>:<id>` ---
  function paged<T>(
    all: T[],
    idOf: (x: T) => string,
    valueOf: (x: T) => string | number,
    tag: string,
    cursor: string | null,
    limit: number,
  ) {
    let from = 0
    if (cursor) {
      const [t] = cursor.split(':')
      if (t !== tag) bad('that cursor is not one this list gave')
      const id = cursor.slice(cursor.lastIndexOf(':') + 1)
      const at = all.findIndex((x) => idOf(x) === id)
      from = at < 0 ? 0 : at + 1
    }
    const page = all.slice(from, from + limit)
    const last = page[page.length - 1]
    return {
      page,
      next: from + limit < all.length && last ? `${tag}:${valueOf(last)}:${idOf(last)}` : '',
    }
  }
  const limitOf = (url: URL) => {
    const raw = url.searchParams.get('limit')
    if (raw === null || raw === '') return 50
    const n = Number(raw)
    if (!Number.isInteger(n) || n < 1 || n > 200) bad('limit is a number from 1 to 200')
    return n
  }
  const oneOf = (url: URL, name: string, allowed: string[]): string => {
    const v = url.searchParams.get(name) ?? ''
    if (v !== '' && !allowed.includes(v)) bad(`${name} is one of ${allowed.join(', ')}`)
    return v
  }
  const flag = (url: URL, name: string): boolean => {
    const v = url.searchParams.get(name)
    if (v === null || v === '' || v === '0') return false
    if (v !== '1') bad(`${name} is 1 or 0`)
    return true
  }
  // `?tenant=`: a site administrator may name the tenant there is; for anyone else it is one that does not exist.
  const tenantParam = (me: U, url: URL) => {
    const t = url.searchParams.get('tenant')
    if (t === null || t === '') return
    if (!siteOf(me) || t !== o.tenant) gone('no such tenant')
  }
  const days0 = (s: S) => (o.now() - s.lastUsed) / DAY

  // --- the summary ---
  const summary = (me: U) => {
    const all = [...o.spaces.values()]
    const n = (f: (s: S) => boolean) => all.filter(f).length
    const people = [...o.users.values()]
    const dormant = people.filter(
      (u) => !firstWriteOf(u) && ![...all].some((s) => s.remote && o.ownerOf(s)?.id === u.id),
    ).length
    const bin = [...items.values()].filter((i) => !i.parent)
    const ex = [...exportsMap.values()].filter((e) => siteOf(me) || !e.siteOnly)
    const notices = all.map((s) => o.notice(s)).filter((x) => x !== null)
    return {
      tenant: null as string | null,
      spaces: {
        local: n((s) => !s.remote),
        full: n((s) => !!s.remote && s.cache === 'full'),
        partial: n((s) => !!s.remote && s.cache === 'partial'),
        evicted: n((s) => !!s.remote && s.cache === 'evicted'),
        archived: 0,
        restoring: 0,
      },
      people: people.length,
      dormant,
      heldBytes: sum(all, heldOf),
      usageBytes: sum(all.filter(counted), bytesOf),
      assetCacheBytes: sum(all, (s) => s.assets?.cacheBytes ?? 0),
      recycle: { items: bin.length, bytes: sum(bin, (i) => i.bytes) },
      recycleDays: o.recycleDays(),
      exports: { items: ex.length, bytes: sum(ex, (e) => e.bytes) },
      remotes: {
        inGrace: notices.filter((x) => !x.overdue).length,
        overdue: notices.filter((x) => x.overdue).length,
      },
      ...(siteOf(me) ? { diskFreeBytes: 50 * 1024 ** 3 } : {}),
      measuredAt: o.rfc3339(o.now()),
    }
  }

  // --- the list of spaces ---
  const SPACE_SORTS = ['created', 'name', 'bytes', 'lastUsed']
  const listSpaces = (me: U, token: string | null, url: URL): MockResponse => {
    const limit = limitOf(url)
    tenantParam(me, url)
    const sort = oneOf(url, 'sort', SPACE_SORTS) || 'created'
    const order = oneOf(url, 'order', ['asc', 'desc']) || (sort === 'created' || sort === 'name' ? 'asc' : 'desc')
    const cache = oneOf(url, 'cache', ['local', 'full', 'partial', 'evicted', 'archived', 'restoring'])
    const remote = oneOf(url, 'remote', ['none', 'ok', 'offline', 'error'])
    const deadline = oneOf(url, 'deadline', ['grace', 'overdue'])
    const overdue = flag(url, 'overdue')
    const pinned = flag(url, 'pinned')
    const exempt = flag(url, 'exempt')
    const owner = url.searchParams.get('owner') ?? ''
    const idle = url.searchParams.get('idleDays')
    if (idle !== null && idle !== '' && (!Number.isInteger(Number(idle)) || Number(idle) < 1))
      bad('idleDays is a number of days')
    const needle = (url.searchParams.get('q') ?? '').toLowerCase()
    const value = (s: S): number | string =>
      sort === 'name'
        ? s.name.toLowerCase()
        : sort === 'bytes'
          ? (s.size?.bytes ?? -1)
          : sort === 'lastUsed'
            ? s.lastUsed
            : s.created
    const rows = [...o.spaces.values()]
      .filter((s) => seesSpace(me, s))
      .filter((s) => needle === '' || s.name.toLowerCase().includes(needle))
      .filter((s) => cache === '' || o.cacheOf(s) === cache)
      .filter(
        (s) =>
          remote === '' ||
          (remote === 'none' ? !s.remote : !!s.remote && (remote === 'ok' ? s.reachable : !s.reachable)),
      )
      .filter((s) => deadline === '' || o.notice(s)?.overdue === (deadline === 'overdue'))
      .filter((s) => !overdue || o.notice(s)?.overdue === true)
      .filter((s) => !pinned || s.pinned)
      .filter((s) => !exempt || s.exempt)
      .filter((s) => owner === '' || o.ownerOf(s)?.id === owner)
      .filter((s) => !idle || days0(s) >= Number(idle))
    // Spaces with no value of the sort (never measured) come after the others.
    const none = (s: S) => sort === 'bytes' && s.size === null
    rows.sort((a, b) => {
      if (none(a) !== none(b)) return none(a) ? 1 : -1
      const x = value(a)
      const y = value(b)
      const c = x < y ? -1 : x > y ? 1 : 0
      return (order === 'asc' ? c : -c) || (a.id < b.id ? -1 : 1)
    })
    const { page, next } = paged(rows, (s) => s.id, value, `${sort}.${order}`, url.searchParams.get('cursor'), limit)
    return { status: 200, body: { items: page.map((s) => decorate(s, token)), next } }
  }

  // --- people ---
  const PEOPLE_SORTS = ['held', 'bytes', 'lastActive', 'created', 'spaces']
  const ownedBy = (u: U) => [...o.spaces.values()].filter((s) => o.ownerOf(s)?.id === u.id)
  const personRow = (u: U) => {
    const mine = ownedBy(u)
    const notices = mine.map((s) => o.notice(s))
    const a = activity.get(u.id)
    const fw = firstWriteOf(u)
    const connected = mine.some((s) => s.remote)
    return {
      user: {
        id: u.id,
        username: u.username,
        name: u.name,
        disabled: u.disabled,
      },
      tenant: o.tenant,
      created: u.created,
      lastActive: a?.lastActive ? o.rfc3339(a.lastActive) : null,
      firstWrite: fw ? o.rfc3339(fw) : null,
      dormant: !fw && !mine.some((s) => s.remote) && !connected,
      connected,
      spaces: mine.length,
      remoteSpaces: mine.filter((s) => s.remote).length,
      evictedSpaces: mine.filter((s) => s.remote && s.cache === 'evicted').length,
      pinnedSpaces: mine.filter((s) => s.pinned).length,
      graceSpaces: notices.filter((x) => x && !x.overdue).length,
      overdueSpaces: notices.filter((x) => x?.overdue).length,
      usageBytes: sum(mine.filter(counted), bytesOf),
      heldBytes: sum(mine, heldOf),
      measuredAt: mine.some((s) => s.size) ? o.rfc3339(o.now()) : null,
    }
  }
  const listPeople = (me: U, url: URL): MockResponse => {
    const limit = limitOf(url)
    tenantParam(me, url)
    const sort = oneOf(url, 'sort', PEOPLE_SORTS) || 'held'
    const order = oneOf(url, 'order', ['asc', 'desc']) || (sort === 'created' ? 'asc' : 'desc')
    const dormant = flag(url, 'dormant')
    const remote = oneOf(url, 'remote', ['none', 'some'])
    const deadline = oneOf(url, 'deadline', ['grace', 'overdue'])
    const dis = oneOf(url, 'disabled', ['0', '1'])
    const inactive = url.searchParams.get('inactiveDays')
    if (inactive !== null && inactive !== '' && (!Number.isInteger(Number(inactive)) || Number(inactive) < 1))
      bad('inactiveDays is a number of days')
    const needle = (url.searchParams.get('q') ?? '').toLowerCase()
    const rows = [...o.users.values()]
      .map(personRow)
      .filter(
        (p) =>
          needle === '' || p.user.username.toLowerCase().includes(needle) || p.user.name.toLowerCase().includes(needle),
      )
      .filter((p) => !dormant || p.dormant)
      .filter((p) => remote === '' || (remote === 'none' ? p.remoteSpaces === 0 : p.remoteSpaces > 0))
      .filter((p) => deadline === '' || (deadline === 'grace' ? p.graceSpaces > 0 : p.overdueSpaces > 0))
      .filter((p) => dis === '' || p.user.disabled === (dis === '1'))
      .filter((p) => !inactive || !p.lastActive || (o.now() - Date.parse(p.lastActive)) / DAY >= Number(inactive))
    const value = (p: ReturnType<typeof personRow>): number | string =>
      sort === 'held'
        ? p.heldBytes
        : sort === 'bytes'
          ? p.usageBytes
          : sort === 'lastActive'
            ? p.lastActive
              ? Date.parse(p.lastActive)
              : -1
            : sort === 'created'
              ? Date.parse(p.created)
              : p.spaces
    rows.sort((a, b) => {
      // People who were never active come last in both orders.
      if (sort === 'lastActive' && (a.lastActive === null) !== (b.lastActive === null))
        return a.lastActive === null ? 1 : -1
      const x = value(a)
      const y = value(b)
      const c = x < y ? -1 : x > y ? 1 : 0
      return (order === 'asc' ? c : -c) || (a.user.id < b.user.id ? -1 : 1)
    })
    const { page, next } = paged(
      rows,
      (p) => p.user.id,
      value,
      `${sort}.${order}`,
      url.searchParams.get('cursor'),
      limit,
    )
    return { status: 200, body: { items: page, next } }
  }
  const onePerson = (me: U, token: string | null, id: string): MockResponse => {
    const u = userFor(id)
    // Their personal space is counted in their row, and never shown to a tenant administrator.
    const spaces = ownedBy(u)
      .filter((s) => seesSpace(me, s))
      .sort((a, b) => b.lastUsed - a.lastUsed)
      .slice(0, 200)
    return {
      status: 200,
      body: { ...personRow(u), items: spaces.map((s) => decorate(s, token)) },
    }
  }

  // --- the recycle bin ---
  const childrenOf = (id: string) => [...items.values()].filter((i) => i.parent === id)
  const itemName = (i: ItemRec<S, U>) => (i.kind === 'user' ? i.user!.username : i.space!.name)
  const itemOut = (me: U, i: ItemRec<S, U>) => {
    const kids = childrenOf(i.id).filter((k) => siteOf(me) || !k.space?.personal)
    const at = i.deletedAt
    return {
      id: i.id,
      kind: i.kind,
      tenant: o.tenant,
      space:
        i.kind === 'space'
          ? {
              id: i.space!.id,
              name: i.space!.name,
              personal: i.space!.personal,
            }
          : null,
      user: i.kind === 'user' ? { id: i.user!.id, username: i.user!.username } : null,
      with: kids.map((k) => ({
        id: k.id,
        space: {
          id: k.space!.id,
          name: k.space!.name,
          personal: k.space!.personal,
        },
        bytes: k.bytes,
      })),
      bytes: i.bytes,
      deletedAt: o.rfc3339(at),
      deletedBy: i.deletedBy,
      purgeAt: o.rfc3339(at + o.recycleDays() * DAY),
      reason: i.reason,
      state: i.purging === null ? 'ready' : 'purging',
      restorable: i.purging === null,
      problems: i.purging === null ? [] : ['the item is not at rest (purging)'],
    }
  }
  const itemFor = (me: U, id: string): ItemRec<S, U> => {
    const i = items.get(id)
    // A personal space is the person's: a tenant administrator is told it does not exist.
    if (
      !i ||
      (i.kind === 'space' && i.space!.personal && !siteOf(me)) ||
      (i.parent && !siteOf(me) && i.space?.personal)
    )
      return gone('no such item in the recycle bin')
    return i
  }
  // A purge goes on while it is asked about, one step each time.
  const advancePurges = () => {
    for (const i of [...items.values()]) {
      if (i.purging === null) continue
      if (i.purging >= 1) {
        for (const k of childrenOf(i.id)) items.delete(k.id)
        items.delete(i.id)
      } else i.purging++
    }
  }
  const listRecycle = (me: U, url: URL): MockResponse => {
    advancePurges()
    const limit = limitOf(url)
    tenantParam(me, url)
    const kind = oneOf(url, 'kind', ['space', 'user'])
    const needle = (url.searchParams.get('q') ?? '').toLowerCase()
    const rows = [...items.values()]
      .filter((i) => !i.parent)
      .filter((i) => i.kind !== 'space' || !i.space!.personal || siteOf(me))
      .filter((i) => kind === '' || i.kind === kind)
      .filter((i) => needle === '' || itemName(i).toLowerCase().includes(needle))
      .sort((a, b) => b.deletedAt - a.deletedAt || (a.id < b.id ? 1 : -1))
    const { page, next } = paged(
      rows,
      (i) => i.id,
      (i) => i.deletedAt,
      'recycle',
      url.searchParams.get('cursor'),
      limit,
    )
    return {
      status: 200,
      body: { items: page.map((i) => itemOut(me, i)), next },
    }
  }

  const memberIds = (s: S) => [...s.members.keys()].sort()
  const recycleSpace = (s: S, by: U, reason: string, parent?: string): ItemRec<S, U> => {
    o.spaces.delete(s.id)
    const rec: ItemRec<S, U> = {
      id: o.newId((x) => items.has(x)),
      kind: 'space',
      space: s,
      parent,
      transfers: [],
      left: [],
      bytes: heldOf(s),
      deletedAt: o.now(),
      deletedBy: { id: by.id, username: by.username },
      reason,
      purging: null,
    }
    items.set(rec.id, rec)
    return rec
  }
  // What a deletion of this space is refused for.
  const checkDeletable = (s: S) => {
    if (s.personal)
      throw new HttpError(
        409,
        'personal_space',
        'a personal space belongs to one person and cannot be transferred or deleted',
      )
    if (busy.has(s.id)) throw new HttpError(409, 'busy', 'this space is in use now; try again in a moment')
  }
  const deleteSpace = (me: U, id: string, reason: string, parent?: string) => {
    const s = spaceFor(me, id)
    checkDeletable(s)
    return recycleSpace(s, me, reason, parent)
  }

  // The name a person had when they were deleted, as the server kept it with the space.
  const nameOfGone = (userId: string): string => {
    for (const i of items.values())
      if (i.kind === 'user' && i.user?.id === userId) return i.user.name || i.user.username
    return ''
  }
  const restoreSpace = (me: U, i: ItemRec<S, U>, ownerId: string, parentUser?: boolean): MockResponse => {
    const s = i.space!
    if (i.purging !== null) throw new HttpError(409, 'busy', 'a purge of this item is running')
    if (s.personal && !parentUser && ![...o.users.values()].some((u) => [...s.members.keys()].includes(u.id)))
      throw new HttpError(409, 'conflict', 'a personal space goes back with its person')
    if (o.spaces.has(s.id)) throw new HttpError(409, 'conflict', 'a space with that id exists again')
    // What is left out is worked out first, and applied only when the restore goes through.
    const skipped: RestoreSkipped[] = []
    const gone = [...s.members.keys()].filter((uid) => !o.users.has(uid))
    for (const uid of gone) skipped.push({ user: uid, name: nameOfGone(uid), reason: 'deleted' })
    const remaining = [...s.members].filter(([uid]) => o.users.has(uid))
    if (ownerId) {
      const u = o.users.get(ownerId)
      if (!u || u.disabled) throw new HttpError(404, 'user_not_found', 'there is no such user')
    } else if (!remaining.some(([, m]) => m.role === 'owner')) {
      throw new HttpError(409, 'owner_required', 'every owner of this space is gone: name one')
    }
    for (const uid of gone) s.members.delete(uid)
    if (ownerId) {
      for (const m of s.members.values()) if (m.role === 'owner') m.role = 'editor'
      const had = s.members.get(ownerId)
      if (had) had.role = 'owner'
      else s.members.set(ownerId, { role: 'owner', joined: o.now() })
    }
    o.spaces.set(s.id, s)
    items.delete(i.id)
    void me
    return {
      status: 200,
      body: {
        restored: { item: i.id, space: { id: s.id, name: s.name } },
        skipped,
      },
    }
  }
  const restoreUser = (me: U, i: ItemRec<S, U>): MockResponse => {
    if (i.purging !== null) throw new HttpError(409, 'busy', 'a purge of this item is running')
    const u = i.user!
    u.disabled = i.wasDisabled === true
    o.users.set(u.id, u)
    const skipped: RestoreSkipped[] = []
    const back: string[] = []
    for (const k of childrenOf(i.id)) {
      const s = k.space!
      if (o.spaces.has(s.id)) {
        k.parent = undefined
        skipped.push({ space: s.id, name: s.name, reason: 'conflict' })
        continue
      }
      for (const [uid] of [...s.members]) if (!o.users.has(uid)) s.members.delete(uid)
      o.spaces.set(s.id, s)
      items.delete(k.id)
      back.push(s.id)
    }
    items.delete(i.id)
    void me
    return {
      status: 200,
      body: {
        restored: {
          item: i.id,
          user: {
            id: u.id,
            username: u.username,
            name: u.name,
            role: u.role,
            disabled: u.disabled,
          },
          spaces: back,
        },
        skipped,
        transfers: i.transfers,
        left: i.left,
      },
    }
  }
  const restoreItem = (me: U, id: string, ownerId: string) => {
    const i = itemFor(me, id)
    return i.kind === 'user' ? restoreUser(me, i) : restoreSpace(me, i, ownerId)
  }
  const purgeItem = (me: U, id: string) => {
    if (!siteOf(me))
      throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may purge the recycle bin early')
    const i = itemFor(me, id)
    if (i.purging === null) i.purging = 0
    return i
  }

  // --- what deleting a space or a person would do ---
  const spacePreview = (me: U, id: string) => {
    const s = spaceFor(me, id)
    return {
      space: { id: s.id, name: s.name },
      pages: s.size?.pages ?? s.store.ids().length,
      bytes: bytesOf(s),
      heldBytes: heldOf(s),
      attachments: { inSpace: null, repository: !!s.assets },
      members: s.members.size,
      groups: s.groups.size,
      drafts: s.store.draftCount(),
      remote: { url: s.remote?.url ?? '', kept: true },
      assetsRemote: { url: s.assets?.url ?? '', kept: true },
      cache: o.cacheOf(s),
      exportable: !(o.cacheOf(s) === 'evicted' && !s.reachable),
      recycleDays: o.recycleDays(),
    }
  }
  const spaceRef = (s: S) => ({
    id: s.id,
    name: s.name,
    ...(s.personal ? { personal: true } : {}),
  })
  const ownerCount = (s: S) => [...s.members.values()].filter((m) => m.role === 'owner').length
  const userPlan = (me: U, id: string, heir: string, transfers: Record<string, string>) => {
    const u = userFor(id)
    if (u.siteAdmin !== false && u.role === 'admin' && !siteOf(me))
      throw new HttpError(403, 'forbidden', 'only a site administrator may delete a site administrator')
    const mine = [...o.spaces.values()].filter((s) => s.members.has(u.id))
    const recycle: { space: ReturnType<typeof spaceRef>; bytes: number }[] = []
    const transfer: {
      space: ReturnType<typeof spaceRef>
      to: { id: string; username: string }
      why: string
      credential?: string
    }[] = []
    const leave: { space: ReturnType<typeof spaceRef>; role: string }[] = []
    const needsHeir: ReturnType<typeof spaceRef>[] = []
    for (const s of mine) {
      const role = s.members.get(u.id)!.role
      const aloneOwner = role === 'owner' && ownerCount(s) === 1
      if (s.personal && role === 'owner') recycle.push({ space: spaceRef(s), bytes: heldOf(s) })
      else if (aloneOwner && s.members.size === 1 && s.groups.size === 0)
        recycle.push({ space: spaceRef(s), bytes: heldOf(s) })
      else if (aloneOwner) {
        const named = transfers[s.id]
        const editor = [...s.members]
          .filter(([uid, m]) => uid !== u.id && m.role === 'editor' && o.users.has(uid))
          .sort((a, b) => a[1].joined - b[1].joined)[0]?.[0]
        const to = named || editor || heir
        const why = named ? 'named' : editor ? 'editor' : 'heir'
        const target = to ? o.users.get(to) : undefined
        if (!target) needsHeir.push(spaceRef(s))
        else
          transfer.push({
            space: spaceRef(s),
            to: { id: target.id, username: target.username },
            why,
            ...(s.remote?.credential.type === 'github' ? { credential: 'user' } : {}),
          })
      } else leave.push({ space: spaceRef(s), role })
    }
    const access = o.accessOf(u.id)
    const blockers: { reason: string; message: string }[] = []
    if (u.id === me.id)
      blockers.push({
        reason: 'self',
        message: 'you cannot delete your own account',
      })
    const enabledSite = [...o.users.values()].filter((x) => x.role === 'admin' && x.siteAdmin !== false && !x.disabled)
    if (o.isSiteAdmin(u) && !u.disabled && enabledSite.length <= 1)
      blockers.push({
        reason: 'last_admin',
        message: 'there must be at least one enabled administrator',
      })
    return {
      user: { id: u.id, username: u.username },
      recycle,
      transfer,
      leave,
      revoke: { ...access, github: false, sessions: 1, connections: [] },
      groups: o.groupCount(u.id),
      needsHeir,
      blockers,
      bytes: sum(recycle, (r) => r.bytes),
      recycleDays: o.recycleDays(),
    }
  }
  const deleteUser = (
    me: U,
    id: string,
    heir: string,
    transfers: Record<string, string>,
    reason: string,
  ): ItemRec<S, U> => {
    const plan = userPlan(me, id, heir, transfers)
    if (plan.blockers.length > 0) throw new HttpError(409, 'conflict', plan.blockers[0].message)
    if (plan.needsHeir.length > 0)
      throw new HttpError(
        409,
        'heir_required',
        'the person owns shared spaces alone and nobody can be chosen to inherit them: name an heir, or one for each of these spaces',
        { spaces: plan.needsHeir.map((s) => ({ id: s.id, name: s.name })) },
      )
    for (const v of [heir, ...Object.values(transfers)])
      if (v && (!o.users.has(v) || v === id)) throw new HttpError(404, 'user_not_found', 'there is no such user')
    const u = userFor(id)
    const rec: ItemRec<S, U> = {
      id: o.newId((x) => items.has(x)),
      kind: 'user',
      user: u,
      wasDisabled: u.disabled,
      transfers: [],
      left: [],
      bytes: 0,
      deletedAt: o.now(),
      deletedBy: { id: me.id, username: me.username },
      reason,
      purging: null,
    }
    items.set(rec.id, rec)
    u.disabled = true
    o.dropSessions(u.id)
    for (const t of plan.transfer) {
      const s = o.spaces.get(t.space.id)!
      const to = s.members.get(t.to.id)
      if (to) to.role = 'owner'
      else s.members.set(t.to.id, { role: 'owner', joined: o.now() })
      rec.transfers.push({
        space: s.id,
        name: s.name,
        to: t.to.id,
        toUsername: t.to.username,
      })
      // After a transfer the person is an editor of it: that membership goes too.
      s.members.delete(u.id)
      rec.left.push({ space: s.id, name: s.name, role: 'editor' })
    }
    for (const l of plan.leave) {
      const s = o.spaces.get(l.space.id)!
      s.members.delete(u.id)
      rec.left.push({ space: s.id, name: s.name, role: l.role })
    }
    for (const r of plan.recycle) recycleSpace(o.spaces.get(r.space.id)!, me, reason, rec.id)
    rec.bytes = sum(childrenOf(rec.id), (k) => k.bytes)
    o.users.delete(u.id)
    return rec
  }

  // --- confirmations and bulk actions ---
  const reqOf = (b: Record<string, unknown>) => ({
    spaces: strs(b.spaces, 'spaces'),
    users: strs(b.users, 'users'),
    items: strs(b.items, 'items'),
    heir: typeof b.heir === 'string' ? b.heir : '',
    transfers: (typeof b.transfers === 'object' && b.transfers !== null ? b.transfers : {}) as Record<string, string>,
  })
  const idsOf = (action: Action, r: ConfirmRec['req']): string[] => {
    const kind = ACTIONS[action]
    return kind === 'spaces'
      ? r.spaces
      : kind === 'users'
        ? r.users
        : kind === 'items'
          ? r.items
          : [...r.spaces, ...r.items]
  }
  // What the plan was made from: ids, owners, members, states, heirs.
  const fingerprint = (me: U, action: Action, r: ConfirmRec['req']): string => {
    const parts: string[] = [action, r.heir]
    for (const id of [...idsOf(action, r)].sort()) {
      if (ACTIONS[action] === 'users') {
        const u = userFor(id)
        parts.push(`${u.id}:${u.disabled}`)
        if (action === 'delete_users') {
          const p = userPlan(me, id, r.heir, r.transfers)
          parts.push(
            JSON.stringify([
              p.recycle.map((x) => x.space.id),
              p.transfer.map((x) => [x.space.id, x.to.id]),
              p.leave.map((x) => [x.space.id, x.role]),
              p.needsHeir.map((x) => x.id),
              p.blockers.map((x) => x.reason),
            ]),
          )
        }
      } else if (ACTIONS[action] === 'items' || (action === 'export' && r.items.includes(id))) {
        const i = itemFor(me, id)
        parts.push(`${i.id}:${i.purging === null ? 'ready' : 'purging'}:${i.deletedAt}`)
      } else {
        const s = spaceFor(me, id)
        parts.push(`${s.id}:${o.ownerOf(s)?.id ?? ''}:${memberIds(s).join(',')}:${s.disabled}:${s.cache}:${s.pinned}`)
      }
    }
    return parts.join('|')
  }
  const planOf = (me: U, action: Action, r: ConfirmRec['req']) => {
    const plan: unknown[] = []
    const needsHeir: unknown[] = []
    const blockers: unknown[] = []
    let bytes = 0
    for (const id of idsOf(action, r)) {
      if (ACTIONS[action] === 'users') {
        if (action === 'delete_users') {
          const p = userPlan(me, id, r.heir, r.transfers)
          plan.push(p)
          bytes += p.bytes
          if (p.needsHeir.length) needsHeir.push({ user: id, spaces: p.needsHeir })
          for (const b of p.blockers) blockers.push({ user: id, reason: b.reason })
        } else {
          const u = userFor(id)
          plan.push({ user: { id: u.id, username: u.username }, action })
        }
      } else if (ACTIONS[action] === 'items' || (action === 'export' && r.items.includes(id))) {
        if (action === 'purge' && !siteOf(me))
          throw new HttpError(
            403,
            'forbidden',
            'only an administrator of the whole server may purge the recycle bin early',
          )
        const i = itemFor(me, id)
        bytes += i.bytes
        plan.push({
          item: i.id,
          kind: i.kind,
          name: itemName(i),
          bytes: i.bytes,
          action,
          deletedAt: o.rfc3339(i.deletedAt),
        })
      } else if (action === 'delete_spaces') {
        const p = spacePreview(me, id)
        plan.push(p)
        bytes += p.heldBytes
        if (o.spaces.get(id)!.personal) blockers.push({ space: id, reason: 'personal_space' })
      } else {
        const s = spaceFor(me, id)
        bytes += bytesOf(s)
        plan.push({ space: { id: s.id, name: s.name }, action })
      }
    }
    return { plan, bytes, needsHeir, blockers }
  }
  const confirm = (me: U, b: Record<string, unknown>): MockResponse => {
    const action = b.action as Action
    if (typeof action !== 'string' || !(action in ACTIONS)) bad(`action is one of ${Object.keys(ACTIONS).join(', ')}`)
    const req = reqOf(b)
    const ids = idsOf(action, req)
    if (ids.length < 1 || ids.length > MAX_IDS)
      bad(`name from 1 to ${MAX_IDS} ${ACTIONS[action].replace('+', ' or ')} for ${action}`)
    const made = planOf(me, action, req)
    const token = `c_${o.token()}`
    confirms.set(token, {
      by: me.id,
      action,
      req,
      hash: fingerprint(me, action, req),
      expires: o.now() + 600_000,
    })
    return {
      status: 200,
      body: {
        confirm: token,
        expires: o.rfc3339(o.now() + 600_000),
        count: ids.length,
        plan: made.plan,
        bytes: made.bytes,
        needsHeir: made.needsHeir,
        blockers: made.blockers,
      },
    }
  }
  // A token is spent when it is used, even by a request that fails.
  const spend = (me: U, token: string, want?: { action: Action; items: string[] }): ConfirmRec => {
    const c = confirms.get(token)
    confirms.delete(token)
    if (!c || c.by !== me.id || c.expires <= o.now()) return gone('that confirmation is unknown, used or out of time')
    if (
      want &&
      (c.action !== want.action || JSON.stringify([...c.req.items].sort()) !== JSON.stringify([...want.items].sort()))
    )
      return gone('that confirmation is for something else')
    if (fingerprint(me, c.action, c.req) !== c.hash)
      throw new HttpError(409, 'confirm_stale', 'what the confirmation was made for has changed: look at it again')
    return c
  }

  const startBulk = (me: U, b: Record<string, unknown>): MockResponse => {
    if (typeof b.confirm !== 'string') bad('give the confirmation')
    const c = spend(me, b.confirm as string)
    const ids = c.action === 'export' ? [''] : idsOf(c.action, c.req)
    const job: JobRec = {
      id: o.newId((x) => jobs.has(x)),
      by: me.id,
      action: c.action,
      req: c.req,
      ids,
      done: 0,
      failed: [],
      state: hooks.holdJobs ? 'queued' : 'running',
      site: siteOf(me),
    }
    jobs.set(job.id, job)
    return { status: 202, body: { job: jobOut(job) } }
  }
  const jobOut = (j: JobRec) => ({
    id: j.id,
    action: j.action,
    total: j.ids.length,
    done: j.done,
    failed: j.failed,
    state: j.state,
  })
  const doItem = (me: U, action: Action, id: string, req: ConfirmRec['req']) => {
    switch (action) {
      case 'delete_spaces':
        deleteSpace(me, id, '')
        break
      case 'delete_users':
        deleteUser(me, id, req.heir, req.transfers, '')
        break
      case 'disable_spaces':
      case 'enable_spaces': {
        const s = spaceFor(me, id)
        s.disabled = action === 'disable_spaces'
        break
      }
      case 'disable_users':
      case 'enable_users': {
        const u = userFor(id)
        if (u.id === me.id && action === 'disable_users')
          throw new HttpError(409, 'conflict', 'you cannot disable your own account')
        u.disabled = action === 'disable_users'
        if (u.disabled) o.dropSessions(u.id)
        break
      }
      case 'restore':
        restoreItem(me, id, '')
        break
      case 'purge':
        purgeItem(me, id)
        break
      case 'evict':
      case 'pin':
      case 'unpin': {
        if (!siteOf(me)) throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may do that')
        const s = spaceFor(me, id)
        if (action === 'evict') {
          if (!s.remote) throw new HttpError(409, 'conflict', 'a space without a remote cannot be evicted')
          if (busy.has(s.id)) throw new HttpError(409, 'busy', 'this space is in use now')
          s.cache = 'evicted'
        } else s.pinned = action === 'pin'
        break
      }
      case 'export':
        break
    }
  }
  // A running job does one item each time it is asked about.
  const stepJob = (j: JobRec) => {
    if (j.state === 'queued' && !hooks.holdJobs) j.state = 'running'
    if (j.state !== 'running') return
    if (hooks.breakJobs) {
      j.state = 'failed'
      j.failed.push({
        id: j.ids[j.done] ?? '',
        error: 'the job stopped',
        code: 'internal',
      })
      return
    }
    const me = o.users.get(j.by)
    const id = j.ids[j.done]
    if (id === undefined) {
      j.state = 'done'
      return
    }
    try {
      if (!me || me.role !== 'admin') throw new HttpError(403, 'forbidden', 'you are not an administrator any more')
      if (j.action === 'export') makeExport(me, { spaces: j.req.spaces, items: j.req.items }, false)
      else doItem(me, j.action, id, j.req)
    } catch (e) {
      if (!(e instanceof HttpError)) throw e
      j.failed.push({ id, error: e.message, code: e.code })
    }
    j.done++
    if (j.done >= j.ids.length) j.state = 'done'
  }
  const jobFor = (me: U, id: string): JobRec => {
    const j = jobs.get(id)
    // A job a site administrator started is the site's: a tenant administrator does not see it.
    if (!j || (j.site && !siteOf(me))) return gone('no such job')
    return j
  }

  // --- exports ---
  const exportOut = (me: U, e: ExportRec2) => ({
    id: e.id,
    tenant: o.tenant,
    madeBy: { id: e.by, username: o.users.get(e.by)?.username ?? '' },
    spaces: e.sources.map((s) => ({
      ...(s.space ? { space: s.space } : {}),
      ...(s.item ? { item: s.item } : {}),
      name: s.name,
      spaceId: s.spaceId,
      // Exists now and the caller may open it (the mock has one tenant: a personal space is a site administrator's only).
      open: o.spaces.has(s.spaceId) && (siteOf(me) || !o.spaces.get(s.spaceId)!.personal),
    })),
    history: e.history,
    state: e.state,
    ...(e.state === 'failed' ? { error: e.error } : {}),
    warnings: e.state === 'ready' ? e.warnings : [],
    bytes: e.bytes,
    created: o.rfc3339(e.created),
    expires: o.rfc3339(e.expires),
  })
  const makeExport = (
    me: U,
    b: {
      spaces?: string[]
      items?: string[]
      user?: string | null
      history?: boolean
    },
    history: boolean,
  ): ExportRec2 => {
    let spaceIds = [...(b.spaces ?? [])]
    let itemIds = [...(b.items ?? [])]
    if (b.user) {
      const u = userFor(b.user)
      spaceIds = ownedBy(u)
        .filter((s) => (siteOf(me) || !s.personal) && s.members.size === 1)
        .map((s) => s.id)
      itemIds = [...items.values()]
        .filter((i) => i.parent === [...items.values()].find((x) => x.kind === 'user' && x.user?.id === u.id)?.id)
        .map((i) => i.id)
    }
    if (spaceIds.length + itemIds.length === 0 || spaceIds.length + itemIds.length > MAX_IDS)
      bad(`name from 1 to ${MAX_IDS} spaces or recycle items to export`)
    if ((b.history === true || history) && !siteOf(me))
      throw new HttpError(403, 'forbidden', 'only an administrator of the whole server may export the history')
    const sources: ExportRec2['sources'] = []
    let personal = false
    for (const id of spaceIds) {
      const s = spaceFor(me, id)
      sources.push({ space: s.id, name: s.name, spaceId: s.id })
      personal = personal || s.personal
    }
    for (const id of itemIds) {
      const i = itemFor(me, id)
      if (i.kind !== 'space') return gone('no such item in the recycle bin')
      sources.push({ item: i.id, name: i.space!.name, spaceId: i.space!.id })
      personal = personal || i.space!.personal
    }
    const bytes = sum(spaceIds, (id) => bytesOf(o.spaces.get(id)!)) + sum(itemIds, (id) => items.get(id)!.bytes) + 2048
    if (sum([...exportsMap.values()], (e) => e.bytes) + bytes > hooks.exportsLimit)
      throw new HttpError(507, 'exports_full', 'the exports would take more room than exports.maxBytes allows')
    const e: ExportRec2 = {
      id: o.newId((x) => exportsMap.has(x)),
      by: me.id,
      siteOnly: siteOf(me) || personal || b.history === true,
      sources,
      history: b.history === true,
      state: 'running',
      error: '',
      warnings: [],
      bytes,
      created: o.now(),
      expires: o.now() + 7 * DAY,
      looks: 0,
    }
    exportsMap.set(e.id, e)
    return e
  }
  const exportFor = (me: U, id: string): ExportRec2 => {
    const e = exportsMap.get(id)
    if (e && e.expires <= o.now()) exportsMap.delete(id)
    const live = exportsMap.get(id)
    if (!live || (live.siteOnly && !siteOf(me))) return gone('no such export')
    // It is made while it is asked about: running, then ready.
    if (live.state === 'running' && ++live.looks >= 1) live.state = hooks.breakJobs ? 'failed' : 'ready'
    if (live.state === 'failed' && !live.error) live.error = 'the export could not be made'
    return live
  }

  // --- the view of a space ---
  const viewKey = (token: string, space: string) => `${token}\u0000${space}`
  const startView = (me: U, token: string | null, id: string, b: Record<string, unknown>): MockResponse => {
    const s = spaceFor(me, id)
    const reason = typeof b.reason === 'string' ? b.reason.trim() : ''
    if (reason === '' || reason.length > 500) bad('reason is from 1 to 500 characters')
    if (s.disabled) throw new HttpError(403, 'space_disabled', 'this space has been disabled by an administrator')
    const until = o.now() + 3_600_000
    views.set(viewKey(token ?? '', s.id), { since: o.now(), until })
    return { status: 200, body: { space: s.id, until: o.rfc3339(until) } }
  }

  return {
    locationOf,
    counted,
    decorate,
    summary,
    touch,
    lastActiveOf: (userId: string): number | undefined => activity.get(userId)?.lastActive,
    listSpaces,
    // Whether this session may read a space it is not a member of.
    viewing: (token: string | null, space: string) => (views.get(viewKey(token ?? '', space))?.until ?? 0) > o.now(),
    // The request of a person who is gone from the world: their sessions are dropped by the server.
    deleteSpace: (me: U, id: string, reason: string) => itemOut(me, deleteSpace(me, id, reason)),
    deleteUser: (me: U, id: string, b: Record<string, unknown>) => {
      const reason = typeof b.reason === 'string' ? b.reason : ''
      if (reason.length > 500) bad('reason is at most 500 characters')
      const r = reqOf(b)
      return itemOut(me, deleteUser(me, id, r.heir, r.transfers, reason))
    },
    route(
      me: U,
      token: string | null,
      method: string,
      path: string,
      url: URL,
      body: Record<string, unknown>,
    ): MockResponse | null {
      const j = (b: unknown, status = 200): MockResponse => ({
        status,
        body: b,
      })
      if (path === '/admin/storage/people' && method === 'GET') return listPeople(me, url)
      const person = /^\/admin\/storage\/people\/([^/]+)$/.exec(path)
      if (person && method === 'GET') return onePerson(me, token, decodeURIComponent(person[1]))
      if (path === '/admin/recycle' && method === 'GET') return listRecycle(me, url)
      const item = /^\/admin\/recycle\/([^/]+)(\/restore)?$/.exec(path)
      if (item) {
        const id = decodeURIComponent(item[1])
        if (item[2] && method === 'POST') {
          const owner = typeof body.owner === 'string' ? body.owner : ''
          return restoreItem(me, id, owner)
        }
        if (!item[2] && method === 'DELETE') {
          itemFor(me, id)
          if (!siteOf(me))
            throw new HttpError(
              403,
              'forbidden',
              'only an administrator of the whole server may purge the recycle bin early',
            )
          spend(me, url.searchParams.get('confirm') ?? '', {
            action: 'purge',
            items: [id],
          })
          return j(itemOut(me, purgeItem(me, id)), 202)
        }
      }
      const sdel = /^\/admin\/spaces\/([^/]+)\/deletion$/.exec(path)
      if (sdel && method === 'GET') return j(spacePreview(me, decodeURIComponent(sdel[1])))
      const udel = /^\/admin\/users\/([^/]+)\/deletion$/.exec(path)
      if (udel && method === 'GET')
        return j(userPlan(me, decodeURIComponent(udel[1]), url.searchParams.get('heir') ?? '', {}))
      if (path === '/admin/confirm' && method === 'POST') return confirm(me, body)
      if (path === '/admin/bulk' && method === 'POST') return startBulk(me, body)
      const job = /^\/admin\/jobs\/([^/]+)$/.exec(path)
      if (job && method === 'GET') {
        const jb = jobFor(me, decodeURIComponent(job[1]))
        stepJob(jb)
        return j({ job: jobOut(jb) })
      }
      if (path === '/admin/exports' && method === 'POST') {
        const e = makeExport(
          me,
          {
            spaces: strs(body.spaces, 'spaces'),
            items: strs(body.items, 'items'),
            user: typeof body.user === 'string' ? body.user : null,
            history: body.history === true,
          },
          false,
        )
        return j({ ...exportOut(me, e), state: 'running' }, 202)
      }
      if (path === '/admin/exports' && method === 'GET') {
        const rows = [...exportsMap.keys()]
          .flatMap((id) => {
            try {
              return [exportFor(me, id)]
            } catch {
              return []
            }
          })
          .sort((a, b) => b.created - a.created || (a.id < b.id ? 1 : -1))
        return j({ items: rows.map((e) => exportOut(me, e)), next: '' })
      }
      const ex = /^\/admin\/exports\/([^/]+)(\/download)?$/.exec(path)
      if (ex) {
        const e = exportFor(me, decodeURIComponent(ex[1]))
        if (ex[2] && method === 'GET') {
          if (e.state !== 'ready') throw new HttpError(409, 'conflict', 'the export is not ready')
          return {
            status: 200,
            bytes: new TextEncoder().encode('PK mock export'),
            headers: {
              'Content-Type': 'application/zip',
              'Content-Disposition': `attachment; filename="export-${e.id}.zip"`,
            },
          }
        }
        if (!ex[2] && method === 'GET') return j(exportOut(me, e))
        if (!ex[2] && method === 'DELETE') {
          exportsMap.delete(e.id)
          return { status: 204 }
        }
      }
      const view = /^\/admin\/spaces\/([^/]+)\/view$/.exec(path)
      if (view && method === 'POST') return startView(me, token, decodeURIComponent(view[1]), body)
      if (view && method === 'DELETE') {
        views.delete(viewKey(token ?? '', spaceFor(me, decodeURIComponent(view[1])).id))
        return { status: 204 }
      }
      return null
    },
    // --- test helpers (mock only) ---
    seed: {
      activity: (userId: string, a: { lastActive?: number; firstWrite?: number }) =>
        void activity.set(userId, { ...activity.get(userId), ...a }),
      busy: (space: string, on = true) => void (on ? busy.add(space) : busy.delete(space)),
      holdJobs: (on: boolean) => void (hooks.holdJobs = on),
      breakJobs: (on: boolean) => void (hooks.breakJobs = on),
      interruptJobs: () =>
        void [...jobs.values()].forEach(
          (j) => (j.state === 'queued' || j.state === 'running') && (j.state = 'interrupted'),
        ),
      exportsLimit: (bytes: number) => void (hooks.exportsLimit = bytes),
      expireExport: (id: string) => void (exportsMap.get(id) && (exportsMap.get(id)!.expires = o.now() - 1)),
      exportWarning: (id: string, text: string) => void exportsMap.get(id)?.warnings.push(text),
      recycled: () =>
        [...items.values()].map((i) => ({
          id: i.id,
          kind: i.kind,
          name: itemName(i),
          parent: i.parent,
        })),
    },
  }
}
export type ManageBook = ReturnType<typeof createManageBook>
