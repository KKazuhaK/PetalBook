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
  // The first time the home page was opened on that day (it stays true all day, so the greeting does not change
  // under the person's eyes when they come back to it).
  first: boolean
  // Days since the visit before this one; 0 when there was none or this is not the first of the day.
  absentDays: number
}

export const visitKey = (user: string | undefined, space: string) => `pb.home.visit.${user ?? 'local'}.${space}`

// Notes that the home page is being opened `today` and says what kind of visit it is. Writing is part of reading:
// the first call of a day records the day, and every later call that day gives the same answer.
export function noteVisit(key: string, today: string): Visit {
  let stored: Visit | null = null
  try {
    const raw = JSON.parse(localStorage.getItem(key) ?? 'null') as Partial<Visit> | null
    if (raw && typeof raw.date === 'string' && typeof raw.first === 'boolean' && typeof raw.absentDays === 'number') stored = raw as Visit
  } catch {
    stored = null
  }
  if (stored?.date === today) return stored
  // A clock that went back is not a long absence.
  const absentDays = stored ? Math.max(0, daysBetween(stored.date, today)) : 0
  const visit: Visit = { date: today, first: true, absentDays }
  try {
    localStorage.setItem(key, JSON.stringify(visit))
  } catch {
    // Not kept: the next visit looks like the first of its day, which is only a greeting.
  }
  return visit
}
