import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Ellipsis, MailCheck, Plus, Star, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { api, ApiError } from '@/api/client'
import { keys, useAuthProviders } from '@/api/hooks'
import type { AccountEmail, AccountInfo, SessionInfo } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { ResponsiveDialog } from '@/components/people/ResponsiveDialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useIsMobile } from '@/hooks/use-mobile'
import { t } from '@/i18n'
import { tm } from '@/i18n/mail'
import { mailLang } from '@/pages/auth/MailPages'
import { SettingsSection } from './SettingsParts'

// What an address is: primary, and verified, not verified or only marked by an administrator. Shared by the person's
// own list and an administrator's read-only view of it.
export function EmailBadges({ e }: { e: AccountEmail }) {
  if (e.pending)
    return (
      <Badge variant="outline" title={tm.emails.pendingHelp}>
        {tm.emails.pending}
      </Badge>
    )
  return (
    <>
      {e.primary && <Badge>{tm.emails.primary}</Badge>}
      {e.verified && e.markedByAdmin ? (
        <Badge variant="outline" title={tm.emails.markedHelp}>
          {tm.emails.marked}
        </Badge>
      ) : (
        <Badge variant={e.verified ? 'secondary' : 'outline'}>
          {e.verified ? tm.emails.verified : tm.emails.unverified}
        </Badge>
      )}
    </>
  )
}

// An administrator's view of a person's addresses: the same list, nothing to change.
export function EmailList({ emails }: { emails: AccountEmail[] }) {
  return (
    <ul className="divide-y rounded-lg border" aria-label={tm.emails.title} data-slot="email-list">
      {emails.map((e) => (
        <li key={e.email} className="flex min-w-0 flex-wrap items-center gap-2 px-3 py-2.5" data-email={e.email}>
          <span className="min-w-0 text-sm break-all">{e.email}</span>
          <EmailBadges e={e} />
        </li>
      ))}
    </ul>
  )
}

const errorText = (err: unknown) => {
  if (!(err instanceof ApiError)) return tm.account.failed
  if (err.code === 'mail_not_configured') return tm.account.notConfigured
  if (err.code === 'rate_limited') return tm.account.rateLimited(t.auth.wait(err.retryAfter ?? 60))
  return err.message
}

