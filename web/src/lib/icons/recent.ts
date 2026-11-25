// The icons a person used last, newest first: in this browser only (a preference, not data of the space).
const KEY = 'pb.icons.recent'
const MAX = 12

export function readRecent(): string[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').slice(0, MAX) : []
  } catch {
    return []
  }
}

export function noteRecent(icon: string): string[] {
  const next = [icon, ...readRecent().filter((x) => x !== icon)].slice(0, MAX)
  try {
    localStorage.setItem(KEY, JSON.stringify(next))
  } catch {
    // Kept nowhere: the list is then only this visit's.
  }
  return next
}
