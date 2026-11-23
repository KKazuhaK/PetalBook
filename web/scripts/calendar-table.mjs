// Writes src/lib/calendarTable.ts: for each year, the days of the Chinese
// festivals (from the runtime's Chinese calendar) and of the 24 solar terms
// (from the sun's longitude, in China Standard Time). Run it again to extend
// the table: `node scripts/calendar-table.mjs 2026 2036 > src/lib/calendarTable.ts`.

const [from, to] = [Number(process.argv[2] ?? 2026), Number(process.argv[3] ?? 2036)]

const TERMS = [
  ['xiaohan', 285], ['dahan', 300], ['lichun', 315], ['yushui', 330], ['jingzhe', 345], ['chunfen', 0],
  ['qingming', 15], ['guyu', 30], ['lixia', 45], ['xiaoman', 60], ['mangzhong', 75], ['xiazhi', 90],
  ['xiaoshu', 105], ['dashu', 120], ['liqiu', 135], ['chushu', 150], ['bailu', 165], ['qiufen', 180],
  ['hanlu', 195], ['shuangjiang', 210], ['lidong', 225], ['xiaoxue', 240], ['daxue', 255], ['dongzhi', 270],
]

const rad = Math.PI / 180
const jd = (ms) => ms / 86400000 + 2440587.5
const sunLongitude = (ms) => {
  const n = jd(ms) - 2451545.0
  const L = 280.46 + 0.9856474 * n
  const g = (357.528 + 0.9856003 * n) * rad
  return (((L + 1.915 * Math.sin(g) + 0.02 * Math.sin(2 * g)) % 360) + 360) % 360
}
// The moment (ms) in [lo, hi] when the sun reaches `target` degrees.
const moment = (target, lo, hi) => {
  const diff = (ms) => ((sunLongitude(ms) - target + 540) % 360) - 180
  for (let i = 0; i < 60; i++) {
    const mid = (lo + hi) / 2
    if (diff(mid) < 0) lo = mid
    else hi = mid
  }
  return lo
}
const CST = 8 * 3600_000
const mmdd = (ms) => {
  const d = new Date(ms + CST)
  return String(d.getUTCMonth() + 1).padStart(2, '0') + String(d.getUTCDate()).padStart(2, '0')
}

// New moons (Meeus, "Astronomical Algorithms", ch. 49, the main terms), as the
// China Standard Time day they fall on. The runtime's Chinese calendar names the
// month (and which one is a leap month) from the middle of it, which no edge
// case can move; the day the month starts is taken from the moon, since a new
// moon a few minutes before midnight is where such calendars go wrong.
const DELTA_T = 70 // seconds, TT - UT, for these years
const newMoon = (k) => {
  const T = k / 1236.85
  const E = 1 - 0.002516 * T - 0.0000074 * T * T
  const M = (2.5534 + 29.1053567 * k - 0.0000014 * T * T) * rad
  const Mp = (201.5643 + 385.81693528 * k + 0.0107582 * T * T + 0.00001238 * T ** 3) * rad
  const F = (160.7108 + 390.67050284 * k - 0.0016118 * T * T - 0.00000227 * T ** 3) * rad
  const O = (124.7746 - 1.56375588 * k + 0.0020672 * T * T + 0.00000215 * T ** 3) * rad
  let j = 2451550.09766 + 29.530588861 * k + 0.00015437 * T * T - 0.00000015 * T ** 3
  j += -0.4072 * Math.sin(Mp) + 0.17241 * E * Math.sin(M) + 0.01608 * Math.sin(2 * Mp) + 0.01039 * Math.sin(2 * F) + 0.00739 * E * Math.sin(Mp - M) - 0.00514 * E * Math.sin(Mp + M)
  j += 0.00208 * E * E * Math.sin(2 * M) - 0.00111 * Math.sin(Mp - 2 * F) - 0.00057 * Math.sin(Mp + 2 * F) + 0.00056 * E * Math.sin(2 * Mp + M) - 0.00042 * Math.sin(3 * Mp)
  j += 0.00042 * E * Math.sin(M + 2 * F) + 0.00038 * E * Math.sin(M - 2 * F) - 0.00024 * E * Math.sin(2 * Mp - M) - 0.00017 * Math.sin(O) - 0.00007 * Math.sin(Mp + 2 * M)
  j += 0.00004 * Math.sin(2 * Mp - 2 * F) + 0.00004 * Math.sin(3 * M) + 0.00003 * Math.sin(Mp + M - 2 * F) + 0.00003 * Math.sin(2 * Mp + 2 * F) - 0.00003 * Math.sin(Mp + M + 2 * F) + 0.00003 * Math.sin(Mp - M + 2 * F) - 0.00002 * Math.sin(Mp - M - 2 * F) - 0.00002 * Math.sin(3 * Mp + M) + 0.00002 * Math.sin(4 * Mp)
  const planets = [[299.77 + 0.107408 * k - 0.009173 * T * T, 0.000325], [251.88 + 0.016321 * k, 0.000165], [251.83 + 26.651886 * k, 0.000164], [349.42 + 36.412478 * k, 0.000126], [84.66 + 18.206239 * k, 0.00011], [141.74 + 53.303771 * k, 0.000062], [207.14 + 2.453732 * k, 0.00006], [154.84 + 7.30686 * k, 0.000056], [34.52 + 27.261239 * k, 0.000047], [207.19 + 0.121824 * k, 0.000042], [291.34 + 1.844379 * k, 0.00004], [161.72 + 24.198154 * k, 0.000037], [239.56 + 25.513099 * k, 0.000035], [331.55 + 3.592518 * k, 0.000023]]
  for (const [a, c] of planets) j += c * Math.sin(a * rad)
  return (j - 2440587.5) * 86400_000 - DELTA_T * 1000
}
const lunar = new Intl.DateTimeFormat('en-u-ca-chinese', { month: 'numeric', day: 'numeric', timeZone: 'Asia/Shanghai' })
const monthOf = (ms) => Object.fromEntries(lunar.formatToParts(new Date(ms)).map((x) => [x.type, x.value])).month
const FESTIVALS = [['1', 1, 'springFestival'], ['1', 15, 'lantern'], ['5', 5, 'dragonBoat'], ['7', 7, 'qixi'], ['8', 15, 'midAutumn'], ['9', 9, 'doubleNinth'], ['12', 8, 'laba']]
const dayStart = (ms) => Math.floor((ms + CST) / 86400_000) * 86400_000 - CST // 00:00 CST of that day

