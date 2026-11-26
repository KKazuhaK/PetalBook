import { lang, t } from '@/i18n'

const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' })

// "just now", "5 minutes ago", "yesterday", and a plain date once it is more
// than a month old; the wording and the date's shape follow the language.
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''
  const sec = Math.round((then - now) / 1000)
  const abs = Math.abs(sec)
  if (abs < 45) return t.time.justNow
  if (abs < 3600) return rtf.format(Math.round(sec / 60), 'minute')
  if (abs < 86_400) return rtf.format(Math.round(sec / 3600), 'hour')
  if (abs < 86_400 * 30) return rtf.format(Math.round(sec / 86_400), 'day')
  return shortDate(new Date(then), new Date(now))
}

// "11月21日" / "November 21"; the year only when it is not the current one.
export const shortDate = (d: Date, now: Date = new Date()) =>
  d.toLocaleDateString(
    lang,
    d.getFullYear() === now.getFullYear()
      ? { month: 'long', day: 'numeric' }
      : { year: 'numeric', month: 'long', day: 'numeric' },
  )

// 24-hour clock in both languages ("h23": plain `hour12: false` shows 24:05 in some English locales).
export const clockTime = (d: Date) =>
  d.toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })

export const absoluteTime = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(lang, { hourCycle: 'h23' })
}

// A moment in the viewer's own time with the name of the zone, as "2026年11月25日 14:48 (America/Los_Angeles)".
export const zonedTime = (iso: string) => {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const when = d.toLocaleString(lang, { dateStyle: 'long', timeStyle: 'short', hourCycle: 'h23' })
  return `${when} (${Intl.DateTimeFormat().resolvedOptions().timeZone})`
}
