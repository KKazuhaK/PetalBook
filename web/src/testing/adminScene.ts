// Scenes for the administration's tests, made in the fixture data before the page opens (the `data` option of
// `mount`'s `fixtures`): a setting's value, a tenant, a plan, a group. They write the records the server would hold;
// they decide nothing.

import { emptySpace, type FixtureData, type SpaceData } from '@/api/fixtures/data/model'
import { DEFAULT_TENANT, makeProvider, type GroupRec, type PersonRec } from '@/api/fixtures/data/admin'
import { DEMO_MEMBER, type UserRec } from '@/api/fixtures/data/people'
import type {
  SpaceInfo,
  AdminGrant,
  AdminSpace,
  AdminToken,
  AuthProvider,
  ProviderInput,
  Plan,
  RecycleItem,
  SettingLower,
  SiteSetting,
  Tenant,
  TenantSetting,
} from '@/api/types'

export { DEFAULT_TENANT }

export function setting(d: FixtureData, key: string): SiteSetting {
  const s = d.admin.settings.find((x) => x.key === key)
  if (!s) throw new Error(`the fixtures have no setting ${key}`)
  return s
}

// The site's value of a setting (and the rule it lays on the tenants), as the settings page shows it. A boolean that is
// also a feature is switched for the pages too.
export function setSite(d: FixtureData, key: string, value: unknown, lower?: SettingLower) {
  const s = setting(d, key)
  s.site.value = value
  if (lower) s.site.lower = lower
  if (typeof value === 'boolean' && key in d.features) d.features[key] = value
}

// More than one tenant may exist.
export const tenancyOn = (d: FixtureData) => setSite(d, 'features.tenancy', true)

// What one tenant has of its own for a setting: its value, the rule it is held to, the rules for it.
export function ownRule(
  d: FixtureData,
  tenant: string,
  key: string,
  rule: Partial<NonNullable<TenantSetting['tenant']>>,
) {
  ;(d.admin.tenantRules[tenant] ??= {})[key] = { value: null, lower: null, inEffect: true, siteLocked: false, ...rule }
}

export function addTenant(d: FixtureData, slug: string, name: string, extra: Partial<Tenant> = {}): Tenant {
  const at = '2026-11-20T08:00:00Z'
  const t: Tenant = {
    id: `01TN${slug.toUpperCase().padEnd(6, 'X').slice(0, 6)}`,
    slug,
    name,
    default: false,
    disabled: false,
    registration: { open: false },
    quota: { people: null, spaces: null, bytes: null, pages: null },
    plan: null,
    origin: 'admin',
    createdBy: '',
    usage: { people: 0, spaces: 0, bytes: 0, pages: 0, measuredAt: null, over: [] },
    created: at,
    updated: at,
    ...extra,
  }
  d.admin.tenants.push(t)
  return t
}

export function addPlan(d: FixtureData, slug: string, name: string, extra: Partial<Plan> = {}): Plan {
  const p: Plan = { slug, name, description: '', rules: {}, tenants: 0, ...extra }
  d.admin.plans.push(p)
  return p
}

export function addGroup(
  d: FixtureData,
  name: string,
  source: 'local' | 'synced' = 'local',
  extra: Partial<GroupRec> = {},
): GroupRec {
  const at = '2026-11-20T08:00:00Z'
  const g: GroupRec = {
    id: `01GP${(d.admin.groups.length + 1).toString().padStart(6, '0')}`,
    name,
    description: '',
    source,
    people: 0,
    spaces: 0,
    mappedFrom: [],
    created: at,
    updated: at,
    members: [],
    spaceRefs: [],
    ...extra,
  }
  d.admin.groups.push(g)
  return g
}

// A sign-in provider of the site (an OIDC one unless said); `input` is what its form would have sent.
export function addProvider(
  d: FixtureData,
  slug: string,
  name: string,
  input: ProviderInput = {},
  extra: Partial<AuthProvider> = {},
) {
  const p = {
    ...makeProvider(
      { slug, name, oidc: { issuer: 'https://sso.example', clientId: 'x', clientSecret: 'x' }, ...input },
      '2026-11-20T08:00:00Z',
      d.admin.providers.length + 1,
    ),
    ...extra,
  }
  d.admin.providers.push(p)
  // The sign-in page lists it too.
  d.auth.providers.push({ slug, kind: p.kind, name })
  return p
}