// Every festival (and the eve) by its date.
const festivals = []
{
  const k0 = Math.floor(((Date.UTC(from, 0, 1) - Date.UTC(2000, 0, 6)) / 86400_000 / 29.530588861)) - 1
  for (let k = k0; ; k++) {
    const start = dayStart(newMoon(k))
    if (start > Date.UTC(to + 1, 1, 1)) break
    const label = monthOf(start + 14 * 86400_000 + 12 * 3600_000)
    const nextStart = dayStart(newMoon(k + 1))
    for (const [m, d, name] of FESTIVALS) if (label === m) festivals.push([start + (d - 1) * 86400_000, name])
    // The eve is the last day of the 12th month.
    if (label === '12') festivals.push([nextStart - 86400_000, 'newYearsEve'])
  }
}

const out = []
const close = []
for (let y = from; y <= to; y++) {
  const days = new Map()
  const add = (key, name) => days.set(key, [...(days.get(key) ?? []), name])
  for (const [name, deg] of TERMS) {
    const month = Math.floor(((deg + 75) % 360) / 30) + 1
    const guess = Date.UTC(y, ((month - 1) % 12), 5) - CST
    const ms = moment(deg, guess - 12 * 86400_000, guess + 22 * 86400_000)
    const minutes = Math.floor(((ms + CST) % 86400_000) / 60_000)
    if (minutes < 45 || minutes > 1440 - 45) close.push(`${y} ${name} ${mmdd(ms)} ${String(Math.floor(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`)
    add(mmdd(ms), name)
  }
  for (const [ms, name] of festivals) if (new Date(ms + CST).getUTCFullYear() === y) add(mmdd(ms), name)
  const items = [...days].sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => `${k}=${v.join('+')}`)
  out.push(`  ${y}: '${items.join(' ')}',`)
}
if (close.length) console.error('Within 45 minutes of midnight (check these by hand):\n' + close.join('\n'))

console.log(`// The days of the Chinese festivals and the 24 solar terms, for ${from} to ${to}.
// Written by scripts/calendar-table.mjs (the festivals from the Chinese calendar,
// the terms from the sun's longitude, in China Standard Time); the app looks a
// day up here, and a year outside the table simply has none of them.
// Each year is "MMDD=name" entries; a day with two names joins them with "+".

export const CALENDAR_YEARS: Readonly<Record<number, string>> = {
${out.join('\n')}
}
`)
