// How quotas and usage read on a page: numbers for people, and the limits that are none.

import { formatBytes } from './bytes'
import { tu } from '@/i18n/usage'
import type { TenantQuotaKey } from '@/api/types'

const GB = 1024 ** 3

// 10485760 -> "10 MB"; a count with its thousands separated.
export function formatUsed(kind: TenantQuotaKey, n: number): string {
  return kind === 'bytes' ? formatBytes(n) : n.toLocaleString()
}

// A limit that is null is none.
export function formatLimit(kind: TenantQuotaKey, limit: number | null): string {
  return limit === null ? tu.noLimit : formatUsed(kind, limit)
}

// The share of a limit that is used, 0 to 1 (more than 1 is over); null when there is no limit.
export function share(used: number, limit: number | null): number | null {
  if (limit === null) return null
  if (limit <= 0) return used > 0 ? 2 : 0
  return used / limit
}

// A disk limit is typed in GB (the API counts bytes).
export const bytesToGb = (n: number | null): string => (n === null ? '' : String(Math.round((n / GB) * 100) / 100))
export const gbToBytes = (text: string): number | null | undefined => {
  const v = text.trim()
  if (v === '') return null
  if (!/^\d+(\.\d{1,2})?$/.test(v)) return undefined
  return Math.max(1, Math.round(Number(v) * GB))
}
// A count typed in a field: empty is null (follow / none), a whole number otherwise, undefined for anything else.
export const parseCount = (text: string): number | null | undefined => {
  const v = text.trim()
  if (v === '') return null
  return /^\d{1,12}$/.test(v) ? Number(v) : undefined
}
export const countToText = (n: number | null): string => (n === null ? '' : String(n))
