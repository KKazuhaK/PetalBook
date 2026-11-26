import { useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/api/client'
import { adminApi } from '@/api/admin'
import { keys } from '@/api/hooks'
import type { OwnQuota, Role, User } from '@/api/types'
import { t } from '@/i18n'
import { tsec } from '@/i18n/security'

export interface UserPatch {
  name?: string
  username?: string
  emailMode?: 'verify' | 'mark'
  role?: Role
  disabled?: boolean
  passkeyRequired?: boolean
  email?: string
  emailVerified?: boolean
  siteAdmin?: boolean
  quota?: Partial<OwnQuota>
  remoteExempt?: boolean
}

// What a refusal of a change to a person means, in plain words.
export function conflictMessage(e: unknown, disabling: boolean): string {
  if (e instanceof ApiError && e.code === 'passkey_unavailable') return tsec.users.passkeyUnavailableCode
  if (e instanceof ApiError && e.code === 'conflict')
    return disabling && /自己|themself|yourself/i.test(e.serverMessage ?? e.message)
      ? t.users.selfDisable
      : e.message || t.users.lastAdmin
  return e instanceof ApiError ? e.message : t.users.changeFailed
}

// Changing a person: one request, then every list and page that shows them is read again. A refusal says why in
// `error` (and the lists are read again in case what was shown is out of date).
export function useUserChange() {
  const qc = useQueryClient()
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<{ id: string; message: string } | null>(null)
  const messageRef = useRef<HTMLParagraphElement>(null)
  const change = async (u: Pick<User, 'id'>, patch: UserPatch): Promise<User | null> => {
    setBusy(u.id)
    setError(null)
    try {
      const { user } = await adminApi.updateUser(u.id, patch)
      void qc.invalidateQueries({ queryKey: keys.users })
      setBusy(null)
      return user
    } catch (e) {
      void qc.invalidateQueries({ queryKey: keys.users })
      setError({ id: u.id, message: conflictMessage(e, patch.disabled === true) })
      setTimeout(() => messageRef.current?.focus(), 0)
      setBusy(null)
      return null
    }
  }
  return { change, busy, error, messageRef }
}
