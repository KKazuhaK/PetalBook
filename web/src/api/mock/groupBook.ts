// The mock's side of milestone 3.2 (docs/api.md, "Groups"): the groups, who is in them, the groups that
// are members of spaces, and the one rule everything else reads: a person's role in a space is the
// highest of their own and their groups'. The sign-in providers' mappings are in signInBook.ts.

import type { EffectiveMember, Group, GroupMember, GroupPerson, GroupRef, GroupSource, Member, SpaceRole } from '../types.ts'
import { HttpError, compareCodePoints } from './store.ts'
import type { SignInBook } from './signInBook.ts'
import type { MockResponse } from './server.ts'

export const GROUP_LIMITS = { groups: 10_000, people: 100_000, perUser: 1_000, perSpace: 200, add: 500 }
const RANK: Record<SpaceRole, number> = { owner: 3, editor: 2, viewer: 1 }
const ROLE_LIST: SpaceRole[] = ['owner', 'editor', 'viewer']

interface GroupRec {
  id: string
  name: string
  description: string
  source: GroupSource
  created: string
  updated: string
  // user id -> where each membership came from (empty for a local group)
  people: Map<string, { provider: string; value: string }[]>
  addedAt: Map<string, string>
}

// What the book needs to know of a space and of a user.
export interface GroupSpace {
  id: string
  name: string
  personal: boolean
  members: Map<string, { role: SpaceRole; joined: number }>
  groups: Map<string, SpaceRole>
}
export interface GroupUser {
  id: string
  username: string
  name: string
  disabled: boolean
}
export interface GroupBookOptions {
  now: () => string
  users: () => Map<string, GroupUser>
  spaces: () => Map<string, GroupSpace>
  newId: (taken: (id: string) => boolean) => string
  book: () => SignInBook
}

const fold = (s: string) => s.normalize('NFC').toLowerCase()
const bad = (message: string): never => {
  throw new HttpError(400, 'invalid', message)
}
const page = <T>(all: T[], cursor: string, limit: number, key: (x: T) => string): { items: T[]; next: string } => {
  const from = cursor ? all.findIndex((x) => key(x) > cursor && true) : 0
  const start = cursor ? (from < 0 ? all.length : from) : 0
  const items = all.slice(start, start + limit)
  return { items, next: start + limit < all.length ? key(items[items.length - 1]) : '' }
}

