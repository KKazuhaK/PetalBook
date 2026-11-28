import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { tt } from '@/i18n/tenants'

const ts = tt.settings

// The captcha forms an organisation may ask a captcha of (the site keeps the invitation and the first-run setup).
export const CAPTCHA_TENANT_FORMS = ['login', 'forgot', 'register'] as const
type Form = (typeof CAPTCHA_TENANT_FORMS)[number]
interface FormValue {
  on: boolean
  when: 'always' | 'after_failures'
  failures: number
}
type Doc = Partial<Record<Form, Partial<FormValue>>>

const DEFAULT: FormValue = { on: false, when: 'after_failures', failures: 3 }

const read = (doc: unknown, form: Form): FormValue => ({
  ...DEFAULT,
  ...((doc as Doc | null | undefined)?.[form] ?? {}),
})

// The value of captcha.contexts of an organisation, form by form: on or off, when it asks, and after how many
// failures. `looser` says whether a whole document would be looser than the site's minimum, so that what the server
// would refuse is greyed out.
export function CaptchaContextsEditor({
  value,
  disabled,
  label,
  looser,
  onChange,
}: {
  value: unknown
  disabled: boolean
  label: string
  looser?: (v: unknown) => boolean
  onChange: (v: unknown) => void
}) {
  // Only the forms of an organisation are written: the others are the site's.
  const doc = (): Doc => Object.fromEntries(CAPTCHA_TENANT_FORMS.map((f) => [f, read(value, f)])) as Doc
  const set = (form: Form, patch: Partial<FormValue>) =>
    onChange({ ...doc(), [form]: { ...read(value, form), ...patch } })
  const blocked = (form: Form, patch: Partial<FormValue>) =>
    looser?.({ ...doc(), [form]: { ...read(value, form), ...patch } }) === true
  return (
    <ul className="grid gap-3" data-slot="captcha-forms" aria-label={label}>
      {CAPTCHA_TENANT_FORMS.map((form) => {
        const v = read(value, form)
        const name = ts.captchaForms[form]
        return (
          <li key={form} className="grid gap-1.5" data-form={form}>
            <label className="flex min-h-10 items-center gap-2 text-sm lg:min-h-0">
              <Switch
                checked={v.on}
                disabled={disabled || blocked(form, { on: !v.on })}
                aria-label={`${label}: ${name}`}
                onCheckedChange={(on) => set(form, { on })}
              />
              {name}
            </label>
            {v.on && (
              <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,6rem)] gap-2">
                <NativeSelect
                  className="h-10 w-full lg:h-8"
                  aria-label={`${label}: ${name}: ${ts.captchaWhenLabel}`}
                  disabled={disabled}
                  value={v.when}
                  onChange={(e) => set(form, { when: e.target.value as FormValue['when'] })}
                >
                  {(['always', 'after_failures'] as const).map((w) => (
                    <option
                      key={w}
                      value={w}
                      disabled={blocked(form, { when: w })}
                      title={blocked(form, { when: w }) ? ts.looserBlocked : undefined}
                    >
                      {ts.captchaWhen[w]}
                    </option>
                  ))}
                </NativeSelect>
                {v.when === 'after_failures' && (
                  <Input
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={100}
                    className="h-10 w-full lg:h-8"
                    aria-label={`${label}: ${name}: ${ts.captchaFailuresLabel}`}
                    disabled={disabled}
                    aria-invalid={blocked(form, { failures: v.failures }) ? true : undefined}
                    value={String(v.failures)}
                    onChange={(e) => set(form, { failures: Number(e.target.value) || 1 })}
                  />
                )}
              </div>
            )}
          </li>
        )
      })}
      <li className="text-xs text-muted-foreground">{ts.captchaSiteForms}</li>
    </ul>
  )
}
