// Who is in a space, and the groups in it; the search for people and groups to add; adding in a batch. No rule about who
// may change what or about the last owner: a test that needs a refusal says so with `fx.fail`.

import type { EffectiveMember, GroupMember, Member, MembersResponse, Permission, SpaceRole } from '../../types.ts'
import type { BatchRowOut } from '../../peopleTypes.ts'
import { current } from './auth.ts'
import type { AreaData } from '../data/area.ts'
import type { FixtureData, SpaceData } from '../data/model.ts'
import type { UserRec } from '../data/people.ts'
import { Refusal, remove, type Ctx, type Route } from '../kit.ts'
import { spaceOf } from './files.ts'

const ORDER: Record<SpaceRole, number> = { owner: 0, editor: 1, viewer: 2 }
const PERMISSIONS: Record<SpaceRole, Permission[]> = {
  owner: ['read', 'write', 'members', 'manage'],
  editor: ['read', 'write'],
  viewer: ['read'],
}
const strongest = (a: SpaceRole | '', b: SpaceRole | ''): SpaceRole | '' =>
  a === '' ? b : b === '' ? a : ORDER[a] <= ORDER[b] ? a : b

const meOf = (d: FixtureData) => current(d.auth)
const userOf = (d: FixtureData, id: string) => d.auth.users.find((u) => u.id === id)
const sameTenant = (d: FixtureData, u: UserRec) => u.tenant?.id === (meOf(d)?.tenant?.id ?? u.tenant?.id)

// The direct members of a space: what is kept for it, else the signed-in person alone.
function direct(d: FixtureData, s: SpaceData): AreaData['members'][string] {
  return (d.area.members[s.info.id] ??= [{ userId: meOf(d)?.id ?? '', role: s.info.directRole || 'owner' }])
}
const groupsIn = (d: FixtureData, s: SpaceData) => (d.area.spaceGroups[s.info.id] ??= [])

// The role the person has through the groups of the space.
function viaGroups(d: FixtureData, s: SpaceData, userId: string): SpaceRole | '' {
  let role: SpaceRole | '' = ''
  for (const g of groupsIn(d, s)) {
    const group = d.area.groups.find((x) => x.id === g.groupId)
    if (group?.members.includes(userId)) role = strongest(role, g.role)
  }
  return role
}

// Keeps the space's own record of the signed-in person's role in step with what is stored for it.
function syncInfo(d: FixtureData, s: SpaceData) {
  const me = meOf(d)
  const mine = direct(d, s).find((m) => m.userId === me?.id)
  const via = me ? viaGroups(d, s, me.id) : ''
  const role = strongest(mine?.role ?? '', via) as SpaceRole
  Object.assign(s.info, {
    directRole: mine?.role ?? '',
    viaGroups: via !== '',
    role,
    permissions: role ? PERMISSIONS[role] : [],
  })
}

function memberOf(d: FixtureData, s: SpaceData, row: { userId: string; role: SpaceRole }, full = true): Member {
  const u = userOf(d, row.userId)
  const via = viaGroups(d, s, row.userId)
  const effective = strongest(row.role, via)
  return {
    userId: row.userId,
    username: u?.username ?? '',
    name: u?.name ?? '',
    role: row.role,
    you: row.userId === meOf(d)?.id,
    // Only the list says how the groups add to the role.
    ...(full ? { viaGroups: via !== '', effectiveRole: effective as SpaceRole } : {}),
  }
}

function groupMember(d: FixtureData, g: { groupId: string; role: SpaceRole }): GroupMember {
  const group = d.area.groups.find((x) => x.id === g.groupId)
  const me = meOf(d)
  return {
    groupId: g.groupId,
    name: group?.name ?? '',
    source: group?.source ?? 'local',
    role: g.role,
    people: group?.members.length ?? 0,
    yours: !!me && !!group?.members.includes(me.id),
  }
}

const personRef = ({ id, username, name }: UserRec) => ({ id, username, name })
const matches = (u: { username?: string; name?: string }, q: string) =>
  (u.username ?? '').toLowerCase().includes(q.toLowerCase()) || (u.name ?? '').toLowerCase().includes(q.toLowerCase())

function batchRow(
  c: Ctx,
  s: SpaceData,
  row: { user?: string; role?: string },
  i: number,
  seen: Set<string>,
): BatchRowOut {
  const name = String(row.user ?? '').trim()
  const role = (row.role || 'editor') as SpaceRole
  const out = (status: BatchRowOut['status'], u?: UserRec): BatchRowOut => ({
    row: i + 1,
    user: name,
    role,
    status,
    ...(u ? { userId: u.id, username: u.username, name: u.name } : {}),
  })
  if (!name) return out('invalid')
  const u = c.data.auth.users.find(
    (x) => (x.username === name || (name.includes('@') && x.email === name)) && sameTenant(c.data, x),
  )
  if (!u) return out('not_found')
  if (seen.has(u.id)) return out('duplicate', u)
  seen.add(u.id)
  if (direct(c.data, s).some((m) => m.userId === u.id)) return out('already_member', u)
  return out('ok', u)
}

