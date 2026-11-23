// What the home page says by way of hello. It is all worked out in the browser, from the clock and what the page
// already knows, with no request; the sentences themselves are in the language files (i18n/home), so this only
// chooses. The choice goes by, first match wins:
//   a festival or a solar term of the day, the day the person registered (a year or more ago),
//   a long absence, having just written something, having written several today,
//   and otherwise the part of the day, with the weekday and "first time today" adding sentences to pick from.
// Within one kind the sentence is taken by the date, so it stays the same all day and changes the next.

import { CALENDAR_YEARS } from './calendarTable'
import { localDay } from './homePrefs'
import type { GreetMessages } from '@/i18n/homeTypes'

export type Bucket = 'dawn' | 'earlyMorning' | 'morning' | 'afternoon' | 'evening' | 'lateNight'

// Late night 22:00 to 01:59, the small hours 02:00 to 04:59, then the morning begins.
export function bucketOf(hour: number): Bucket {
  if (hour >= 22 || hour < 2) return 'lateNight'
  if (hour < 5) return 'dawn'
  if (hour < 8) return 'earlyMorning'
  if (hour < 12) return 'morning'
  if (hour < 18) return 'afternoon'
  return 'evening'
}

// Whole days from the epoch to the date where the browser is: what the rotation counts.
export function dayNumber(d: Date): number {
  return Math.floor(Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()) / 86_400_000)
}

// Festivals on fixed days of the Gregorian calendar.
const FIXED: Record<string, string> = { '0101': 'newYear', '0214': 'valentine', '0501': 'laborDay', '0601': 'childrensDay', '1001': 'nationalDay', '1225': 'christmas' }

export const FESTIVALS = ['newYear', 'valentine', 'laborDay', 'childrensDay', 'nationalDay', 'christmas', 'newYearsEve', 'springFestival', 'lantern', 'qingming', 'dragonBoat', 'qixi', 'midAutumn', 'doubleNinth', 'laba', 'dongzhi'] as const
export const TERMS = [
  'xiaohan', 'dahan', 'lichun', 'yushui', 'jingzhe', 'chunfen', 'qingming', 'guyu', 'lixia', 'xiaoman', 'mangzhong', 'xiazhi',
  'xiaoshu', 'dashu', 'liqiu', 'chushu', 'bailu', 'qiufen', 'hanlu', 'shuangjiang', 'lidong', 'xiaoxue', 'daxue', 'dongzhi',
] as const

const parsed = new Map<number, Map<string, string[]>>()
function yearTable(year: number): Map<string, string[]> | undefined {
  const known = parsed.get(year)
  if (known) return known
  const raw = CALENDAR_YEARS[year]
  if (raw === undefined) return undefined
  const m = new Map<string, string[]>()
  for (const entry of raw.split(' ')) {
    const [day, names] = entry.split('=')
    m.set(day, names.split('+'))
  }
  parsed.set(year, m)
  return m
}

// Whether the table knows the year at all (it covers ten years from 2026).
export const hasCalendar = (year: number) => CALENDAR_YEARS[year] !== undefined

// The festivals and solar terms of a day, festivals first; a festival that is also a term (Qingming, the winter
// solstice) is named once.
export function namesOn(d: Date): string[] {
  const key = String(d.getMonth() + 1).padStart(2, '0') + String(d.getDate()).padStart(2, '0')
  const names = [...(FIXED[key] ? [FIXED[key]] : []), ...(yearTable(d.getFullYear())?.get(key) ?? [])]
  const fest = names.filter((n) => (FESTIVALS as readonly string[]).includes(n))
  const terms = names.filter((n) => !fest.includes(n))
  return [...fest, ...terms]
}

export interface GreetingInput {
  now: Date
  // Left out for a name that is not known (single-user mode).
  name?: string
  // When the account was made.
  registered?: Date | null
  // This is the first time the home page was opened today.
  firstToday: boolean
  // Days since the visit before today's first (0 when there was none).
  absentDays: number
  // When the person last saved a page themselves (not anyone else in the space), newest first.
  recentUpdated: Date[]
  // False: only the plain good morning, good afternoon, good evening.
  variety: boolean
}

export const LONG_ABSENCE_DAYS = 14
export const JUST_WROTE_MINUTES = 30
export const WROTE_SEVERAL = 3

// Fills in what a sentence has room for; with no name the comma and the placeholder before it go too.
export function fill(sentence: string, vars: Record<string, string | number | undefined>): string {
  let out = sentence
  for (const [k, v] of Object.entries(vars)) {
    if (v === undefined || v === '') out = out.replace(new RegExp(`^\\{${k}\\}\\s*[，,、]\\s*`), '').replace(new RegExp(`\\s*[，,、]?\\s*\\{${k}\\}`, 'g'), '')
    else out = out.replaceAll(`{${k}}`, () => String(v))
  }
  return out
}

// The date's turn: one step a day, and one more at each week, so a pool as long as the week does not give the same
// sentence on every Monday.
const pickBy = <T>(list: readonly T[], day: number): T => {
  const turn = day + Math.floor(day / 7)
  return list[((turn % list.length) + list.length) % list.length]
}

export function greetingFor(input: GreetingInput, g: GreetMessages): string {
  const { now, name } = input
  const day = dayNumber(now)
  const hour = now.getHours()
  const plain = hour >= 18 || hour < 5 ? g.simple.evening : hour < 12 ? g.simple.morning : g.simple.afternoon
  if (!input.variety) return fill(plain, { name })

  const names = namesOn(now)
  for (const n of names) {
    const sentences = (g.festivals as Record<string, string[] | undefined>)[n]
    if (sentences?.length) return fill(pickBy(sentences, day), { name })
  }
  const term = names.find((n) => (TERMS as readonly string[]).includes(n))
  if (term) return fill(pickBy(g.termGreetings, day), { name, term: (g.terms as Record<string, string>)[term] })

  const r = input.registered
  if (r && r.getMonth() === now.getMonth() && r.getDate() === now.getDate() && now.getFullYear() > r.getFullYear()) {
    return fill(pickBy(g.anniversary, day), { name, years: now.getFullYear() - r.getFullYear() })
  }

  if (input.firstToday && input.absentDays >= LONG_ABSENCE_DAYS) return fill(pickBy(g.situations.longAbsence, day), { name, days: input.absentDays })

  const newest = input.recentUpdated[0]
  if (newest && now.getTime() - newest.getTime() >= 0 && now.getTime() - newest.getTime() <= JUST_WROTE_MINUTES * 60_000) return fill(pickBy(g.situations.justWrote, day), { name })
  const today = localDay(now)
  const writtenToday = input.recentUpdated.filter((d) => localDay(d) === today).length
  if (writtenToday >= WROTE_SEVERAL) return fill(pickBy(g.situations.manyToday, day), { name, n: writtenToday })

  // Everything else is the part of the day, with more to pick from when the day itself is something.
  const pool: string[] = [...g.buckets[bucketOf(hour)]]
  const weekday = now.getDay()
  if (weekday === 1 && hour >= 5 && hour < 18) pool.push(...g.weekdays.monday)
  else if (weekday === 5 && hour >= 5) pool.push(...g.weekdays.friday)
  else if (weekday === 0 || weekday === 6) pool.push(...g.weekdays.weekend)
  if (input.firstToday) pool.push(...g.situations.firstToday)
  return fill(pickBy(pool, day), { name })
}
