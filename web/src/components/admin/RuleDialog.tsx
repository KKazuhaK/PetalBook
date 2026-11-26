import { useState, type FormEvent, type ReactNode } from 'react'
import { Trash2 } from 'lucide-react'
import type { RuleWrite, SettingKind, SettingLower } from '@/api/types'
import { FormMessage } from '@/components/auth/Field'
import { SettingsRow, SettingsRows } from '@/components/settings/SettingsParts'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { tt } from '@/i18n/tenants'
import { RuleEditor } from './SettingsPanel'

const tx = tt.rules
const GB = 1024 ** 3

// A key counted in bytes is typed in GB; the API counts bytes.
export const inBytes = (key: string) => /bytes$/i.test(key)

export const nameOf = (key: string) => tt.settings.names[key] ?? key

// What the editor needs to know of a setting.
export interface RuleSetting {
  key: string
  kind: SettingKind
  choices?: string[]
  // A limit the tenant also sets: a plan's or billing's value for it must cap (a maximum or a lock), because the
  // tenant's own value would rank above a bare value.
  tenantSets?: boolean
}
// What a rule holds: both parts optional, the dates only where the rule can have a window.
export interface RuleDraft {
  value?: unknown
  lower?: SettingLower | null
  from?: string | null
  until?: string | null
}

