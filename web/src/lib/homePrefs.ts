// What the home page remembers in this browser: whether the greeting may vary
// (a preference), and when the person last came, which decides greetings like
// "first time today" and "long time no see". Storage can be blocked, so every
// access is guarded and the page works without it.

export const VARIETY_KEY = 'pb.home.variety'

export function getVariety(): boolean {
  try {
    return localStorage.getItem(VARIETY_KEY) !== 'off'
  } catch {
    return true
  }
}

export function setVariety(on: boolean) {
  try {
    localStorage.setItem(VARIETY_KEY, on ? 'on' : 'off')
  } catch {
    // Not kept; the choice lasts until the page is left.
  }
}

// The day as `2026-11-23`, where the browser is.
export function localDay(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

// Whole days between two such days.
export function daysBetween(from: string, to: string): number {
  const ms = (s: string) => {
    const [y, m, d] = s.split('-').map(Number)
    return Date.UTC(y, m - 1, d)
  }
  return Math.round((ms(to) - ms(from)) / 86_400_000)
}

export interface Visit {
  // The day this was worked out for.
  date: string
  // The first time the home page was opened on that day. Only the call that makes the record gets true.
  first: boolean
  // Days since the visit before this one; 0 when there was none or this is not the first of the day.
  absentDays: number
  // The page has been opened today (stored true from the first call on).
  seen?: boolean
  // What the greeting said today, with what it went by, so coming back to the page does not change it.
  greeting?: { text: string; sig: string }
}

export const visitKey = (user: string | undefined, space: string) => `pb.home.visit.${user ?? 'local'}.${space}`

function readVisit(key: string): Visit | null {
  try {
    const raw = JSON.parse(localStorage.getItem(key) ?? 'null') as Partial<Visit> | null
    if (raw && typeof raw.date === 'string' && typeof raw.first === 'boolean' && typeof raw.absentDays === 'number')
      return raw as Visit
  } catch {
    // Unreadable: as if there were none.
  }
  return null
}

function writeVisit(key: string, v: Visit) {
  try {
    localStorage.setItem(key, JSON.stringify(v))
  } catch {
    // Not kept: the next visit looks like the first of its day, which is only a greeting.
  }
}

// Notes that the home page is being opened `today` and says what kind of visit it is. Writing is part of reading:
// the first call of a day records the day and is the only one that is "the first visit"; every later call that
// day says it is not, and carries what the greeting said.
export function noteVisit(key: string, today: string): Visit {
  const stored = readVisit(key)
  if (stored?.date === today) return { ...stored, first: stored.seen === true ? false : stored.first }
  // A clock that went back is not a long absence.
  const absentDays = stored ? Math.max(0, daysBetween(stored.date, today)) : 0
  const visit: Visit = { date: today, first: true, absentDays, seen: true }
  writeVisit(key, visit)
  return visit
}

// Keeps what the greeting said today (only if the visit of `today` is the one on record).
export function rememberGreeting(key: string, today: string, greeting: NonNullable<Visit['greeting']>) {
  const stored = readVisit(key)
  if (stored?.date === today) writeVisit(key, { ...stored, greeting })
}

// --- the person's own writing ---------------------------------------------------------

// The pages this browser saved, per person and space (the newest few, one entry per page, with when it last was):
// the greeting's "just wrote" goes by these and not by the space's recent pages, which in a shared space are other
// people's too. One page saved ten times is one page written.
export const wroteKey = (user: string | undefined, space: string) => `pb.home.wrote.${user ?? 'local'}.${space}`
const WROTE_KEEP = 20

export interface Wrote {
  id: string
  at: Date
}

export function noteWrote(key: string, id: string, at: Date = new Date()) {
  try {
    const list = loadWroteEntries(key).filter((w) => w.id !== id)
    localStorage.setItem(
      key,
      JSON.stringify(
        [{ id, at }, ...list]
          .sort((a, b) => b.at.getTime() - a.at.getTime())
          .slice(0, WROTE_KEEP)
          .map((w) => ({ id: w.id, at: w.at.getTime() })),
      ),
    )
  } catch {
    // Not kept: the greeting just does not notice this one.
  }
}

// Newest first. A bare number (the older format) is a page of its own.
export function loadWroteEntries(key: string): Wrote[] {
  try {
    const raw = JSON.parse(localStorage.getItem(key) ?? '[]') as unknown
    if (!Array.isArray(raw)) return []
    const out: Wrote[] = []
    for (const e of raw as unknown[]) {
      const n =
        typeof e === 'number'
          ? e
          : e && typeof e === 'object' && typeof (e as { at?: unknown }).at === 'number'
            ? (e as { at: number }).at
            : NaN
      const id =
        typeof e === 'object' && e && typeof (e as { id?: unknown }).id === 'string'
          ? (e as { id: string }).id
          : `legacy-${n}`
      const at = new Date(n)
      if (!Number.isNaN(at.getTime())) out.push({ id, at })
    }
    return out.sort((a, b) => b.at.getTime() - a.at.getTime())
  } catch {
    return []
  }
}

// The times of the distinct pages, newest first.
export const loadWrote = (key: string): Date[] => loadWroteEntries(key).map((w) => w.at)
