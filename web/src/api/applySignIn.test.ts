import { describe, expect, it } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { applySignIn, keys } from './hooks'
import type { SessionInfo, SignInAnswer } from './types'

describe('applySignIn', () => {
  it('drops why an earlier session ended, so a later sign-out does not show it again', () => {
    const qc = new QueryClient()
    qc.setQueryData<SessionInfo>(keys.session, {
      mode: 'accounts',
      setup: false,
      endedReason: 'disabled',
    } as SessionInfo)
    const answer = { user: { id: 'U1', permissions: [] }, hold: null } as unknown as SignInAnswer
    applySignIn(qc, answer)
    const now = qc.getQueryData<SessionInfo>(keys.session)
    expect(now?.endedReason).toBeUndefined()
    expect(now?.user).toBe(answer.user)
    expect(now?.mode).toBe('accounts')
  })
})
