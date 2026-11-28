import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { adminApi } from '@/api/admin'
import { daysIn, UsageExport } from './UsageExport'

// The usage export: the address names the tenant, the days and the format; a range of more than 366 days is not offered.

describe('the address of the usage export', () => {
  it('names the tenant, both days and the format', () => {
    expect(adminApi.usageExportUrl('T1', { from: '2026-05-01', to: '2026-05-31', format: 'jsonl' })).toBe(
      '/api/admin/tenants/T1/usage/export?from=2026-05-01&to=2026-05-31&format=jsonl',
    )
  })
})

describe('the days of a range', () => {
  it('counts both ends', () => {
    expect(daysIn('2026-05-01', '2026-05-01')).toBe(1)
    expect(daysIn('2025-05-01', '2026-05-01')).toBe(366)
    expect(daysIn('2025-04-30', '2026-05-01')).toBe(367)
    expect(daysIn('2026-05-02', '2026-05-01')).toBe(0)
    expect(daysIn('', '2026-05-01')).toBeNaN()
  })
})

describe('the usage export block', () => {
  it('offers the download for the last 30 days as CSV', () => {
    const html = renderToStaticMarkup(<UsageExport tenant="T1" />)
    expect(html).toContain('data-slot="usage-export-download"')
    expect(html).toContain('/api/admin/tenants/T1/usage/export?from=')
    expect(html).toContain('format=csv')
    expect(html).not.toContain('usage-export-range')
  })
})
