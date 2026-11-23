// Which spaces of which accounts have a copy on this device, and how big each is. It lives in localStorage so the
// device-wide cap can be checked, and the copies of one account found and cleared, without opening every database.
// The databases are the truth: a registry that is lost is rebuilt as stores are opened.

export const DB_PREFIX = 'petalbook-offline'
export const REGISTRY_KEY = 'petalbook.offline'

export const dbName = (user: string, space: string) => `${DB_PREFIX}:${encodeURIComponent(user)}:${encodeURIComponent(space)}`

export interface RegistryEntry {
  // Bytes of text kept, pages with text, pages in the catalogue.
  bytes: number
  texts: number
  catalog: number
  at: number
}
type Registry = Record<string, Record<string, RegistryEntry>>

function read(): Registry {
  try {
    const v = JSON.parse(localStorage.getItem(REGISTRY_KEY) ?? '{}') as unknown
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Registry) : {}
  } catch {
    return {}
  }
}
function write(r: Registry) {
  try {
    localStorage.setItem(REGISTRY_KEY, JSON.stringify(r))
  } catch {
    // No storage for the registry: the databases still work.
  }
}

export function setEntry(user: string, space: string, e: RegistryEntry) {
  const r = read()
  ;(r[user] ??= {})[space] = e
  write(r)
}
export function dropEntry(user: string, space: string) {
  const r = read()
  if (r[user]) {
    delete r[user][space]
    if (Object.keys(r[user]).length === 0) delete r[user]
    write(r)
  }
}
export const spacesOf = (user: string): string[] => Object.keys(read()[user] ?? {})
export const usersOf = (): string[] => Object.keys(read())
export const entryOf = (user: string, space: string): RegistryEntry | undefined => read()[user]?.[space]

// The text kept on this device, over every account.
export function totalBytes(): number {
  let n = 0
  for (const spaces of Object.values(read())) for (const e of Object.values(spaces)) n += e.bytes
  return n
}
// What one account keeps.
export function bytesOf(user: string): number {
  return Object.values(read()[user] ?? {}).reduce((n, e) => n + e.bytes, 0)
}
