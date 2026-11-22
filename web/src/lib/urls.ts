// Addresses inside the app. A space id is the first segment after `/s/`, so a
// link or a reload keeps the space.

const encodePath = (path: string) => path.split('/').map(encodeURIComponent).join('/')

export function spaceUrls(space: string) {
  const base = `/s/${encodeURIComponent(space)}`
  return {
    base,
    recent: () => `${base}/recent`,
    folder: (path: string) => (path === '' ? `${base}/f` : `${base}/f/${encodePath(path)}`),
    tag: (tag: string) => `${base}/t/${encodePath(tag)}`,
    page: (id: string) => `${base}/p/${encodeURIComponent(id)}`,
    settings: () => `${base}/settings`,
    search: (q: string) => `${base}/search?q=${encodeURIComponent(q)}`,
  }
}
export type SpaceUrls = ReturnType<typeof spaceUrls>
