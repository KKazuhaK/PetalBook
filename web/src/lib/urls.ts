// Addresses inside the app. A space id is the first segment after `/s/`, so a
// link or a reload keeps the space.

const encodePath = (path: string) => path.split('/').map(encodeURIComponent).join('/')

export function spaceUrls(space: string) {
  const base = `/s/${encodeURIComponent(space)}`
  return {
    base,
    // The space's home page.
    home: () => `${base}/`,
    recent: () => `${base}/recent`,
    folder: (path: string) => (path === '' ? `${base}/f` : `${base}/f/${encodePath(path)}`),
    tag: (tag: string) => `${base}/t/${encodePath(tag)}`,
    page: (id: string) => `${base}/p/${encodeURIComponent(id)}`,
    settings: () => `${base}/settings`,
    // The check page: what is wrong with the files of the space.
    check: (kind = '') => (kind === '' ? `${base}/check` : `${base}/check?kind=${encodeURIComponent(kind)}`),
    import: (folder = '') => (folder === '' ? `${base}/import` : `${base}/import?folder=${encodeURIComponent(folder)}`),
    search: (q: string) => `${base}/search?q=${encodeURIComponent(q)}`,
  }
}
export type SpaceUrls = ReturnType<typeof spaceUrls>

// The administration (administrators only, accounts mode): its own pages, kept apart from the personal settings. The
// address is `/admin/<section>/<tab>`: a section is an item of the sidebar and a tab one of its pages. A person has a
// page of their own, `/admin/user/<id>/<tab>`.
export const ADMIN_SECTIONS = [
  'users',
  'groups',
  'spaces',
  'storage',
  'security',
  'mail',
  'daily',
  'settings',
  'tenants',
  'audit',
  'status',
] as const
export type AdminSection = (typeof ADMIN_SECTIONS)[number]
export const isAdminSection = (v: unknown): v is AdminSection => ADMIN_SECTIONS.some((s) => s === v)

// With `scope` the address is in the console of that tenant (`/admin/t/<id>/...`, 3.6 step c); without it, the whole
// site's.
const adminBase = (scope?: string | null) => (scope ? `/admin/t/${encodeURIComponent(scope)}` : '/admin')
export function adminPath(section?: AdminSection, tab?: string, scope?: string | null): string {
  const base = adminBase(scope)
  if (!section) return base
  return tab ? `${base}/${section}/${tab}` : `${base}/${section}`
}
export const adminUserPath = (id: string, tab?: string, scope?: string | null): string =>
  `${adminBase(scope)}/user/${encodeURIComponent(id)}${tab ? `/${tab}` : ''}`
// A tenant's own page, in the site's console: `/admin/tenants/<id>/<tab>`.
export const adminTenantPath = (id: string, tab?: string): string =>
  `/admin/tenants/${encodeURIComponent(id)}${tab ? `/${tab}` : ''}`