// A space that the console lists: owned by `owner` (the signed-in person when none is named). `outside`: one the
// signed-in person only administers, so it is not in their own list of spaces.
export function addSpace(
  d: FixtureData,
  name: string,
  opts: {
    owner?: UserRec
    personal?: boolean
    outside?: boolean
    info?: Partial<SpaceInfo>
    extra?: Partial<AdminSpace>
  } = {},
): SpaceData {
  const s = emptySpace({
    id: `01SP${(d.spaces.length + d.admin.otherSpaces.length + 1).toString().padStart(6, '0')}`,
    name,
    personal: !!opts.personal,
    ...opts.info,
  })
  if (opts.outside) d.admin.otherSpaces.push(s)
  else d.spaces.push(s)
  const owner = opts.owner
  if (owner || opts.extra)
    d.admin.spaceExtra[s.info.id] = {
      ...(owner ? { owner: { id: owner.id, username: owner.username, name: owner.name } } : {}),
      ...opts.extra,
    }
  return s
}

// The signed-in person is an administrator of their organisation only: none of the site's permissions.
export function asTenantAdmin(d: FixtureData) {
  const me = d.auth.users[0]
  me.siteAdmin = false
  me.permissions = (me.permissions ?? []).filter((k) => !k.startsWith('site.'))
}

// What a person holds, as the server measured it (the usage page lists people by it).
export function measured(d: FixtureData, u: UserRec, usage: NonNullable<PersonRec['usage']>) {
  ;(d.admin.people[u.id] ??= { sessions: [], links: [], passkeys: [] }).usage = {
    measuredAt: '2026-11-20T08:00:00Z',
    ...usage,
  }
}

// A person with an account, as an invite would have made one.
export function addPerson(d: FixtureData, username: string, extra: Partial<UserRec> = {}): UserRec {
  const u: UserRec = {
    ...DEMO_MEMBER,
    id: `01FP${username.toUpperCase().padEnd(6, 'X').slice(0, 6)}`,
    username,
    name: `名 ${username}`,
    email: `${username}@example.test`,
    ...extra,
  }
  d.auth.users.push(u)
  return u
}

const ownerOf = (u: UserRec) => ({ id: u.id, username: u.username, name: u.name })

export function addToken(d: FixtureData, owner: UserRec, name: string, extra: Partial<AdminToken> = {}): AdminToken {
  const t: AdminToken = {
    id: `01TK${(d.admin.tokens.length + 1).toString().padStart(6, '0')}`,
    name,
    prefix: 'pb_abc',
    access: 'read',
    spaces: [],
    created: '2026-11-20T08:00:00Z',
    expires: null,
    lastUsed: null,
    revoked: null,
    state: 'active',
    owner: ownerOf(owner),
    ...extra,
  }
  d.admin.tokens.push(t)
  return t
}

export function addGrant(d: FixtureData, owner: UserRec, client: string): AdminGrant {
  const g: AdminGrant = {
    id: `01GR${(d.admin.grants.length + 1).toString().padStart(6, '0')}`,
    client: { name: client, id: `https://${client.replace(/\W/g, '')}.example/` },
    access: 'write',
    spaces: [],
    created: '2026-11-20T08:00:00Z',
    lastUsed: null,
    owner: ownerOf(owner),
  }
  d.admin.grants.push(g)
  return g
}

// Something in the recycle bin: a space or a person, deleted just now (the bin keeps it 30 days).
export function addRecycled(
  d: FixtureData,
  kind: 'space' | 'user',
  name: string,
  extra: Partial<RecycleItem> = {},
): RecycleItem {
  const me = d.auth.users[0]
  const at = Date.now()
  const item: RecycleItem = {
    id: `01RC${(d.admin.recycle.length + 1).toString().padStart(6, '0')}`,
    kind,
    tenant: DEFAULT_TENANT,
    space: kind === 'space' ? { id: `gone-${name}`, name, personal: false } : null,
    user: kind === 'user' ? { id: `gone-${name}`, username: name } : null,
    with: [],
    bytes: 0,
    deletedAt: new Date(at).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    deletedBy: { id: me.id, username: me.username },
    purgeAt: new Date(at + 30 * 86_400_000).toISOString().replace(/\.\d{3}Z$/, 'Z'),
    reason: '',
    state: 'ready',
    job: null,
    restorable: true,
    problems: null as unknown as string[],
    ...extra,
  }
  d.admin.recycle.push(item)
  return item
}

// A page of a space, as the server would hold it.
export function addPage(space: SpaceData, title: string, body = 'x\n', meta: Record<string, unknown> = {}) {
  const id = `01PG${(space.pages.length + 1).toString().padStart(6, '0')}`
  space.pages.push({
    id,
    folder: '',
    file: `${title}.md`,
    meta: { id, title, created: '2026-11-20', ...meta },
    body,
    updated: Date.now(),
  })
  return id
}
