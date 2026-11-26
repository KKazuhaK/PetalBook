import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { User } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ResponsiveDialog } from '@/components/people/ResponsiveDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { tp } from '@/i18n/people'

// Whether the organization's sign-in supplies this person's name and address: they have a connected sign-in. What it
// supplies may be overwritten the next time they sign in through it, so an edit made here may not last.
export function isManaged(u: Pick<User, 'sso'>): boolean {
  return (u.sso ?? 0) > 0
}

function Managed() {
  return (
    <div className="grid gap-1">
      <Badge variant="secondary" className="w-fit">
        {tp.admin.managed}
      </Badge>
      <p className="text-xs text-muted-foreground">{tp.admin.managedWarn}</p>
    </div>
  )
}

function useAfterEdit() {
  const qc = useQueryClient()
  return () => qc.invalidateQueries({ queryKey: keys.users })
}

function errorOf(e: unknown): string {
  return e instanceof ApiError ? e.message : tp.failed
}

interface DialogProps {
  u: User
  open: boolean
  onOpenChange: (open: boolean) => void
}

// An administrator edits a person's name and username. The person is told, and what was there is in the audit log.
export function EditPersonDialog(props: DialogProps) {
  return props.open ? <EditPersonInner {...props} /> : null
}

function EditPersonInner({ u, open, onOpenChange }: DialogProps) {
  const after = useAfterEdit()
  const [name, setName] = useState(u.name)
  const [username, setUsername] = useState(u.username)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const managed = isManaged(u)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    const body: { name?: string; username?: string } = {}
    if (name.trim() !== u.name) body.name = name.trim()
    if (username.trim() !== u.username) body.username = username.trim()
    if (Object.keys(body).length === 0) return onOpenChange(false)
    setBusy(true)
    setError('')
    try {
      await adminApi.updateUser(u.id, body)
      await after()
      toast.success(tp.admin.edited)
      onOpenChange(false)
    } catch (err) {
      setError(errorOf(err))
    }
    setBusy(false)
  }

  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={onOpenChange}
      title={tp.profile.editProfile}
      footer={
        <>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {tp.cancel}
          </Button>
          <Button type="submit" form="edit-person-form" disabled={busy || name.trim() === ''}>
            {busy ? tp.profile.saving : tp.profile.save}
          </Button>
        </>
      }
    >
      <form id="edit-person-form" className="grid gap-4 py-1" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tp.profile.name}
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          required
          autoComplete="off"
        />
        <Field
          label={tp.profile.username}
          help={tp.profile.usernameHelp}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
        />
        {managed && <Managed />}
        <FormMessage>{error}</FormMessage>
      </form>
    </ResponsiveDialog>
  )
}

// Changing a person's address: verify it (send the link, the default) or mark it verified. The old address is told.
export function ChangePersonEmailDialog(props: DialogProps) {
  return props.open ? <ChangePersonEmailInner {...props} /> : null
}

function ChangePersonEmailInner({ u, open, onOpenChange }: DialogProps) {
  const after = useAfterEdit()
  const [email, setEmail] = useState('')
  const [mode, setMode] = useState<'verify' | 'mark'>('verify')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || result) return
    setBusy(true)
    setError('')
    try {
      const r = await adminApi.updateUser(u.id, { email: email.trim(), emailMode: mode })
      await after()
      setResult(
        mode === 'mark' || r.verification === undefined
          ? tp.admin.edited
          : r.verification === 'sent'
            ? tp.admin.emailSent(r.user.email)
            : tp.admin.emailNotSent,
      )
    } catch (err) {
      setError(errorOf(err))
    }
    setBusy(false)
  }

  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={onOpenChange}
      title={tp.profile.changeEmail}
      footer={
        result ? (
          <Button type="button" onClick={() => onOpenChange(false)}>
            {tp.done}
          </Button>
        ) : (
          <>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {tp.cancel}
            </Button>
            <Button
              type="submit"
              form="change-person-email-form"
              disabled={busy || email.trim() === '' || email.trim() === u.email}
            >
              {busy ? tp.profile.saving : tp.admin.saveEmail}
            </Button>
          </>
        )
      }
    >
      <form id="change-person-email-form" className="grid gap-4 py-1" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tp.profile.newEmail}
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          autoComplete="off"
          autoFocus
          disabled={result !== ''}
        />
        <fieldset className="grid gap-2" disabled={result !== ''}>
          <legend className="mb-1 text-sm font-medium">{tp.admin.emailMode}</legend>
          {(
            [
              ['verify', tp.admin.modeVerify, tp.admin.modeVerifyHelp],
              ['mark', tp.admin.modeMark, tp.admin.modeMarkHelp],
            ] as const
          ).map(([value, label, help]) => (
            <label key={value} className="flex min-h-10 items-start gap-3 text-sm">
              <input
                type="radio"
                name="email-mode"
                className="mt-1 size-4 accent-[var(--brand)]"
                checked={mode === value}
                onChange={() => setMode(value)}
              />
              <span className="min-w-0">
                {label}
                <span className="block text-xs text-muted-foreground">{help}</span>
              </span>
            </label>
          ))}
        </fieldset>
        {isManaged(u) && <Managed />}
        <FormMessage>{error}</FormMessage>
        <FormMessage tone="info">{result}</FormMessage>
      </form>
    </ResponsiveDialog>
  )
}

// A password the administrator types for the person. It is never shown again; they must replace it at their next
// sign-in and their sessions end now.
export function TemporaryPasswordDialog(props: DialogProps) {
  return props.open ? <TemporaryPasswordInner {...props} /> : null
}

function TemporaryPasswordInner({ u, open, onOpenChange }: DialogProps) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await adminApi.setTemporaryPassword(u.id, password)
      toast.success(tp.admin.temporaryDone)
      onOpenChange(false)
    } catch (err) {
      setError(errorOf(err))
    }
    setBusy(false)
  }

  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={onOpenChange}
      title={tp.admin.temporaryTitle}
      description={tp.admin.temporaryHelp}
      footer={
        <>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {tp.cancel}
          </Button>
          <Button type="submit" form="temporary-password-form" disabled={busy || password.length < 8}>
            {busy ? tp.profile.saving : tp.admin.temporarySet}
          </Button>
        </>
      }
    >
      <form id="temporary-password-form" className="grid gap-4 py-1" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tp.admin.temporaryField}
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
          autoFocus
        />
        <FormMessage>{error}</FormMessage>
      </form>
    </ResponsiveDialog>
  )
}
