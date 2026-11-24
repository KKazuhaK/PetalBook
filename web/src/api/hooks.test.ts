import { afterEach, describe, expect, it, vi } from 'vitest'
import { MutationObserver, QueryClient } from '@tanstack/react-query'
import { applySession, keys, putTableRow, lastSpace, refreshSpaceAccess, rememberSpace, secretMutation } from './hooks'
import type { Page, SessionInfo, User } from './types'

const user = (over: Partial<User> = {}): User => ({
  id: 'u1',
  username: 'ada',
  name: 'Ada',
  email: '',
  emailVerified: false,
  role: 'member',
  disabled: false,
  passkeyRequired: false,
  created: '2026-11-21T08:30:00Z',
  ...over,
})
const session = (over: Partial<SessionInfo> = {}): SessionInfo => ({
  mode: 'accounts',
  user: null,
  setup: false,
  methods: { password: true, oidc: null },
  ...over,
})

describe('signing in and out', () => {
  it('replaces the session and drops every cached query, so nothing of one person shows for another', () => {
    const qc = new QueryClient()
    qc.setQueryData(keys.session, session({ user: user() }))
    qc.setQueryData(keys.list('sp1', {}), { items: [{ id: 'secret' }] })
    qc.setQueryData(keys.account, { user: user() })
    applySession(qc, user({ id: 'u2', name: 'Bo' }))
    expect(qc.getQueryData(keys.list('sp1', {}))).toBeUndefined()
    expect(qc.getQueryData(keys.account)).toBeUndefined()
    expect(qc.getQueryData<SessionInfo>(keys.session)).toMatchObject({
      mode: 'accounts',
      setup: false,
      user: { id: 'u2' },
      methods: { password: true },
    })
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
      return JSON.stringify(
        qc
          .getMutationCache()
          .getAll()
          .map((m) => m.state),
      )
    }
    expect(await run(secretMutation)).not.toContain('SECRET-123')
    // Without it the default keeps the variables around: this is what the option is for.
    expect(await run({})).toContain('SECRET-123')
  })
})

describe('a request refused for access or not found inside a space', () => {
  afterEach(() => vi.useRealTimers())
  const setup = () => {
    vi.useFakeTimers()
    const qc = new QueryClient()
    const stale = () => [
      qc.getQueryState(keys.spaces)?.isInvalidated,
      qc.getQueryState(keys.viewedSpace('sp1'))?.isInvalidated,
    ]
    const answered = () => {
      qc.setQueryData(keys.spaces, { spaces: [] })
      qc.setQueryData(keys.viewedSpace('sp1'), null)
    }
    answered()
    return { qc, stale, answered }
  }
  it('looks at the viewed space again at most every five seconds when it is only a 404, like the list', () => {
    const { qc, stale, answered } = setup()
    refreshSpaceAccess(qc, 'sp1', true)
    expect(stale()).toEqual([false, false])
    // Not dropped: it is made once the window is over.
    vi.advanceTimersByTime(4_999)
    expect(stale()).toEqual([false, false])
    vi.advanceTimersByTime(1)
    expect(stale()).toEqual([true, true])
    // After the window it is asked at once.
    answered()
    vi.advanceTimersByTime(5_000)
    refreshSpaceAccess(qc, 'sp1', true)
    expect(stale()).toEqual([true, true])
    // A refusal for access is never throttled.
    answered()
    refreshSpaceAccess(qc, 'sp1', false)
    expect(stale()).toEqual([true, true])
  })
  it('makes one recheck at the end of the window however many 404s come inside it', () => {
    const { qc } = setup()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    for (let i = 0; i < 20; i++) {
      refreshSpaceAccess(qc, 'sp1', true)
      vi.advanceTimersByTime(100)
    }
    expect(spy).not.toHaveBeenCalled()
    vi.advanceTimersByTime(5_000)
    expect(spy).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(20_000)
    expect(spy).toHaveBeenCalledTimes(2)
  })
  it('leaves the recheck out when the query was asked again meanwhile', () => {
    const { qc, stale, answered } = setup()
    refreshSpaceAccess(qc, 'sp1', true)
    vi.advanceTimersByTime(3_000)
    answered()
    vi.advanceTimersByTime(2_000)
    expect(stale()).toEqual([false, false])
  })
  it('still makes the recheck when the timer goes off a little before the clock says the window is over', () => {
    const { qc, stale } = setup()
    refreshSpaceAccess(qc, 'sp1', true)
    // The event loop counts from when it last looked at the time, so a timer can run early by the clock.
    vi.setSystemTime(Date.now() - 3)
    vi.advanceTimersByTime(5_000)
    expect(stale()).toEqual([false, false])
    vi.advanceTimersByTime(3)
    expect(stale()).toEqual([true, true])
  })
  it('counts the window from a failed fetch as well', async () => {
    vi.useFakeTimers()
    const qc = new QueryClient()
    await qc
      .fetchQuery({ queryKey: keys.viewedSpace('sp1'), queryFn: () => Promise.reject(new Error('down')), retry: false })
      .catch(() => {})
    await qc
      .fetchQuery({ queryKey: keys.spaces, queryFn: () => Promise.reject(new Error('down')), retry: false })
      .catch(() => {})
    const spy = vi.spyOn(qc, 'invalidateQueries')
    refreshSpaceAccess(qc, 'sp1', true)
    expect(spy).not.toHaveBeenCalled()
    vi.advanceTimersByTime(5_000)
    expect(spy).toHaveBeenCalledTimes(2)
  })
})

describe('putTableRow', () => {
  const row = { id: 'p1', rev: 'r0', updated: 'x', props: {}, partialProps: ['old'] }
  const page = (over: Partial<Page>): Page => ({
    id: 'p1',
    path: 'p1.md',
    folder: '',
    rev: 'r1',
    updated: 'y',
    meta: { title: 'T', hex: 16 },
    body: '',
    ...over,
  })
  const key = [...keys.pages('sp'), 'table', 'x']
  const read = (qc: QueryClient) =>
    (qc.getQueryData(key) as { pages: { items: Record<string, unknown>[] }[] }).pages[0].items[0]

  it('takes the partial keys from the server, which sees how the file spells a value, not from the header it was given', () => {
    const qc = new QueryClient()
    qc.setQueryData(key, { pages: [{ items: [row] }], pageParams: [] })
    // 0x10 is 16 in meta: the browser cannot tell it from a canonical 16.
    putTableRow(qc, 'sp', page({ partialProps: ['hex'] }))
    expect(read(qc)).toMatchObject({ rev: 'r1', props: { hex: 16 }, partialProps: ['hex'] })
    // A response without the list says there are none.
    putTableRow(qc, 'sp', page({ rev: 'r2' }))
    expect(read(qc)).toMatchObject({ rev: 'r2' })
    expect('partialProps' in read(qc)).toBe(false)
  })
})
