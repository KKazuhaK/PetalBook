const rtf = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' })

// "刚刚", "5 分钟前", "昨天", and a plain date once it is more than a month old.
export function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''
  const sec = Math.round((then - now) / 1000)
  const abs = Math.abs(sec)
  if (abs < 45) return '刚刚'
  if (abs < 3600) return rtf.format(Math.round(sec / 60), 'minute')
  if (abs < 86_400) return rtf.format(Math.round(sec / 3600), 'hour')
  if (abs < 86_400 * 30) return rtf.format(Math.round(sec / 86_400), 'day')
  const d = new Date(then)
  const sameYear = d.getFullYear() === new Date(now).getFullYear()
  return sameYear ? `${d.getMonth() + 1}月${d.getDate()}日` : `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日`
}

export const absoluteTime = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString('zh-CN', { hour12: false })
}
