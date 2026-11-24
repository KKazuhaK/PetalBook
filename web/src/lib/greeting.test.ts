// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import {
  bucketOf,
  dayNumber,
  fill,
  FESTIVALS,
  greetingFor,
  greetingSignature,
  hasCalendar,
  namesOn,
  TERMS,
  type GreetingInput,
} from './greeting'
import { CALENDAR_YEARS } from './calendarTable'
import {
  daysBetween,
  getVariety,
  loadWrote,
  localDay,
  noteVisit,
  noteWrote,
  setVariety,
  visitKey,
  wroteKey,
} from './homePrefs'
import { homeEn } from '@/i18n/home/en'
import { homeZhCN } from '@/i18n/home/zh-CN'

const zh = homeZhCN.greet
const en = homeEn.greet
// 2026-11-24 is a Tuesday; local time, as the browser has it.
const at = (y: number, m: number, d: number, h = 9, min = 0) => new Date(y, m - 1, d, h, min)
const base = (over: Partial<GreetingInput> = {}): GreetingInput => ({
  now: at(2026, 11, 24, 9),
  name: '小花',
  firstToday: false,
  absentDays: 0,
  recentUpdated: [],
  variety: true,
  ...over,
})
const has = (list: string[], text: string) =>
  list.some((s) => fill(s, { name: '小花', days: 20, n: 3, years: 2, term: '' }) === text)
const anyOf = (lists: string[][], text: string, vars: Record<string, string | number | undefined> = { name: '小花' }) =>
  lists.some((l) => l.some((s) => fill(s, vars) === text))

describe('the table of festivals and solar terms', () => {
  it('covers ten years from 2026, each with all 24 terms', () => {
    for (let y = 2026; y <= 2036; y++) {
      expect(hasCalendar(y)).toBe(true)
      const names =
        Object.values(CALENDAR_YEARS[y]).join('') &&
        CALENDAR_YEARS[y].split(' ').flatMap((e) => e.split('=')[1].split('+'))
      for (const term of TERMS) expect(names, `${y} ${term}`).toContain(term)
      for (const f of ['springFestival', 'newYearsEve', 'lantern', 'dragonBoat', 'qixi', 'midAutumn', 'doubleNinth'])
        expect(names, `${y} ${f}`).toContain(f)
    }
    expect(hasCalendar(2025)).toBe(false)
    expect(hasCalendar(2037)).toBe(false)
  })
  it('knows the days that are known', () => {
    expect(namesOn(at(2026, 2, 17))).toContain('springFestival')
    expect(namesOn(at(2027, 2, 6))).toContain('springFestival')
    expect(namesOn(at(2030, 2, 3))).toContain('springFestival')
    expect(namesOn(at(2026, 9, 25))).toContain('midAutumn')
    expect(namesOn(at(2026, 6, 19))).toContain('dragonBoat')
    expect(namesOn(at(2026, 4, 5))).toEqual(['qingming'])
    expect(namesOn(at(2026, 12, 22))).toEqual(['dongzhi'])
    expect(namesOn(at(2026, 10, 1))).toEqual(['nationalDay'])
    expect(namesOn(at(2026, 1, 1))).toContain('newYear')
    expect(namesOn(at(2026, 11, 24))).toEqual([])
    // Outside the table the lunar days and the terms are not known; the fixed festivals still are.
    expect(namesOn(at(2040, 2, 5))).toEqual([])
    expect(namesOn(at(2040, 12, 25))).toEqual(['christmas'])
  })
  it('has a sentence set for every festival in both languages', () => {
    for (const f of FESTIVALS) {
      expect((zh.festivals as Record<string, string[]>)[f]?.length, f).toBeGreaterThan(0)
      expect((en.festivals as Record<string, string[]>)[f]?.length, f).toBeGreaterThan(0)
    }
    for (const term of TERMS) {
      expect((zh.terms as Record<string, string>)[term], term).toBeTruthy()
      expect((en.terms as Record<string, string>)[term], term).toBeTruthy()
    }
  })
})

