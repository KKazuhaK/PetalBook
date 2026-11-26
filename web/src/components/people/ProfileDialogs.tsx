import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { keys, useAuthProviders } from '@/api/hooks'
import type { AccountInfo, SessionInfo, User } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { Avatar } from '@/components/admin/users/Avatar'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { isLang, lang, languages, setLanguage } from '@/i18n'
import { tp } from '@/i18n/people'
import { mailLang } from '@/pages/auth/MailPages'
import { ResponsiveDialog } from './ResponsiveDialog'

function useStoreUser() {
  const qc = useQueryClient()
  return (user: User) => {
    qc.setQueryData<AccountInfo>(keys.account, (prev) => prev && { ...prev, user })
    qc.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, user: { ...prev.user!, ...user } })
  }
}

// Editing who the person is: name, username and language (the avatar is made from the name). The email has its
// own dialog, and the password stays in Security. Mounted only while open, so it starts from what is saved.
export function EditProfileDialog(props: { user: User; open: boolean; onOpenChange: (open: boolean) => void }) {
  return props.open ? <EditProfileInner {...props} /> : null
}

function EditProfileInner({
  user,
  open,
  onOpenChange,
}: {
  user: User
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const store = useStoreUser()
  const [name, setName] = useState(user.name)
  const [username, setUsername] = useState(user.username)
  const [language, setLang] = useState(lang)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    const body: { name?: string; username?: string } = {}
    if (name.trim() !== user.name) body.name = name.trim()
    if (username.trim() !== user.username) body.username = username.trim()
    setBusy(true)
    setError('')
    try {
      if (Object.keys(body).length > 0) store((await api.updateAccount(body)).user)
      // The language is the browser's own choice; changing it reloads the page, so it is last.
      if (isLang(language) && language !== lang) {
        setLanguage(language)
        return
      }
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : tp.failed)
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
          <Button type="submit" form="edit-profile-form" disabled={busy || name.trim() === ''}>
            {busy ? tp.profile.saving : tp.profile.save}
          </Button>
        </>
      }
    >
      <form id="edit-profile-form" className="grid gap-4 py-1" onSubmit={(e) => void submit(e)} noValidate>
        <div className="flex items-center gap-3">
          <Avatar name={name} size="lg" />
          <div className="min-w-0">
            <div className="text-sm font-medium">{tp.profile.avatar}</div>
            <p className="text-xs text-muted-foreground">{tp.profile.avatarNote}</p>
          </div>
        </div>
        <Field
          label={tp.profile.name}
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoComplete="name"
          autoFocus
          required
        />
        <Field
          label={tp.profile.username}
          help={tp.profile.usernameHelp}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
        />
        <div className="grid gap-1.5 sm:max-w-56">
          <Label htmlFor="edit-profile-language">{tp.profile.language}</Label>
          <NativeSelect
            id="edit-profile-language"
            value={language}
            onChange={(e) => setLang(e.target.value as typeof lang)}
          >
            {languages.map((l) => (
              <option key={l.code} value={l.code}>
                {l.name}
              </option>
            ))}
          </NativeSelect>
        </div>
        <FormMessage>{error}</FormMessage>
      </form>
    </ResponsiveDialog>
  )
}

// Changing the address: the new one is verified (a link is sent when the server can mail), the old one is told.
export function ChangeEmailDialog(props: { user: User; open: boolean; onOpenChange: (open: boolean) => void }) {
  return props.open ? <ChangeEmailInner {...props} /> : null
}

function ChangeEmailInner({
  user,
  open,
  onOpenChange,
}: {
  user: User
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const store = useStoreUser()
  const mailOn = useAuthProviders(true).data?.forgot === true
  const [email, setEmail] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy || result) return
    setBusy(true)
    setError('')
    try {
      const next = (await api.updateAccount({ email: email.trim() })).user
      store(next)
      let sent = false
      if (mailOn && next.email) {
        try {
          await api.requestEmailVerify(mailLang())
          sent = true
        } catch {
          sent = false
        }
      }
      setResult(
        next.email === '' ? tp.profile.noEmail : sent ? tp.profile.emailSent(next.email) : tp.profile.emailNotSent,
      )
    } catch (err) {
      setError(err instanceof ApiError ? err.message : tp.failed)
    }
    setBusy(false)
  }

  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={onOpenChange}
      title={tp.profile.changeEmail}
      description={tp.profile.changeEmailHelp}
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
              form="change-email-form"
              disabled={busy || email.trim() === '' || email.trim() === user.email}
            >
              {busy ? tp.profile.saving : tp.profile.changeEmail}
            </Button>
          </>
        )
      }
    >
      <form id="change-email-form" className="grid gap-4 py-1" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tp.profile.newEmail}
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          autoComplete="email"
          autoFocus
          disabled={result !== ''}
        />
        <FormMessage>{error}</FormMessage>
        <FormMessage tone="info">{result}</FormMessage>
      </form>
    </ResponsiveDialog>
  )
}
