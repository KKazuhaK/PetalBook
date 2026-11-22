import { afterEach, describe, expect, it } from 'vitest'
import { MutationObserver, QueryClient } from '@tanstack/react-query'
import { applySession, keys, lastSpace, rememberSpace, secretMutation } from './hooks'
import type { SessionInfo, User } from './types'

const user = (over: Partial<User> = {}): User => ({ id: 'u1', username: 'ada', name: 'Ada', email: '', role: 'member', disabled: false, created: '2026-11-21T08:30:00Z', ...over })
const session = (over: Partial<SessionInfo> = {}): SessionInfo => ({ mode: 'accounts', user: null, setup: false, methods: { password: true, oidc: null }, ...over })

describe('signing in and out', () => {
  it('replaces the session and drops every cached query, so nothing of one person shows for another', () => {
    const qc = new QueryClient()
    qc.setQueryData(keys.session, session({ user: user() }))
    qc.setQueryData(keys.list('sp1', {}), { items: [{ id: 'secret' }] })
    qc.setQueryData(keys.account, { user: user() })
    applySession(qc, user({ id: 'u2', name: 'Bo' }))
    expect(qc.getQueryData(keys.list('sp1', {}))).toBeUndefined()
    expect(qc.getQueryData(keys.account)).toBeUndefined()
    expect(qc.getQueryData<SessionInfo>(keys.session)).toMatchObject({ mode: 'accounts', setup: false, user: { id: 'u2' }, methods: { password: true } })
  })

  it('signing out keeps the mode and the sign-in methods and clears the user', () => {
    const qc = new QueryClient()
    qc.setQueryData(keys.session, session({ user: user() }))
    applySession(qc, null)
    expect(qc.getQueryData<SessionInfo>(keys.session)).toEqual(session({ user: null }))
  })

  it('finishing the first-run setup closes `setup`', () => {
    const qc = new QueryClient()
    qc.setQueryData(keys.session, session({ setup: true }))
    applySession(qc, user({ role: 'admin' }))
    expect(qc.getQueryData<SessionInfo>(keys.session)?.setup).toBe(false)
  })
})

describe('the space used last', () => {
  afterEach(() => localStorage.clear())
  it('is remembered, and blocked storage is the same as never having been told', () => {
    expect(lastSpace()).toBeNull()
    rememberSpace('01JD3K8Q7Z')
    expect(lastSpace()).toBe('01JD3K8Q7Z')
    const real = Storage.prototype.getItem
    Storage.prototype.getItem = () => {
      throw new DOMException('denied', 'SecurityError')
    }
    try {
      expect(lastSpace()).toBeNull()
    } finally {
      Storage.prototype.getItem = real
    }
  })
})

describe('a mutation that carries a secret', () => {
  it('is not kept in the cache once nothing shows it', async () => {
    const qc = new QueryClient()
    const run = async (opts: object) => {
      const obs = new MutationObserver(qc, { ...opts, mutationFn: async (v: { token: string }) => v.token.length })
      const off = obs.subscribe(() => {})
      await obs.mutate({ token: 'SECRET-123' })
      obs.reset()
      off()
      await new Promise((r) => setTimeout(r, 0))
      return JSON.stringify(qc.getMutationCache().getAll().map((m) => m.state))
    }
    expect(await run(secretMutation)).not.toContain('SECRET-123')
    // Without it the default keeps the variables around: this is what the option is for.
    expect(await run({})).toContain('SECRET-123')
  })
})