describe('parts of the day', () => {
  it('cuts the day at the hours the design names', () => {
    const cases: [number, string][] = [
      [0, 'lateNight'],
      [1, 'lateNight'],
      [2, 'dawn'],
      [4, 'dawn'],
      [5, 'earlyMorning'],
      [7, 'earlyMorning'],
      [8, 'morning'],
      [11, 'morning'],
      [12, 'afternoon'],
      [17, 'afternoon'],
      [18, 'evening'],
      [21, 'evening'],
      [22, 'lateNight'],
      [23, 'lateNight'],
    ]
    for (const [h, b] of cases) expect(bucketOf(h), `${h}:00`).toBe(b)
  })
  it('says something of its own at each part of the day', () => {
    const buckets = zh.buckets
    for (const [hour, key] of [
      [3, 'dawn'],
      [6, 'earlyMorning'],
      [9, 'morning'],
      [14, 'afternoon'],
      [19, 'evening'],
      [23, 'lateNight'],
    ] as const) {
      // A Wednesday, which adds nothing of its own.
      const text = greetingFor(base({ now: at(2026, 11, 25, hour) }), zh)
      expect(anyOf([buckets[key]], text), `${hour}h: ${text}`).toBe(true)
    }
  })
  it('mentions the late night and the small hours in their own words', () => {
    expect(greetingFor(base({ now: at(2026, 11, 25, 23) }), zh)).toMatch(/夜|深夜/)
    expect(greetingFor(base({ now: at(2026, 11, 25, 3) }), zh)).toMatch(/睡|凌晨|天快亮/)
  })
})

describe('what is chosen first', () => {
  it('a festival beats everything else, a solar term comes after festivals', () => {
    const t = greetingFor(
      base({ now: at(2026, 2, 17), firstToday: true, absentDays: 40, recentUpdated: [at(2026, 2, 17, 8, 50)] }),
      zh,
    )
    expect(anyOf([zh.festivals.springFestival], t)).toBe(true)
    const term = greetingFor(base({ now: at(2026, 11, 22) }), zh)
    expect(term).toContain('小雪')
    const english = greetingFor(base({ now: at(2026, 11, 22), name: 'Ann' }), en)
    expect(english).toContain('Minor Snow')
    expect(greetingFor(base({ now: at(2026, 12, 22) }), zh)).toMatch(/冬至/)
  })
  it('a festival is greeted in the other language too', () => {
    const t = greetingFor(base({ now: at(2026, 9, 25), name: 'Ann' }), en)
    expect(anyOf([en.festivals.midAutumn], t, { name: 'Ann' })).toBe(true)
  })
  it('the registration anniversary, with the years', () => {
    const t = greetingFor(base({ now: at(2026, 11, 24), registered: new Date(2024, 10, 24, 15) }), zh)
    expect(anyOf([zh.anniversary], t, { name: '小花', years: 2 })).toBe(true)
    expect(t).toContain('2')
    // The same day in the year of registering is no anniversary.
    const same = greetingFor(base({ now: at(2026, 11, 24), registered: new Date(2026, 10, 24, 8) }), zh)
    expect(anyOf([zh.anniversary], same, { name: '小花', years: 0 })).toBe(false)
  })
  it('a long absence is noticed on the first visit, not a short one', () => {
    const t = greetingFor(base({ firstToday: true, absentDays: 20 }), zh)
    expect(anyOf([zh.situations.longAbsence], t, { name: '小花', days: 20 })).toBe(true)
    expect(t).toContain('20')
    const short = greetingFor(base({ firstToday: true, absentDays: 3 }), zh)
    expect(anyOf([zh.situations.longAbsence], short, { name: '小花', days: 3 })).toBe(false)
  })
  it('having just written something', () => {
    const now = at(2026, 11, 24, 15)
    const t = greetingFor(base({ now, recentUpdated: [new Date(now.getTime() - 10 * 60_000)] }), zh)
    expect(anyOf([zh.situations.justWrote], t)).toBe(true)
    const old = greetingFor(base({ now, recentUpdated: [new Date(now.getTime() - 2 * 3600_000)] }), zh)
    expect(anyOf([zh.situations.justWrote], old)).toBe(false)
  })
  it('having written several pages today', () => {
    const now = at(2026, 11, 24, 20)
    const pages = [at(2026, 11, 24, 10), at(2026, 11, 24, 12), at(2026, 11, 24, 14), at(2026, 11, 23, 9)]
    const t = greetingFor(base({ now, recentUpdated: pages }), zh)
    expect(anyOf([zh.situations.manyToday], t, { name: '小花', n: 3 })).toBe(true)
    expect(t).toContain('3')
    expect(
      anyOf([zh.situations.manyToday], greetingFor(base({ now, recentUpdated: pages.slice(0, 2) }), zh), {
        name: '小花',
        n: 2,
      }),
    ).toBe(false)
  })
  it('the first visit of the day, and the weekdays, add sentences to the part of the day', () => {
    // Over a run of Mondays and first visits the extra sentences turn up and the part of the day's still do.
    const seen = new Set<string>()
    for (let i = 0; i < 12; i++) seen.add(greetingFor(base({ now: at(2026, 11, 23 + 7 * i, 9), firstToday: true }), zh))
    expect([...seen].some((s) => anyOf([zh.weekdays.monday], s))).toBe(true)
    expect([...seen].some((s) => anyOf([zh.situations.firstToday], s))).toBe(true)
    expect([...seen].some((s) => anyOf([zh.buckets.morning], s))).toBe(true)
    // Weekends and Fridays have their own.
    const week = new Set<string>()
    for (let i = 0; i < 30; i++) week.add(greetingFor(base({ now: at(2026, 12, 5 + 7 * i + (i % 2), 9) }), zh))
    expect([...week].some((s) => anyOf([zh.weekdays.weekend], s))).toBe(true)
  })
})

