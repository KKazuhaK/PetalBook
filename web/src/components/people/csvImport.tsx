import { useRef, useState } from 'react'
import { Copy, FileUp } from 'lucide-react'
import { ApiError } from '@/api/client'
import { MAX_BATCH_ROWS, type BatchAnswer, type BatchRowIn, type BatchRowOut, type BatchRunner } from '@/api/people'
import { FormMessage } from '@/components/auth/Field'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { readPeopleCsv } from '@/lib/csv'
import { tp } from '@/i18n/people'

export interface RoleOption {
  value: string
  label: string
}

// The statuses of rows that a request would act on.
const ACTS = new Set(['ok', 'added', 'invited'])

export interface CsvImport {
  text: string
  setText: (v: string) => void
  role: string
  setRole: (v: string) => void
  answer: BatchAnswer | null
  error: string
  busy: boolean
  fileName: string
  loadFile: (f: File) => Promise<void>
  check: () => Promise<void>
  apply: () => Promise<BatchAnswer | null>
  reset: () => void
  // How many rows the request would act on.
  actionable: number
}

// The state of one CSV import: the text, its rows checked by the server without writing (a dry run), then the
// same rows applied. The roles a file may name are those of the dialog (by key or by label).
export function useCsvImport(run: BatchRunner, roles: RoleOption[] | undefined, defaultRole: string): CsvImport {
  const [text, setText] = useState('')
  const [fileName, setFileName] = useState('')
  const [role, setRole] = useState(defaultRole)
  const [rows, setRows] = useState<BatchRowIn[]>([])
  const [answer, setAnswer] = useState<BatchAnswer | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const normalize = (r: BatchRowIn): BatchRowIn => {
    if (!roles) {
      const { role: _drop, ...rest } = r
      void _drop
      return rest
    }
    const given = (r.role ?? '').trim().toLowerCase()
    if (given === '') return { ...r, role }
    const hit = roles.find((o) => o.value.toLowerCase() === given || o.label.toLowerCase() === given)
    return { ...r, role: hit ? hit.value : (r.role ?? '') }
  }
  const fail = (e: unknown) => setError(e instanceof ApiError ? e.message : tp.failed)

  const check = async () => {
    setError('')
    const read = readPeopleCsv(text).rows
    if (read.length === 0) return setError(tp.csv.empty)
    if (read.length > MAX_BATCH_ROWS) return setError(tp.csv.tooMany(MAX_BATCH_ROWS))
    const next = read.map(normalize)
    setBusy(true)
    try {
      setAnswer(await run(next, true))
      setRows(next)
    } catch (e) {
      fail(e)
    }
    setBusy(false)
  }
  const apply = async () => {
    setError('')
    setBusy(true)
    try {
      const a = await run(rows, false)
      setAnswer(a)
      setBusy(false)
      return a
    } catch (e) {
      fail(e)
    }
    setBusy(false)
    return null
  }
  return {
    text,
    setText: (v) => {
      setText(v)
      setAnswer(null)
    },
    role,
    setRole,
    answer,
    error,
    busy,
    fileName,
    loadFile: async (f) => {
      setFileName(f.name)
      setText(await f.text())
      setAnswer(null)
    },
    check,
    apply,
    reset: () => {
      setAnswer(null)
      setError('')
    },
    actionable: answer ? answer.results.filter((r) => ACTS.has(r.status)).length : 0,
  }
}

const TONE: Record<string, 'secondary' | 'destructive' | 'outline' | 'default'> = {
  ok: 'default',
  added: 'default',
  invited: 'default',
  already_member: 'secondary',
}

// The label of a row's result in words; `done` once the batch has been applied.
function RowStatus({ r, done }: { r: BatchRowOut; done: boolean }) {
  const label = (done ? tp.statusDone : tp.status)[r.status] ?? r.status
  return (
    <Badge variant={TONE[r.status] ?? 'destructive'} title={r.detail}>
      {label}
    </Badge>
  )
}

function InviteLink({ token }: { token: string }) {
  const link = `${window.location.origin}/invite#${token}`
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  return (
    <span className="inline-flex items-center gap-2">
      <Button
        type="button"
        variant="outline"
        size="xs"
        onClick={() =>
          void navigator.clipboard.writeText(link).then(
            () => setState('copied'),
            () => setState('failed'),
          )
        }
      >
        <Copy aria-hidden="true" />
        {state === 'copied' ? tp.csv.copied : tp.csv.copyLink}
      </Button>
      {state === 'failed' && <span className="text-xs text-destructive">{tp.csv.copyFailed}</span>}
    </span>
  )
}