export const memberRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/members',
    (c): MembersResponse => {
      const s = spaceOf(c)
      const rows = [...direct(c.data, s)].sort((a, b) => ORDER[a.role] - ORDER[b.role])
      return {
        members: rows.map((r) => memberOf(c.data, s, r)),
        groups: groupsIn(c.data, s).map((g) => groupMember(c.data, g)),
      }
    },
  ],
  [
    'POST',
    '/api/spaces/:space/members',
    (c): Member => {
      const s = spaceOf(c)
      const u = c.data.auth.users.find((x) => x.username === c.body.username)
      if (!u) throw new Refusal(404, 'not_found', 'nobody has that name')
      const row = { userId: u.id, role: c.body.role as SpaceRole }
      direct(c.data, s).push(row)
      return memberOf(c.data, s, row, false)
    },
  ],
  [
    'POST',
    '/api/spaces/:space/members/batch',
    (c) => {
      const s = spaceOf(c)
      const rows = c.body.rows as { user?: string; role?: string }[]
      const seen = new Set<string>()
      const results = rows.map((r, i) => batchRow(c, s, r, i, seen))
      const summary: Record<string, number> = {}
      for (const r of results) summary[r.status] = (summary[r.status] ?? 0) + 1
      if (!c.body.dryRun)
        for (const r of results)
          if (r.status === 'ok') direct(c.data, s).push({ userId: r.userId!, role: (r.role || 'editor') as SpaceRole })
      return { dryRun: !!c.body.dryRun, results, summary }
    },
  ],
  [
    'GET',
    '/api/spaces/:space/members/effective',
    (c) => {
      const s = spaceOf(c)
      const q = c.query.get('q') ?? ''
      const ids = new Set(direct(c.data, s).map((m) => m.userId))
      for (const g of groupsIn(c.data, s))
        for (const id of c.data.area.groups.find((x) => x.id === g.groupId)?.members ?? []) ids.add(id)
      const items: EffectiveMember[] = [...ids]
        .map((id) => userOf(c.data, id))
        .filter((u): u is UserRec => !!u && matches(u, q))
        .map((u) => {
          const own = direct(c.data, s).find((m) => m.userId === u.id)?.role ?? ''
          return {
            userId: u.id,
            username: u.username,
            name: u.name,
            role: strongest(own, viaGroups(c.data, s, u.id)),
            directRole: own,
            groups: groupsIn(c.data, s)
              .filter((g) => c.data.area.groups.find((x) => x.id === g.groupId)?.members.includes(u.id))
              .map((g) => ({
                groupId: g.groupId,
                name: c.data.area.groups.find((x) => x.id === g.groupId)!.name,
                role: g.role,
              })),
            you: u.id === meOf(c.data)?.id,
          }
        })
      return { items, next: '' }
    },
  ],
  [
    'PATCH',
    '/api/spaces/:space/members/:user',
    (c) => {
      const s = spaceOf(c)
      const row = direct(c.data, s).find((m) => m.userId === c.params.user)
      if (!row) throw new Refusal(404, 'not_found', 'not a member')
      row.role = c.body.role
      syncInfo(c.data, s)
      return memberOf(c.data, s, row, false)
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/members/:user',
    (c) => {
      const s = spaceOf(c)
      const rows = direct(c.data, s)
      const row = rows.find((m) => m.userId === c.params.user)
      if (row) remove(rows, row)
      // Leaving: the space is no longer the person's.
      if (c.params.user === meOf(c.data)?.id) c.data.spaces = c.data.spaces.filter((x) => x !== s)
    },
  ],

  // groups in a space
  [
    'POST',
    '/api/spaces/:space/groups',
    (c): GroupMember => {
      const s = spaceOf(c)
      const row = { groupId: c.body.groupId as string, role: c.body.role as SpaceRole }
      groupsIn(c.data, s).push(row)
      syncInfo(c.data, s)
      return groupMember(c.data, row)
    },
  ],
  [
    'PATCH',
    '/api/spaces/:space/groups/:group',
    (c): GroupMember => {
      const s = spaceOf(c)
      const row = groupsIn(c.data, s).find((g) => g.groupId === c.params.group)
      if (!row) throw new Refusal(404, 'not_found', 'not in the space')
      row.role = c.body.role
      syncInfo(c.data, s)
      return groupMember(c.data, row)
    },
  ],
  [
    'DELETE',
    '/api/spaces/:space/groups/:group',
    (c) => {
      const s = spaceOf(c)
      c.data.area.spaceGroups[s.info.id] = groupsIn(c.data, s).filter((g) => g.groupId !== c.params.group)
      syncInfo(c.data, s)
    },
  ],

  // finding people and groups
  [
    'GET',
    '/api/spaces/:space/people',
    (c) => ({
      people: c.data.auth.users
        .filter((u) => sameTenant(c.data, u) && matches(u, c.query.get('q') ?? ''))
        .slice(0, 20)
        .map(personRef),
    }),
  ],
  [
    'GET',
    '/api/people',
    (c) => ({
      people: c.data.auth.users
        .filter((u) => sameTenant(c.data, u) && matches(u, c.query.get('q') ?? ''))
        .slice(0, 20)
        .map(personRef),
    }),
  ],
  [
    'GET',
    '/api/groups',
    (c) => ({
      groups: c.data.area.groups
        .filter((g) => g.name.toLowerCase().includes((c.query.get('q') ?? '').toLowerCase()))
        .slice(0, 20)
        .map(({ id, name, source }) => ({ id, name, source })),
    }),
  ],
]
