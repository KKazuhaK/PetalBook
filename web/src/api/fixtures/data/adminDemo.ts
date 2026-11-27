// What `npm run dev:mock` adds in accounts mode so the administration console has something to show: two more people,
// a group, a plan, a space the administrator only manages, a token and a few audit rows. The tests do not use it (each
// builds the scene it needs).

import type { TenantDomain } from '../../types.ts'
import { DEFAULT_TENANT, makeProvider } from './admin.ts'
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
    links: [],
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
  // The default tenant's email domains, one in each state; acme.example's record is published, so "Check now" finds it.
  const day = 86_400_000
  const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
  const t0 = Date.parse(at)
  const base = (key: string, display = key): TenantDomain => ({
    domain: key,
    display,
    state: 'unverified',
    verifiedAt: null,
    missingSince: null,
    lapsedAt: null,
    checkedAt: null,
    nextCheckAt: null,
    codeExpires: iso(t0 + 7 * day),
    codeExpired: false,
    lastCheck: null,
    nextCheckAllowedAt: null,
    restrictOrgs: false,
    provider: null,
    join: null,
    recordName: `_petalbook.${key}`,
    code: null,
    records: null,
    created: at,
  })
  const found = { at, by: 'daily' as const, result: 'found' as const, via: 'txt' as const, seen: [], error: '' }
  d.admin.domains[DEFAULT_TENANT] = [
    {
      ...base('acme.example'),
      state: 'verified',
      verifiedAt: at,
      checkedAt: at,
      nextCheckAt: iso(t0 + day),
      lastCheck: found,
      restrictOrgs: true,
    },
    {
      ...base('xn--bcher-kva.example', 'bücher.example'),
      state: 'missing',
      verifiedAt: at,
      missingSince: at,
      checkedAt: at,
      nextCheckAt: iso(t0 + day),
      lastCheck: { ...found, result: 'missing', via: '' },
    },
    {
      ...base('old-brand.example'),
      state: 'lapsed',
      lapsedAt: at,
      codeExpired: true,
      lastCheck: { ...found, result: 'wrong_value', via: '', seen: ['petalbook-verify=an-old-code-from-last-year'] },
    },
    { ...base('a-rather-long-subdomain-name.engineering.example-company.co.uk') },
  ]
  d.admin.siteHost = 'notes.example.test'
  d.admin.publishedDomains.push('acme.example', 'a-rather-long-subdomain-name.engineering.example-company.co.uk')
}