describe('rotation', () => {
  it('stays the same all day within a part of the day, and moves on with the date', () => {
    const a = greetingFor(base({ now: at(2026, 11, 25, 9, 0) }), zh)
    expect(greetingFor(base({ now: at(2026, 11, 25, 9, 1) }), zh)).toBe(a)
    expect(greetingFor(base({ now: at(2026, 11, 25, 11, 59) }), zh)).toBe(a)
    const days = new Set<string>()
    for (let d = 25; d < 40; d++) days.add(greetingFor(base({ now: new Date(2026, 10, d, 9) }), zh))
    expect(days.size).toBeGreaterThan(1)
    expect(dayNumber(at(2026, 11, 25, 0, 5))).toBe(dayNumber(at(2026, 11, 25, 23, 55)))
    expect(dayNumber(at(2026, 11, 26))).toBe(dayNumber(at(2026, 11, 25)) + 1)
  })
  it('with variety off is only the plain greeting', () => {
    expect(greetingFor(base({ now: at(2026, 2, 17, 9), variety: false }), zh)).toBe('早上好，小花')
    expect(greetingFor(base({ now: at(2026, 11, 25, 14), variety: false }), zh)).toBe('下午好，小花')
    expect(greetingFor(base({ now: at(2026, 11, 25, 20), variety: false, firstToday: true, absentDays: 50 }), zh)).toBe(
      '晚上好，小花',
    )
    expect(greetingFor(base({ now: at(2026, 11, 25, 3), variety: false }), zh)).toBe('晚上好，小花')
    expect(greetingFor(base({ now: at(2026, 11, 25, 9), variety: false, name: 'Ann' }), en)).toBe('Good morning, Ann')
  })
  it('leaves the name and its comma out when there is none', () => {
    expect(greetingFor(base({ now: at(2026, 11, 25, 9), variety: false, name: undefined }), zh)).toBe('早上好')
    expect(greetingFor(base({ now: at(2026, 11, 25, 9), variety: false, name: undefined }), en)).toBe('Good morning')
    expect(fill('{name}，你回来啦', { name: undefined })).toBe('你回来啦')
    expect(fill('Hi {name}!', { name: '' })).toBe('Hi!')
    expect(has(zh.buckets.morning, '上午好')).toBe(false)
  })
  it('never leaves a placeholder behind, in any language and on any day', () => {
    for (const g of [zh, en]) {
      for (let day = 0; day < 400; day++) {
        for (const hour of [1, 3, 6, 9, 14, 19, 23]) {
          for (const name of ['小花', undefined]) {
            const now = new Date(2026, 0, 1 + day, hour)
            const text = greetingFor(
              base({
                now,
                name,
                firstToday: day % 2 === 0,
                absentDays: day % 3 === 0 ? 30 : 0,
                registered: new Date(2020, 5, 5),
              }),
              g,
            )
            expect(text, `${localDay(now)} ${hour}`).not.toMatch(/[{}]/)
            expect(text.trim()).not.toBe('')
          }
        }
      }
    }
  })
})

describe('filling in a sentence', () => {
  it('takes a name with dollar signs as it is', () => {
    expect(fill('你好，{name}', { name: "$& $' $1 $$" })).toBe("你好，$& $' $1 $$")
  })
  it('the anniversary says full years, also the first', () => {
    for (const sentence of zh.anniversary) expect(fill(sentence, { name: '小花', years: 1 })).not.toMatch(/第 1 年/)
    expect(zh.anniversary).toContain('{name}，你来到这里满 {years} 年了')
  })
})

