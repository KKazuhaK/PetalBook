import { useId, type ComponentProps, type Ref } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

// A labelled input with its help and error text tied to it, so a screen reader
// reads them with the field and an invalid field says so.
export function Field({
  label,
  help,
  error,
  inputRef,
  ...input
}: { label: string; help?: string; error?: string; inputRef?: Ref<HTMLInputElement> } & Omit<ComponentProps<'input'>, 'ref'>) {
  const id = useId()
  const helpId = `${id}-help`
  const errorId = `${id}-error`
  const described = [help ? helpId : '', error ? errorId : ''].filter(Boolean).join(' ') || undefined
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} ref={inputRef} aria-describedby={described} aria-invalid={error ? true : undefined} {...input} />
      {help && (
        <p id={helpId} className="text-xs text-muted-foreground">
          {help}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}

// A message about the whole form. `role="alert"` makes it announced when it
// appears; it can also take focus (see `focusRef`) so keyboard users land on it.
export function FormMessage({ children, tone = 'error', focusRef }: { children: React.ReactNode; tone?: 'error' | 'info'; focusRef?: Ref<HTMLParagraphElement> }) {
  if (!children) return null
  return (
    <p
      ref={focusRef}
      tabIndex={-1}
      role={tone === 'error' ? 'alert' : 'status'}
      className={`rounded-md border px-3 py-2 text-sm outline-none ${tone === 'error' ? 'border-destructive/30 bg-destructive/10 text-destructive' : 'border-border bg-muted text-foreground'}`}
    >
      {children}
    </p>
  )
}
