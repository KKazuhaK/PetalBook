import { t } from '@/i18n'

// The marks of a form's labels: a required input has a star (the input itself says aria-required), an optional one
// says so in words, where the form has both kinds.
export function RequiredMark() {
  return (
    <span aria-hidden="true" data-slot="required-mark" title={t.common.required} className="text-destructive">
      *
    </span>
  )
}

export function OptionalMark() {
  return (
    <span data-slot="optional-mark" className="font-normal text-muted-foreground">
      {t.common.optionalMark}
    </span>
  )
}