describe('what a kept sentence goes by', () => {
  const base = { now: at(2026, 11, 24, 9, 0), name: 'Ann', lang: 'zh-CN', variety: true, wrote: [] as Date[] }
  const sig = (over: Partial<typeof base> = {}) => greetingSignature({ ...base, ...over })
  const wrote = (n: number, h = 8) => Array.from({ length: n }, (_, i) => at(2026, 11, 24, h, i))
  // Same inputs but for the one that changes: the signature must differ (so the sentence is worked out again).
  type Over = Partial<typeof base>
  it.each<[string, Over]>([
    ['the part of the day', { now: at(2026, 11, 24, 21, 0) }],
    ['the part of the day, across a boundary', { now: at(2026, 11, 24, 12, 0) }],
    ['the language', { lang: 'en' }],
    ['the name', { name: 'Bo' }],
    ['no name', { name: undefined }],
    ['the variety', { variety: false }],
    ['a page written a minute ago', { wrote: [at(2026, 11, 24, 8, 59)] }],
    ['a page written earlier today', { wrote: [at(2026, 11, 24, 7, 0)] }],
    ['three pages written today', { wrote: wrote(3) }],
    ['a festival of the date', { now: at(2026, 12, 25, 9, 0) }],
    ['another date', { now: at(2026, 11, 25, 9, 0) }],
  ])('differs with %s', (_n, over) => {
    expect(sig(over)).not.toBe(sig())
  })
  it.each<[string, Over]>([
    ['minutes within the same part of the day', { now: at(2026, 11, 24, 10, 41) }],
    ['a save of yesterday', { wrote: [at(2026, 11, 23, 9, 0)] }],
  ])('does not differ with %s', (_n, over) => {
    expect(sig(over)).toBe(sig())
  })
  it('counts written pages only up to where the sentence cares', () => {
    expect(sig({ wrote: wrote(4) })).toBe(sig({ wrote: wrote(3) }))
  })
  it('"just wrote" ends thirty minutes after the save, with the same part of the day', () => {
    const w = [at(2026, 11, 24, 9, 5)]
    expect(sig({ now: at(2026, 11, 24, 9, 30), wrote: w })).not.toBe(sig({ now: at(2026, 11, 24, 9, 36), wrote: w }))
  })
})

describe('what is remembered of visits', () => {
  beforeEach(() => localStorage.clear())
  it('only the first call of a day is the first visit; the tenth is not', () => {
    const key = visitKey('u1', 's1')
    expect(noteVisit(key, '2026-11-24')).toEqual({ date: '2026-11-24', first: true, absentDays: 0, seen: true })
    for (let i = 0; i < 9; i++) expect(noteVisit(key, '2026-11-24')).toMatchObject({ first: false, seen: true })
    expect(noteVisit(key, '2026-11-25').first).toBe(true)
  })
  it('keeps what this browser wrote, newest first, per person and space', () => {
    const key = wroteKey('u1', 's1')
    noteWrote(key, 'a', new Date(2026, 10, 24, 9))
    noteWrote(key, 'b', new Date(2026, 10, 24, 10))
    expect(loadWrote(key).map((d) => d.getHours())).toEqual([10, 9])
    expect(loadWrote(wroteKey('u2', 's1'))).toEqual([])
    expect(loadWrote(wroteKey('u1', 's2'))).toEqual([])
  })
  it('counts a page once however often it is saved, and reads the older format as pages of their own', () => {
    const key = wroteKey('u1', 's1')
    for (let h = 9; h < 13; h++) noteWrote(key, 'same', new Date(2026, 10, 24, h))
    noteWrote(key, 'other', new Date(2026, 10, 24, 13))
    expect(loadWrote(key).map((d) => d.getHours())).toEqual([13, 12])
    localStorage.setItem(
      key,
      JSON.stringify([new Date(2026, 10, 24, 9).getTime(), new Date(2026, 10, 24, 10).getTime()]),
    )
    expect(loadWrote(key)).toHaveLength(2)
  })
  it('counts the days since the visit before', () => {
    const key = visitKey('u1', 's1')
    noteVisit(key, '2026-11-01')
    expect(noteVisit(key, '2026-11-24')).toEqual({ date: '2026-11-24', first: true, absentDays: 23, seen: true })
    // A clock that went back is no absence.
    expect(noteVisit(key, '2026-11-10').absentDays).toBe(0)
  })
  it('is per person and per space', () => {
    noteVisit(visitKey('u1', 's1'), '2026-11-01')
    expect(noteVisit(visitKey('u2', 's1'), '2026-11-24').absentDays).toBe(0)
    expect(noteVisit(visitKey('u1', 's2'), '2026-11-24').absentDays).toBe(0)
  })
  it('works with storage that throws', () => {
    const real = Storage.prototype.getItem
    Storage.prototype.getItem = () => {
      throw new Error('blocked')
    }
    try {
      expect(noteVisit('k', '2026-11-24')).toEqual({ date: '2026-11-24', first: true, absentDays: 0, seen: true })
      expect(getVariety()).toBe(true)
    } finally {
      Storage.prototype.getItem = real
    }
  })
  it('variety is on until turned off', () => {
    expect(getVariety()).toBe(true)
    setVariety(false)
    expect(getVariety()).toBe(false)
    setVariety(true)
    expect(getVariety()).toBe(true)
  })
  it('counts days between days', () => {
    expect(daysBetween('2026-11-24', '2026-11-24')).toBe(0)
    expect(daysBetween('2026-02-27', '2026-03-02')).toBe(3)
    expect(localDay(new Date(2026, 0, 5))).toBe('2026-01-05')
  })
})
