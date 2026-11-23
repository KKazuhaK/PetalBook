import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { api, ApiError } from '@/api/client'
import type { User } from '@/api/types'
import { Button } from '@/components/ui/button'
import { getCredential, passkeyFailure } from '@/lib/webauthn'
import { t } from '@/i18n'
import { ts } from '@/i18n/signin'

// Signs in with a passkey: the server's options go to the browser, whose credential goes back (docs/api.md,
// "Passkeys"). With a username the server names that person's passkeys; without one the browser offers the
// ones it has for this site.
export function PasskeyButton({ username, ret, onDone, onError, disabled }: { username?: string; ret: string; onDone: (r: { user: User; return: string }) => void; onError: (message: string, e?: unknown) => void; disabled?: boolean }) {
  const [busy, setBusy] = useState(false)
  const go = async () => {
    if (busy) return
    setBusy(true)
    onError('')
    try {
      const { ceremony, options } = await api.passkeyBegin(username?.trim() || undefined)
      const credential = await getCredential(options)
      onDone(await api.passkeyFinish(ceremony, ret, credential))
      return
    } catch (e) {
      if (e instanceof ApiError) onError(e.code === 'local_login_refused' ? ts.login.localRefused : e.code === 'rate_limited' ? ts.login.rateLimited(t.auth.wait(e.retryAfter ?? 60)) : e.message, e)
      else {
        const why = passkeyFailure(e)
        onError(why === 'cancelled' ? ts.login.passkeyCancelled : why === 'unsupported' ? ts.login.passkeyUnsupported : ts.login.passkeyFailed, e)
      }
    }
    setBusy(false)
  }
  return (
    <Button type="button" variant="outline" className="w-full min-w-0 gap-2" disabled={busy || disabled} onClick={() => void go()}>
      <KeyRound aria-hidden="true" />
      <span className="truncate">{busy ? ts.login.passkeyWorking : ts.login.passkeyButton}</span>
    </Button>
  )
}