const toLocal = (iso?: string | null) => {
  if (!iso) return ''
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
const fromLocal = (v: string) => (v ? new Date(v).toISOString().replace('.000Z', 'Z') : undefined)

// What the form of a value says: undefined when it is not a value of the kind.
export function readValue(s: RuleSetting, flag: boolean, choice: string, text: string): unknown {
  if (s.kind === 'boolean') return flag
  if (s.kind === 'choice') return choice
  if (s.kind === 'text') return text
  const n = Number(text.trim())
  const ok = /^\d+(\.\d{1,2})?$/.test(text.trim()) && Number.isFinite(n)
  if (!ok || (!inBytes(s.key) && !Number.isInteger(n))) return undefined
  return inBytes(s.key) ? Math.round(n * GB) : n
}

// Whether a rule is accepted without asking the server: the message that says why not, or ''. With `capRequired` (a
// plan's rule) a limit the tenant also sets that has a value must also have a maximum or a lock.
export function ruleProblem(
  s: RuleSetting,
  rule: { value?: unknown; lower?: SettingLower | null },
  capRequired: boolean,
): string {
  if (rule.value === undefined && !rule.lower) return tx.needsOne
  if (!capRequired || s.kind !== 'limit' || !s.tenantSets || rule.value === undefined || rule.value === null) return ''
  const l = rule.lower
  if (!l || l.mode === 'allow') return tx.needsCap
  if (l.mode === 'restrict' && typeof rule.value === 'number' && l.max !== undefined && rule.value > l.max)
    return tx.aboveMax
  return ''
}

// The rule of one setting, as one form: a value, what it lets the tenant do (a lock or a restriction), and the window
// it counts in. A bottom sheet on a phone. Used for a tenant's manual rule (with a window) and for a plan's rule
// (no window; a limit the tenant also sets must cap). The caller sends it and says what went wrong in `error`.
export function RuleDialog({
  setting,
  title,
  rule,
  start,
  windows,
  capRequired = false,
  header,
  pending,
  error,
  removeLabel,
  onRemove,
  onSubmit,
  onClose,
}: {
  setting: RuleSetting
  title?: string
  // The rule as it is now (null: there is none yet).
  rule: RuleDraft | null
  // What the value starts as when the rule gives none: the value in effect.
  start: unknown
  windows: boolean
  capRequired?: boolean
  // Above the form's fields (the plan's picker of the setting).
  header?: ReactNode
  pending: boolean
  error: string
  removeLabel?: string
  onRemove?: () => void
  onSubmit: (rule: RuleWrite) => void
  onClose: () => void
}) {
  const s = setting
  const first = rule && rule.value != null ? rule.value : start
  const capped = capRequired && s.kind === 'limit' && !!s.tenantSets
  const [giveValue, setGiveValue] = useState(rule ? rule.value != null : true)
  const [flag, setFlag] = useState(first === true)
  const [choice, setChoice] = useState(typeof first === 'string' ? first : (s.choices?.[0] ?? ''))
  const [text, setText] = useState(
    typeof first === 'number'
      ? inBytes(s.key)
        ? String(Math.round((first / GB) * 100) / 100)
        : String(first)
      : typeof first === 'string' && s.kind === 'text'
        ? first
        : '',
  )
  const [lower, setLower] = useState<SettingLower>(
    rule?.lower ?? (capped ? { mode: 'restrict', max: typeof first === 'number' ? first : 1 } : { mode: 'allow' }),
  )
  const [from, setFrom] = useState(toLocal(rule?.from))
  const [until, setUntil] = useState(toLocal(rule?.until))
  const [problem, setProblem] = useState('')

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setProblem('')
    const out: RuleWrite = {}
    if (giveValue) {
      const v = readValue(s, flag, choice, text)
      if (v === undefined) return setProblem(tx.invalidValue)
      out.value = v
    }
    if (lower.mode !== 'allow') out.lower = lower
    const why = ruleProblem(s, out, capRequired)
    if (why) return setProblem(why)
    if (windows) {
      if (from && until && new Date(until) <= new Date(from)) return setProblem(tx.untilBeforeFrom)
      if (from) out.from = fromLocal(from)
      if (until) out.until = fromLocal(until)
    }
    onSubmit(out)
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent sheet className="sm:max-w-lg">
        <form
          onSubmit={submit}
          className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-4"
          noValidate
          data-slot="rule-form"
        >
          <DialogHeader>
            <DialogTitle className="pr-8 text-base">{title ?? nameOf(s.key)}</DialogTitle>
            <DialogDescription className="font-mono [overflow-wrap:anywhere]">{s.key}</DialogDescription>
          </DialogHeader>
          {header}
          <FormMessage>{problem || error}</FormMessage>
          <SettingsRows>
            <SettingsRow title={tx.valueLabel} controlId="rule-value">
              <div className="grid gap-2">
                <label className="flex min-h-10 items-center gap-2 text-sm lg:min-h-0">
                  <input
                    type="checkbox"
                    className="size-4"
                    checked={giveValue}
                    onChange={(e) => setGiveValue(e.target.checked)}
                  />
                  {tx.giveValue}
                </label>
                {giveValue && s.kind === 'boolean' && (
                  <Switch id="rule-value" checked={flag} onCheckedChange={setFlag} aria-label={tx.valueLabel} />
                )}
                {giveValue && s.kind === 'limit' && (
                  <Input
                    id="rule-value"
                    value={text}
                    inputMode="decimal"
                    className="h-10 lg:h-8"
                    placeholder={inBytes(s.key) ? tx.gb : ''}
                    aria-label={tx.valueLabel}
                    onChange={(e) => setText(e.target.value)}
                  />
                )}
                {giveValue && s.kind === 'text' && (
                  <Input
                    id="rule-value"
                    value={text}
                    className="h-10 lg:h-8"
                    aria-label={tx.valueLabel}
                    onChange={(e) => setText(e.target.value)}
                  />
                )}
                {giveValue && s.kind === 'choice' && (
                  <NativeSelect
                    id="rule-value"
                    className="h-10 w-full lg:h-8"
                    aria-label={tx.valueLabel}
                    value={choice}
                    onChange={(e) => setChoice(e.target.value)}
                  >
                    {(s.choices ?? []).map((c) => (
                      <option key={c} value={c}>
                        {tt.settings.choices[s.key]?.[c] ?? c}
                      </option>
                    ))}
                  </NativeSelect>
                )}
              </div>
            </SettingsRow>
            <SettingsRow title={tx.lowerLabel} help={capped ? tx.capHelp : tx.lowerHelp} controlId="rule-lower">
              <RuleEditor
                kind={s.kind}
                lower={lower}
                disabled={false}
                label={tx.lowerLabel}
                choices={s.choices}
                words={tt.settings.choices[s.key]}
                onChange={setLower}
              />
            </SettingsRow>
            {windows && (
              <>
                <SettingsRow title={tx.fromLabel} help={tx.windowHelp} controlId="rule-from">
                  <Input
                    id="rule-from"
                    type="datetime-local"
                    className="h-10 w-full min-w-0 lg:h-8"
                    value={from}
                    onChange={(e) => setFrom(e.target.value)}
                  />
                </SettingsRow>
                <SettingsRow title={tx.untilLabel} controlId="rule-until">
                  <Input
                    id="rule-until"
                    type="datetime-local"
                    className="h-10 w-full min-w-0 lg:h-8"
                    value={until}
                    onChange={(e) => setUntil(e.target.value)}
                  />
                </SettingsRow>
              </>
            )}
          </SettingsRows>
          {windows && (from || until) && <p className="text-xs text-muted-foreground">{tx.replaceNote}</p>}
          <DialogFooter className="max-sm:flex-col-reverse">
            {onRemove && (
              <Button type="button" variant="outline" className="sm:mr-auto" disabled={pending} onClick={onRemove}>
                <Trash2 aria-hidden="true" />
                {removeLabel ?? tx.remove}
              </Button>
            )}
            <Button type="button" variant="outline" onClick={onClose}>
              {tt.list.cancel}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? tx.saving : tx.save}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