// The person's addresses while their organisation allows more than one (accounts.multipleEmails): the list with what
// each one is, asking for a link again, making a verified one primary, removing one that is not, and adding one.
export function EmailsSection({ account }: { account: AccountInfo }) {
  const qc = useQueryClient()
  const mobile = useIsMobile()
  const mailOn = useAuthProviders(true).data?.forgot === true
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<AccountEmail | null>(null)
  const [busy, setBusy] = useState('')
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const emails = account.emails ?? []

  const refresh = () => qc.invalidateQueries({ queryKey: keys.account })
  const run = async (key: string, fn: () => Promise<void>) => {
    if (busy) return
    setBusy(key)
    setMessage(null)
    try {
      await fn()
    } catch (err) {
      setMessage({ tone: 'error', text: errorText(err) })
    }
    setBusy('')
  }
  const verify = (e: AccountEmail) =>
    run(`verify:${e.email}`, async () => {
      await (e.primary ? api.requestEmailVerify(mailLang()) : api.resendEmailVerify(e.email, mailLang()))
      setMessage({ tone: 'info', text: tm.emails.sent(e.email) })
    })
  const makePrimary = (e: AccountEmail) =>
    run(`primary:${e.email}`, async () => {
      const { user } = await api.makePrimaryEmail(e.email)
      qc.setQueryData<SessionInfo>(keys.session, (prev) => prev && { ...prev, user: { ...prev.user!, ...user } })
      await refresh()
      toast.success(tm.emails.madePrimary(e.email))
    })
  const remove = async () => {
    const target = removing
    setRemoving(null)
    if (!target) return
    await run(`remove:${target.email}`, async () => {
      await api.removeEmail(target.email)
      await refresh()
      toast.success(tm.emails.removed(target.email))
    })
  }

  return (
    <SettingsSection title={tm.emails.title} id="emails-heading" description={tm.emails.help}>
      <ul className="divide-y rounded-lg border" aria-label={tm.emails.title} data-slot="email-list">
        {emails.map((e) => {
          const pending = e.pending === true
          const canVerify = mailOn && !pending && (!e.verified || e.markedByAdmin === true)
          const canPrimary = !pending && !e.primary && e.verified && e.markedByAdmin !== true
          const canRemove = !e.primary
          const any = canVerify || canPrimary || canRemove
          return (
            <li
              key={e.email}
              className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 px-3 py-2.5"
              data-email={e.email}
            >
              <div className="flex min-w-0 flex-1 basis-48 flex-wrap items-center gap-2">
                <span className="min-w-0 text-sm font-medium break-all">{e.email}</span>
                <EmailBadges e={e} />
              </div>
              {any && mobile && (
                <DropdownMenu>
                  <DropdownMenuTrigger
                    aria-label={tm.emails.actionsFor(e.email)}
                    disabled={busy !== ''}
                    className="inline-flex size-10 shrink-0 items-center justify-center rounded-lg border text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-popup-open:bg-muted"
                  >
                    <Ellipsis aria-hidden="true" />
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="min-w-44">
                    {canVerify && (
                      <DropdownMenuItem className="min-h-10" onClick={() => void verify(e)}>
                        <MailCheck />
                        {tm.emails.verify}
                      </DropdownMenuItem>
                    )}
                    {canPrimary && (
                      <DropdownMenuItem className="min-h-10" onClick={() => void makePrimary(e)}>
                        <Star />
                        {tm.emails.makePrimary}
                      </DropdownMenuItem>
                    )}
                    {canRemove && (
                      <DropdownMenuItem className="min-h-10 text-destructive" onClick={() => setRemoving(e)}>
                        <Trash2 />
                        {tm.emails.remove}
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
              {any && !mobile && (
                <div className="flex flex-wrap gap-2">
                  {canVerify && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy !== ''}
                      aria-label={t.fmt.labelled(tm.emails.verify, e.email)}
                      onClick={() => void verify(e)}
                    >
                      <MailCheck aria-hidden="true" />
                      {busy === `verify:${e.email}` ? tm.emails.sending : tm.emails.verify}
                    </Button>
                  )}
                  {canPrimary && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy !== ''}
                      aria-label={t.fmt.labelled(tm.emails.makePrimary, e.email)}
                      onClick={() => void makePrimary(e)}
                    >
                      <Star aria-hidden="true" />
                      {tm.emails.makePrimary}
                    </Button>
                  )}
                  {canRemove && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={busy !== ''}
                      aria-label={t.fmt.labelled(tm.emails.remove, e.email)}
                      onClick={() => setRemoving(e)}
                    >
                      <Trash2 aria-hidden="true" />
                      {tm.emails.remove}
                    </Button>
                  )}
                </div>
              )}
            </li>
          )
        })}
      </ul>
      {emails.some((e) => e.pending) && <p className="text-xs text-muted-foreground">{tm.emails.pendingHelp}</p>}
      {emails.some((e) => e.markedByAdmin) && <p className="text-xs text-muted-foreground">{tm.emails.markedHelp}</p>}
      {mailOn && (
        <div>
          <Button type="button" variant="outline" className="h-10" onClick={() => setAdding(true)}>
            <Plus aria-hidden="true" />
            {tm.emails.add}
          </Button>
        </div>
      )}
      {message && <FormMessage tone={message.tone}>{message.text}</FormMessage>}
      <AddEmailDialog open={adding} onOpenChange={setAdding} onAdded={() => void refresh()} />
      <AlertDialog open={removing !== null} onOpenChange={(open) => !open && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{tm.emails.removeTitle}</AlertDialogTitle>
            <AlertDialogDescription className="break-words">
              {removing ? tm.emails.removeBody(removing.email) : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tm.emails.cancel}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={() => void remove()}>
              {tm.emails.remove}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

// Adding an address: it is sent a link; it shows (unverified) in the list at once, or once proved when another account
// held it unverified. Mounted only while open, so it starts empty.
function AddEmailDialog(props: { open: boolean; onOpenChange: (open: boolean) => void; onAdded: () => void }) {
  return props.open ? <AddEmailInner {...props} /> : null
}

function AddEmailInner({
  open,
  onOpenChange,
  onAdded,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdded: () => void
}) {
  const [email, setEmail] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [sent, setSent] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const value = email.trim()
    if (busy || sent || value === '') return
    setBusy(true)
    setError('')
    try {
      await api.addEmail(value, mailLang())
      setSent(tm.emails.sent(value))
      onAdded()
    } catch (err) {
      setError(errorText(err))
    }
    setBusy(false)
  }
  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={onOpenChange}
      title={tm.emails.addTitle}
      description={tm.emails.addHelp}
      footer={
        sent ? (
          <Button type="button" onClick={() => onOpenChange(false)}>
            {tm.emails.done}
          </Button>
        ) : (
          <>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {tm.emails.cancel}
            </Button>
            <Button type="submit" form="add-email-form" disabled={busy || email.trim() === ''}>
              {busy ? tm.emails.sending : tm.emails.send}
            </Button>
          </>
        )
      }
    >
      <form id="add-email-form" className="grid gap-4 py-1" onSubmit={(e) => void submit(e)} noValidate>
        <Field
          label={tm.emails.address}
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          autoComplete="email"
          autoFocus
          maxLength={254}
          disabled={sent !== ''}
        />
        <FormMessage>{error}</FormMessage>
        <FormMessage tone="info">{sent}</FormMessage>
      </form>
    </ResponsiveDialog>
  )
}
