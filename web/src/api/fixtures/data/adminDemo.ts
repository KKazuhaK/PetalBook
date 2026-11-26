// What `npm run dev:mock` adds in accounts mode so the administration console has something to show: two more people,
// a group, a plan, a space the administrator only manages, a token and a few audit rows. The tests do not use it (each
// builds the scene it needs).

import { makeProvider } from './admin.ts'
import { emptySpace, type FixtureData } from './model.ts'
import { DEMO_MEMBER } from './people.ts'

export function seedAdminDemo(d: FixtureData) {
  const at = '2026-11-20T08:00:00Z'
  const me = d.auth.users[0]
  const carol = { ...DEMO_MEMBER }
  const dave = {
    ...DEMO_MEMBER,
    id: '01FXMEMBR2',
    username: 'dave',
    name: '戴夫',
    email: 'dave@example.test',
    passkeys: 1,
  }
  d.auth.users.push(carol, dave)
  d.admin.people[dave.id] = {
    sessions: [{ id: 's1', userAgent: 'Firefox on Linux', method: 'password', created: at, lastUsed: at }],
    identities: [],
    passkeys: [{ id: 'k1', name: '笔记本电脑', created: at, lastUsed: null, synced: false }],
  }
  d.admin.groups.push({
    id: '01GPDEMO01',
    name: '编辑组',
    description: '可以改共享空间的人',
    source: 'local',
    people: 0,
    spaces: 0,
    mappedFrom: [],
    created: at,
    updated: at,
    members: [carol.id],
    spaceRefs: [],
  })
  d.admin.plans.push({
    slug: 'team',
    name: '团队版',
    description: '小团队用',
    rules: { 'quota.people': { value: 20, lower: { mode: 'lock' } } },
    tenants: 0,
  })
  // The two providers the sign-in page offers.
  d.admin.providers.push(
    makeProvider(
      {
        slug: 'company',
        kind: 'oidc',
        name: '示例单点登录',
        oidc: { issuer: 'https://sso.example.test', clientId: 'demo', clientSecret: 'x' },
      },
      at,
      1,
    ),
    makeProvider(
      {
        slug: 'campus',
        kind: 'saml',
        name: 'Campus login',
        saml: { metadataUrl: 'https://idp.example.test/metadata' },
      },
      at,
      2,
    ),
  )
  const shared = emptySpace({ id: '01SPDEMO01', name: '团队资料', role: '', permissions: [] })
  d.admin.otherSpaces.push(shared)
  d.admin.spaceExtra[shared.info.id] = {
    owner: { id: carol.id, username: carol.username, name: carol.name },
    members: 3,
  }
  d.admin.tokens.push({
    id: '01TKDEMO01',
    name: '同步脚本',
    prefix: 'pb_demo',
    access: 'read',
    spaces: [],
    created: at,
    expires: null,
    lastUsed: at,
    revoked: null,
    state: 'active',
    owner: { id: carol.id, username: carol.username, name: carol.name },
  })
  for (const [i, kind] of ['login', 'space_created', 'user_changed'].entries())
    d.admin.audit.push({
      id: `A0DEMO00${i}`,
      at,
      kind,
      category: kind === 'login' ? 'signins' : 'admin',
      capacity: kind === 'login' ? null : 'site_admin',
      actor: me.id,
      user: carol.id,
      username: carol.username,
      ip: '203.0.113.7',
      detail: {},
    })
}