export function createGroupBook(o: GroupBookOptions) {
  const groups = new Map<string, GroupRec>()
  const j = (b: unknown, status = 200): MockResponse => ({ status, body: b })
  const ref = (g: GroupRec): GroupRef => ({ id: g.id, name: g.name, source: g.source })
  const byName = (a: GroupRec, b: GroupRec) => compareCodePoints(fold(a.name), fold(b.name)) || compareCodePoints(a.id, b.id)
  const spacesOf = (id: string) => [...o.spaces().values()].filter((s) => s.groups.has(id))

  const view = (g: GroupRec): Group => ({
    ...ref(g),
    description: g.description,
    people: g.people.size,
    spaces: spacesOf(g.id).length,
    mappedFrom: g.source === 'synced' ? o.book().mappingsOf(g.id) : [],
    created: g.created,
    updated: g.updated,
  })
  const need = (id: string): GroupRec => groups.get(id) ?? (() => { throw new HttpError(404, 'not_found', 'there is no such group') })()
  const inGroups = (userId: string): GroupRec[] => [...groups.values()].filter((g) => g.people.has(userId))

  // --- who may do what in a space: the union of the person's own role and their groups' ---
  const reach = (s: GroupSpace, userId: string): { role: SpaceRole | undefined; direct: SpaceRole | undefined; via: boolean } => {
    const direct = s.members.get(userId)?.role
    let best = direct
    let via = false
    for (const [gid, role] of s.groups) {
      if (!groups.get(gid)?.people.has(userId)) continue
      via = true
      if (!best || RANK[role] > RANK[best]) best = role
    }
    return { role: best, direct, via }
  }
  const has = (s: GroupSpace | undefined, userId: string): boolean => !!s && reach(s, userId).role !== undefined

  const checkName = (raw: unknown, except?: string): string => {
    const name = typeof raw === 'string' ? raw.trim() : bad('name is required')
    const n = Array.from(name).length
    if (n < 1 || n > 80) bad('a group name is 1 to 80 characters')
    if (Array.from(name).some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)) bad('a group name cannot contain control characters')
    for (const g of groups.values()) if (g.id !== except && fold(g.name) === fold(name)) throw new HttpError(409, 'exists', 'a group with that name exists already')
    return name
  }
  const checkDescription = (raw: unknown): string => {
    const d = typeof raw === 'string' ? raw.trim() : bad('description is text')
    if (Array.from(d).length > 500) bad('a group description is at most 500 characters')
    return d
  }
  const limitInt = (url: URL, def: number, max: number): number => {
    const raw = url.searchParams.get('limit')
    if (raw === null || raw === '') return def
    const n = Number(raw)
    if (!Number.isInteger(n) || n < 1 || n > max) return bad(`limit is a number from 1 to ${max}`)
    return n
  }

  // --- administrators ---
  const routeAdmin = (me: { id: string }, method: string, path: string, url: URL, body: Record<string, unknown>): MockResponse => {
    const book = o.book()
    if (path === '/admin/groups' && method === 'GET') {
      const limit = limitInt(url, 50, 200)
      const q = fold(url.searchParams.get('q') ?? '')
      const source = url.searchParams.get('source') ?? ''
      if (source && source !== 'local' && source !== 'synced') bad('source is local or synced')
      const user = url.searchParams.get('user') ?? ''
      const all = [...groups.values()].filter((g) => (q === '' || fold(g.name).includes(q)) && (!source || g.source === source) && (!user || g.people.has(user))).sort(byName)
      const r = page(all, fold(url.searchParams.get('cursor') ?? '') === '' ? '' : url.searchParams.get('cursor')!, limit, (g) => fold(g.name))
      return j({ items: r.items.map(view), next: r.next })
    }
    if (path === '/admin/groups' && method === 'POST') {
      const source = body.source
      if (source !== 'local' && source !== 'synced') bad('source is local or synced')
      const name = checkName(body.name)
      const description = body.description === undefined ? '' : checkDescription(body.description)
      if (groups.size >= GROUP_LIMITS.groups) throw new HttpError(409, 'group_limit', `a server has at most ${GROUP_LIMITS.groups} groups`)
      const at = o.now()
      const g: GroupRec = { id: o.newId((x) => groups.has(x)), name, description, source: source as GroupSource, created: at, updated: at, people: new Map(), addedAt: new Map() }
      groups.set(g.id, g)
      book.record('group_created', { actor: me.id, detail: { group: g.id, name, source } })
      return j(view(g), 201)
    }
    const one = /^\/admin\/groups\/([^/]+)$/.exec(path)
    if (one) {
      const g = need(decodeURIComponent(one[1]))
      if (method === 'GET') return j(view(g))
      if (method === 'PATCH') {
        if (body.source !== undefined) bad('the source of a group cannot change')
        const changed: Record<string, unknown> = {}
        const name = body.name === undefined ? g.name : checkName(body.name, g.id)
        const description = body.description === undefined ? g.description : checkDescription(body.description)
        const renamed = name !== g.name
        g.name = name
        if (description !== g.description) {
          g.description = description
          changed.description = description
        }
        if (renamed || Object.keys(changed).length > 0) {
          g.updated = o.now()
          book.record('group_changed', { actor: me.id, detail: { group: g.id, name: g.name, source: g.source, ...changed } })
        }
        return j(view(g))
      }
      if (method === 'DELETE') {
        const mappings = book.mappingsOf(g.id)
        const force = url.searchParams.get('force')
        if (force !== null && force !== '1' && force !== '0') bad('force is 1 or 0')
        if (force !== '1' && (spacesOf(g.id).length > 0 || mappings.length > 0)) throw new HttpError(409, 'group_in_use', 'the group is a member of spaces or mapped by providers', { spaces: spacesOf(g.id).length, mappings: mappings.length })
        const inSpaces = spacesOf(g.id)
        for (const s of inSpaces) s.groups.delete(g.id)
        book.unmap(g.id)
        groups.delete(g.id)
        book.record('group_deleted', { actor: me.id, detail: { group: g.id, name: g.name, source: g.source, people: g.people.size, spaces: inSpaces.length } })
        return { status: 204 }
      }
    }
    const people = /^\/admin\/groups\/([^/]+)\/members$/.exec(path)
    if (people) {
      const g = need(decodeURIComponent(people[1]))
      const users = o.users()
      if (method === 'GET') {
        const limit = limitInt(url, 50, 200)
        const q = fold(url.searchParams.get('q') ?? '')
        const all = [...g.people.keys()]
          .map((id) => users.get(id)!)
          .filter((u) => q === '' || fold(u.username).includes(q) || fold(u.name).includes(q))
          .sort((a, b) => compareCodePoints(a.username, b.username))
        const r = page(all, url.searchParams.get('cursor') ?? '', limit, (u) => u.username)
        const items: GroupPerson[] = r.items.map((u) => ({ userId: u.id, username: u.username, name: u.name, disabled: u.disabled, from: g.people.get(u.id)!.filter((f) => f.provider !== ''), added: g.addedAt.get(u.id) ?? g.created }))
        return j({ items, next: r.next })
      }
      if (method === 'POST') {
        if (g.source !== 'local') throw new HttpError(409, 'group_synced', 'the people of a synced group come from sign-in and cannot be changed by hand')
        const ids = body.userIds
        if (!Array.isArray(ids) || ids.length < 1 || ids.length > GROUP_LIMITS.add || ids.some((x) => typeof x !== 'string')) bad(`userIds holds 1 to ${GROUP_LIMITS.add} ids`)
        const want = [...new Set(ids as string[])]
        const missing = want.filter((id) => !users.has(id))
        if (missing.length > 0) throw new HttpError(404, 'user_not_found', 'some of those users do not exist', { missing })
        const fresh = want.filter((id) => !g.people.has(id))
        if (fresh.some((id) => inGroups(id).length + 1 > GROUP_LIMITS.perUser)) throw new HttpError(409, 'group_limit', `a person is in at most ${GROUP_LIMITS.perUser} groups`)
        if (g.people.size + fresh.length > GROUP_LIMITS.people) throw new HttpError(409, 'group_limit', `a group has at most ${GROUP_LIMITS.people} people`)
        for (const id of fresh) {
          g.people.set(id, [{ provider: '', value: '' }])
          g.addedAt.set(id, o.now())
        }
        if (fresh.length > 0) book.record('group_member_added', { actor: me.id, detail: { group: g.id, users: want } })
        return j({ added: fresh.length, already: want.length - fresh.length })
      }
    }
    const person = /^\/admin\/groups\/([^/]+)\/members\/([^/]+)$/.exec(path)
    if (person && method === 'DELETE') {
      const g = need(decodeURIComponent(person[1]))
      if (g.source !== 'local') throw new HttpError(409, 'group_synced', 'the people of a synced group come from sign-in and cannot be changed by hand')
      const uid = decodeURIComponent(person[2])
      if (!g.people.delete(uid)) throw new HttpError(404, 'not_found', 'that user is not in this group')
      book.record('group_member_removed', { actor: me.id, user: uid, detail: { group: g.id, users: [uid] } })
      return { status: 204 }
    }
    const sp = /^\/admin\/groups\/([^/]+)\/spaces$/.exec(path)
    if (sp && method === 'GET') {
      const g = need(decodeURIComponent(sp[1]))
      const limit = limitInt(url, 50, 200)
      const all = spacesOf(g.id).sort((a, b) => compareCodePoints(fold(a.name), fold(b.name)) || compareCodePoints(a.id, b.id))
      const r = page(all, url.searchParams.get('cursor') ?? '', limit, (s) => `${fold(s.name)}\u0000${s.id}`)
      return j({ items: r.items.map((s) => ({ id: s.id, name: s.name, role: s.groups.get(g.id)! })), next: r.next })
    }
    throw new HttpError(404, 'not_found', '没有这个接口')
  }

  // --- everyone signed in ---
  const find = (url: URL): MockResponse => {
    const q = url.searchParams.get('q') ?? ''
    const n = Array.from(q).length
    if (n < 1 || n > 80) bad('q is 1 to 80 characters')
    const limit = limitInt(url, 20, 20)
    const out = [...groups.values()].filter((g) => fold(g.name).includes(fold(q))).sort(byName).slice(0, limit)
    return j({ groups: out.map(ref) })
  }

  // --- the members of a space ---
  const roleName = (r: SpaceRole | undefined) => r ?? ''
  const membersView = (s: GroupSpace, me: string, directList: Member[]): { members: Member[]; groups: GroupMember[] } => {
    const members = directList.map((m) => {
      const r = reach(s, m.userId)
      return { ...m, viaGroups: r.via, effectiveRole: r.role ?? m.role }
    })
    const list: GroupMember[] = [...s.groups]
      .map(([gid, role]) => ({ g: groups.get(gid)!, role }))
      .sort((a, b) => byName(a.g, b.g))
      .map(({ g, role }) => ({ groupId: g.id, name: g.name, source: g.source, role, people: g.people.size, yours: g.people.has(me) }))
    return { members, groups: list }
  }
  const effective = (s: GroupSpace, me: string, url: URL): MockResponse => {
    const limit = limitInt(url, 50, 200)
    const q = fold(url.searchParams.get('q') ?? '')
    const users = o.users()
    const all: EffectiveMember[] = []
    for (const u of users.values()) {
      if (u.disabled) continue
      const r = reach(s, u.id)
      if (!r.role) continue
      if (q !== '' && !fold(u.username).includes(q) && !fold(u.name).includes(q)) continue
      const gs = [...s.groups].filter(([gid]) => groups.get(gid)?.people.has(u.id)).map(([gid, role]) => ({ groupId: gid, name: groups.get(gid)!.name, role })).sort((a, b) => compareCodePoints(fold(a.name), fold(b.name)))
      all.push({ userId: u.id, username: u.username, name: u.name, role: r.role, directRole: roleName(r.direct), groups: gs, you: u.id === me })
    }
    all.sort((a, b) => compareCodePoints(a.username, b.username))
    const r = page(all, url.searchParams.get('cursor') ?? '', limit, (x) => x.username)
    return j({ items: r.items, next: r.next })
  }
  const routeSpace = (me: { id: string }, s: GroupSpace, method: string, rest: string, body: Record<string, unknown>): MockResponse | null => {
    const book = o.book()
    const checkRole = (raw: unknown): SpaceRole => {
      if (typeof raw !== 'string' || !ROLE_LIST.includes(raw as SpaceRole)) return bad(`a role is one of ${ROLE_LIST.join(', ')}`)
      return raw as SpaceRole
    }
    const member = (g: GroupRec, role: SpaceRole): GroupMember => ({ groupId: g.id, name: g.name, source: g.source, role, people: g.people.size, yours: g.people.has(me.id) })
    if (rest === '/groups' && method === 'POST') {
      const role = checkRole(body.role)
      if (s.personal) throw new HttpError(409, 'personal_space', 'a personal space belongs to one person and cannot be shared')
      const g = typeof body.groupId === 'string' ? groups.get(body.groupId) : undefined
      if (!g) throw new HttpError(404, 'group_not_found', 'there is no such group')
      if (s.groups.has(g.id)) throw new HttpError(409, 'exists', 'that group is a member of this space already')
      if (s.groups.size >= GROUP_LIMITS.perSpace) throw new HttpError(409, 'group_limit', `a space has at most ${GROUP_LIMITS.perSpace} groups`)
      s.groups.set(g.id, role)
      book.record('space_group_added', { actor: me.id, detail: { space: s.id, group: g.id, role } })
      return j(member(g, role), 201)
    }
    const one = /^\/groups\/([^/]+)$/.exec(rest)
    if (one) {
      const gid = decodeURIComponent(one[1])
      const cur = s.groups.get(gid)
      if (method === 'PATCH') {
        const role = checkRole(body.role)
        if (cur === undefined) throw new HttpError(404, 'not_found', 'that group is not a member of this space')
        if (cur !== role) {
          s.groups.set(gid, role)
          book.record('space_group_changed', { actor: me.id, detail: { space: s.id, group: gid, role: { old: cur, new: role } } })
        }
        return j(member(groups.get(gid)!, role))
      }
      if (method === 'DELETE') {
        if (cur === undefined) throw new HttpError(404, 'not_found', 'that group is not a member of this space')
        s.groups.delete(gid)
        book.record('space_group_removed', { actor: me.id, detail: { space: s.id, group: gid, role: cur } })
        return { status: 204 }
      }
    }
    return null
  }
  // Someone who reaches a space only through groups cannot leave it.
  const viaGroupsOnly = (s: GroupSpace, userId: string): string[] => {
    if (s.members.has(userId)) return []
    return [...s.groups].filter(([gid]) => groups.get(gid)?.people.has(userId)).map(([gid]) => groups.get(gid)!.name).sort(compareCodePoints)
  }

  return {
    groups,
    reach,
    has,
    inGroups: (id: string): GroupRef[] => inGroups(id).sort(byName).map(ref),
    routeAdmin,
    find,
    routeSpace,
    effective,
    membersView,
    viaGroupsOnly,
    // Providers: a provider goes, or a mapping entry leaves.
    dropProvider: (slug: string) => {
      for (const g of groups.values()) for (const [uid, from] of g.people) {
        const left = from.filter((f) => f.provider !== slug)
        if (left.length === 0) g.people.delete(uid)
        else g.people.set(uid, left)
      }
    },
    dropMapped: (slug: string, value: string, keep: string) => {
      for (const g of groups.values()) {
        if (g.id === keep) continue
        for (const [uid, from] of g.people) {
          const left = from.filter((f) => !(f.provider === slug && f.value === value))
          if (left.length === 0) g.people.delete(uid)
          else g.people.set(uid, left)
        }
      }
    },
    // Mock only: what a sign-in through a provider would have brought (tests and the dev server).
    syncFrom: (userId: string, slug: string, want: { groupId: string; value: string }[]) => {
      for (const g of groups.values()) {
        const kept = (g.people.get(userId) ?? []).filter((f) => f.provider !== slug)
        const mine = want.filter((w) => w.groupId === g.id).map((w) => ({ provider: slug, value: w.value }))
        const next = [...kept, ...mine]
        if (next.length === 0) g.people.delete(userId)
        else g.people.set(userId, next)
      }
    },
  }
}
export type GroupBook = ReturnType<typeof createGroupBook>
