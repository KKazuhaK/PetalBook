import { useState } from 'react'
import { Download } from 'lucide-react'
import { adminApi } from '@/api/admin'
import { buttonVariants } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { tu } from '@/i18n/usage'

const te = tu.export
const DAY = 24 * 60 * 60 * 1000
const MAX_DAYS = 366

// A day as the server writes it: the UTC date.
const dayText = (t: number) => new Date(t).toISOString().slice(0, 10)

// The days in a range, both ends counted; NaN when either is not a day.
export function daysIn(from: string, to: string): number {
  return Math.round((Date.parse(to) - Date.parse(from)) / DAY) + 1
}

// The usage export of a tenant (docs/api.md, "Usage export"): a range of days and a format, then a link the browser
// downloads from (the server streams the rows and writes the export to the audit log).
export function UsageExport({ tenant }: { tenant: string }) {
  const [to, setTo] = useState(() => dayText(Date.now()))
  const [from, setFrom] = useState(() => dayText(Date.now() - 29 * DAY))
  const [format, setFormat] = useState<'csv' | 'jsonl'>('csv')
  const days = daysIn(from, to)
  const valid = days >= 1 && days <= MAX_DAYS
  return (
    <section className="grid min-w-0 gap-3 border-t pt-3" data-slot="usage-export">
      <div className="grid gap-0.5">
        <h4 className="text-sm font-medium">{te.title}</h4>
        <p className="text-xs text-muted-foreground">{te.help}</p>
      </div>
      <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-[repeat(3,minmax(0,12rem))_auto] sm:items-end">
        <div className="grid min-w-0 gap-1">
          <Label htmlFor={`usage-from-${tenant}`}>{te.from}</Label>
          <Input
            id={`usage-from-${tenant}`}
            type="date"
            value={from}
            max={to}
            onChange={(e) => setFrom(e.target.value)}
          />
        </div>
        <div className="grid min-w-0 gap-1">
          <Label htmlFor={`usage-to-${tenant}`}>{te.to}</Label>
          <Input id={`usage-to-${tenant}`} type="date" value={to} min={from} onChange={(e) => setTo(e.target.value)} />
        </div>
        <div className="grid min-w-0 gap-1">
          <Label htmlFor={`usage-format-${tenant}`}>{te.format}</Label>
          <NativeSelect
            id={`usage-format-${tenant}`}
            value={format}
            onChange={(e) => setFormat(e.target.value === 'jsonl' ? 'jsonl' : 'csv')}
          >
            <option value="csv">{te.csv}</option>
            <option value="jsonl">{te.jsonl}</option>
          </NativeSelect>
        </div>
        {valid ? (
          <a
            href={adminApi.usageExportUrl(tenant, { from, to, format })}
            download
            className={buttonVariants({ variant: 'outline', className: 'h-10 w-full sm:h-8 sm:w-fit' })}
            data-slot="usage-export-download"
          >
            <Download aria-hidden="true" />
            {te.download}
          </a>
        ) : (
          <span className="text-xs text-destructive" role="alert" data-slot="usage-export-range">
            {te.badRange}
          </span>
        )}
      </div>
    </section>
  )
}
