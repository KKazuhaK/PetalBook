import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { OwnQuota, User } from '@/api/types'
import { Field, FormMessage } from '@/components/auth/Field'
import { Button } from '@/components/ui/button'
import { bytesToGb, countToText, gbToBytes, parseCount } from '@/lib/usage'
import { tu } from '@/i18n/usage'

// What a refusal of a person's limits means, in plain words.
export function ownLimitsError(e: unknown): string {
  if (!(e instanceof ApiError)) return tu.own.failed
  switch (e.code) {
    case 'forbidden':
      return tu.own.forbidden
    case 'setting_locked':
      return tu.own.locked
    case 'setting_restricted':
      return tu.own.restricted
    case 'invalid':
      return e.message || tu.own.invalid
    default:
      return e.message || tu.own.failed
  }
}

// A person's own limits: what overrides the tenant's and the site's for them alone. Empty follows them; for spaces, 0 is no limit.
export function OwnLimits({ userId, own, onSaved }: { userId: string; own: OwnQuota; onSaved?: (user: User) => void }) {
  const qc = useQueryClient()
  // What the fields started from (the last saved values): a field left as it was is not sent, so a disk
  // limit that is no whole number of GB keeps its exact bytes, and a limit nobody touched stays as it is.
  const [base, setBase] = useState(own)
  const [spaces, setSpaces] = useState(countToText(own.spaces))
  const [bytes, setBytes] = useState(bytesToGb(own.bytes))
  const [pages, setPages] = useState(countToText(own.pages))
  const [message, setMessage] = useState<{ tone: 'error' | 'info'; text: string } | null>(null)
  const [busy, setBusy] = useState(false)

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setMessage(null)
    const s = parseCount(spaces)
    const b = gbToBytes(bytes)
    const p = parseCount(pages)
    if (s === undefined || b === undefined || p === undefined || (p !== null && p < 1) || (s !== null && s > 10000))
      return setMessage({ tone: 'error', text: tu.own.invalid })
    const quota: Partial<OwnQuota> = {}
    if (spaces !== countToText(base.spaces)) quota.spaces = s
    if (bytes !== bytesToGb(base.bytes)) quota.bytes = b
    if (pages !== countToText(base.pages)) quota.pages = p
    setBusy(true)
    try {
      const { user } = await adminApi.updateUser(userId, { quota })
      setBase(user.quota ?? { ...base, ...quota })
      void qc.invalidateQueries({ queryKey: keys.users })
      void qc.invalidateQueries({ queryKey: ['admin', 'usage'] })
      setMessage({ tone: 'info', text: tu.own.saved })
      onSaved?.(user)
    } catch (err) {
      setMessage({ tone: 'error', text: ownLimitsError(err) })
    }
    setBusy(false)
  }

  return (
    <form className="grid min-w-0 gap-3" onSubmit={(e) => void save(e)} noValidate data-slot="own-limits">
      <div className="grid gap-0.5">
        <h4 className="text-sm font-medium">{tu.own.title}</h4>
        <p className="text-xs break-words text-muted-foreground">{tu.own.help}</p>
      </div>
      <FormMessage tone={message?.tone}>{message?.text}</FormMessage>
      <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-3">
        <Field
          label={tu.own.spaces}
          value={spaces}
          inputMode="numeric"
          placeholder={tu.own.follow}
          onChange={(e) => setSpaces(e.target.value)}
        />
        <Field
          label={tu.own.bytesGb}
          value={bytes}
          inputMode="decimal"
          placeholder={tu.own.follow}
          onChange={(e) => setBytes(e.target.value)}
        />
        <Field
          label={tu.own.pages}
          value={pages}
          inputMode="numeric"
          placeholder={tu.own.follow}
          onChange={(e) => setPages(e.target.value)}
        />
      </div>
      <div>
        <Button type="submit" size="sm" disabled={busy}>
          {busy ? tu.own.saving : tu.own.save}
        </Button>
      </div>
    </form>
  )
}