function MailNote({ r }: { r: BatchRowOut }) {
  if (!r.mail) return null
  return (
    <span className="text-xs text-muted-foreground">
      {r.mail === 'sent' ? tp.csv.mailSent : r.mail === 'limited' ? tp.csv.mailLimited : tp.csv.mailNone}
    </span>
  )
}

// The body of the import tab: the text to read (a file or pasted), then the rows as the server checked them.
export function CsvBody({
  csv,
  roles,
  help,
  mailPage,
}: {
  csv: CsvImport
  roles?: RoleOption[]
  help: string
  // Set for a batch that invites: where to set up mail when none is configured.
  mailPage?: string
}) {
  const file = useRef<HTMLInputElement>(null)
  const done = csv.answer !== null && !csv.answer.dryRun
  return (
    <div className="grid gap-3">
      {csv.answer === null ? (
        <>
          <p className="text-sm text-muted-foreground">{help}</p>
          <div className="flex flex-wrap items-center gap-2">
            <input
              ref={file}
              type="file"
              accept=".csv,.txt,text/csv,text/plain"
              className="sr-only"
              tabIndex={-1}
              aria-hidden="true"
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) void csv.loadFile(f)
                e.target.value = ''
              }}
            />
            <Button type="button" variant="outline" onClick={() => file.current?.click()}>
              <FileUp aria-hidden="true" />
              {tp.csv.chooseFile}
            </Button>
            <span className="min-w-0 truncate text-xs text-muted-foreground">{csv.fileName || tp.csv.paste}</span>
          </div>
          <Textarea
            aria-label={tp.csv.pasteLabel}
            className="min-h-28 font-mono text-xs"
            spellCheck={false}
            placeholder={tp.csv.pastePlaceholder}
            value={csv.text}
            onChange={(e) => csv.setText(e.target.value)}
          />
          {roles && (
            <div className="grid gap-1.5 sm:max-w-56">
              <Label htmlFor="csv-default-role">{tp.csv.defaultRole}</Label>
              <NativeSelect id="csv-default-role" value={csv.role} onChange={(e) => csv.setRole(e.target.value)}>
                {roles.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </NativeSelect>
            </div>
          )}
        </>
      ) : (
        <>
          {mailPage && csv.answer.mail && !csv.answer.mail.configured && (
            <div role="note" className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
              <div className="font-medium">{tp.csv.noMailTitle}</div>
              <p className="mt-1 text-muted-foreground">
                {tp.csv.noMailHelp}{' '}
                <a className="underline underline-offset-3" href={mailPage}>
                  {tp.csv.noMailLink}
                </a>
              </p>
            </div>
          )}
          {mailPage && csv.answer.mail?.configured && !done && (
            <p className="text-xs text-muted-foreground">{tp.csv.mailSite}</p>
          )}
          <p className="text-sm font-medium" aria-live="polite">
            {done ? tp.csv.applied(csv.actionable) : tp.csv.rows(csv.answer.results.length)}
            <span className="font-normal text-muted-foreground">
              {' · '}
              {Object.entries(csv.answer.summary)
                .map(([k, n]) => `${(done ? tp.statusDone : tp.status)[k as keyof typeof tp.status] ?? k} ${n}`)
                .join(' · ')}
            </span>
          </p>
          <ul className="divide-y rounded-lg border text-sm">
            {csv.answer.results.map((r) => (
              <li key={r.row} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                <span className="w-7 shrink-0 text-xs text-muted-foreground tabular-nums">{r.row}</span>
                <span className="min-w-0 flex-1 basis-40 [overflow-wrap:anywhere]">
                  {r.name || r.user}
                  {r.name && <span className="block text-xs text-muted-foreground">{r.username || r.user}</span>}
                  {r.detail && <span className="block text-xs text-muted-foreground">{r.detail}</span>}
                </span>
                {r.role && roles && (
                  <span className="text-xs text-muted-foreground">
                    {roles.find((o) => o.value === r.role)?.label ?? r.role}
                  </span>
                )}
                <RowStatus r={r} done={done} />
                {done && <MailNote r={r} />}
                {done && r.token && <InviteLink token={r.token} />}
              </li>
            ))}
          </ul>
          {done && csv.answer.results.some((r) => r.token) && (
            <p className="text-xs text-muted-foreground">{tp.csv.linkOnce}</p>
          )}
        </>
      )}
      <FormMessage>{csv.error}</FormMessage>
    </div>
  )
}
