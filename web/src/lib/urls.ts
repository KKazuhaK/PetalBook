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

// The administration (administrators only, accounts mode): its own pages, kept
// apart from the personal settings.
export const ADMIN_SECTIONS = ['users', 'spaces', 'daily', 'security', 'status'] as const
export type AdminSection = (typeof ADMIN_SECTIONS)[number]
export const isAdminSection = (v: unknown): v is AdminSection => ADMIN_SECTIONS.some((s) => s === v)
export const adminPath = (section?: AdminSection) => (section ? `/admin/${section}` : '/admin')
