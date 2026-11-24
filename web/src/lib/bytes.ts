// A size for people: 10485760 -> "10 MB". Binary units, the way upload limits are set.
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  const text = v >= 100 || Number.isInteger(v) ? String(Math.round(v)) : v.toFixed(1)
  return `${text} ${units[i]}`
}
