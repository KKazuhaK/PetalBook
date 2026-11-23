import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field } from '@/components/auth/Field'
import { ApiError } from '@/api/client'
import { t } from '@/i18n'
import { tu } from '@/i18n/usage'

// Asks for one name (a space's). Mounted only while open, so the field starts
// from `initial` each time.
export function NameDialog({
  open,
  onOpenChange,
  title,
  initial,
  submitLabel,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  initial: string
  submitLabel: string
  onSubmit: (name: string) => Promise<void>
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <NameForm title={title} initial={initial} submitLabel={submitLabel} onSubmit={onSubmit} onClose={() => onOpenChange(false)} />}
    </Dialog>
  )
}

function NameForm({ title, initial, submitLabel, onSubmit, onClose }: { title: string; initial: string; submitLabel: string; onSubmit: (name: string) => Promise<void>; onClose: () => void }) {
  const [value, setValue] = useState(initial)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    input.current?.select()
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const name = value.trim()
    if (name === '' || Array.from(name).length > 80) return setError(t.spaces.nameInvalid)
    setBusy(true)
    setError('')
    try {
      await onSubmit(name)
      onClose()
    } catch (err) {
      setBusy(false)
      setError(err instanceof ApiError && err.code === 'quota_exceeded' ? tu.account.createRefused(Number((err.body as { limit?: number } | undefined)?.limit ?? 0)) : err instanceof ApiError ? err.message : t.spaces.failed)
    }
  }

  return (
    <DialogContent>
      <form className="grid gap-4" onSubmit={(e) => void submit(e)} noValidate>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{t.spaces.nameHelp}</DialogDescription>
        </DialogHeader>
        <Field label={t.spaces.nameLabel} inputRef={input} value={value} onChange={(e) => setValue(e.target.value)} error={error} autoFocus autoComplete="off" />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t.spaces.cancel}
          </Button>
          <Button type="submit" disabled={busy}>
            {submitLabel}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  )
}
