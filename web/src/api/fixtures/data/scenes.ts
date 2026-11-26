// Scenes a test sets up in the fixture's data before the page opens: other people, a space someone else owns, groups.
// Nothing here goes through a route, so none of it is checked.

import type { SpaceRole } from '../../types.ts'
import { newId } from '../kit.ts'
import type { FixtureServer } from '../server.ts'
import { addNotification, type GroupRec } from './area.ts'
import { emptySpace, sharedSpace, type FixtureData } from './model.ts'
import { DEMO_MEMBER, type UserRec } from './people.ts'

// Another person of the same tenant (an account that exists; nobody signs in as them).
export function addPerson(d: FixtureData, username: string, over: Partial<UserRec> = {}): UserRec {
  const person: UserRec = {
    ...structuredClone(DEMO_MEMBER),
    id: newId(d, '01FP'),
    username,
    name: `名 ${username}`,
    email: '',
    emailVerified: false,
    ...over,
  }
  d.auth.users.push(person)
  return person
}

// A group of people, by id of the people.
export function addGroup(
  d: FixtureData,
  name: string,
  members: string[] = [],
  source: GroupRec['source'] = 'local',
): GroupRec {
  const group: GroupRec = { id: newId(d, '01FG'), name, source, members }
  d.area.groups.push(group)
  return group
}

// A space someone else owns, in which the signed-in person is `role`. `owner.request` acts on it as its owner would.
export function shareSpace(fx: FixtureServer, role: SpaceRole, name = '团队', ownerName = 'sharer') {
  const d = fx.data
  const person = addPerson(d, ownerName)
  const data = sharedSpace(d, role, name)
  const space = data.info.id
  d.area.members[space] = [
    { userId: person.id, role: 'owner' },
    { userId: d.auth.users[0].id, role },
  ]
  return {
    space,
    data,
    owner: {
      id: person.id,
      request: (method: string, path: string, body?: unknown) => fx.call(method, path, body),
    },
  }
}

// Requests made behind the page's back, as the signed-in person would make them.
export const adminOf = (fx: FixtureServer) => (method: string, path: string, body?: unknown) =>
  fx.call(method, path, body)

// Another person, with a request function that acts on the server (the fixture has one caller: whoever is signed in).
export function makePerson(fx: FixtureServer, username: string) {
  const person = addPerson(fx.data, username)
  return { id: person.id, username, request: adminOf(fx) }
}

// A space the signed-in person is not a member of (an administrator can see it in the list, and may open it in a view).
export function foreignSpace(fx: FixtureServer, name = '别人的') {
  const space = emptySpace({
    id: newId(fx.data, '01FO'),
    name,
    role: '',
    directRole: '',
    permissions: [],
  })
  fx.data.spaces.push(space)
  return space
}

// The signed-in person is added to a space by someone who owns it: the space, and the notification of the kind invite.
// `at` is when (a later one is listed first).
export function invitedBy(fx: FixtureServer, who = 'sharer', role: SpaceRole = 'editor', name = '团队', at?: string) {
  const shared = shareSpace(fx, role, name, who)
  addNotification(fx.data, 'invite', shared.space, { space: name, role, by: `名 ${who}` }, at)
  return shared
}
