// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import type { User } from '@/api/types'
import {
  forgetLastAccount,
  lastAccount,
  noteSession,
  rememberAccount,
  safeAvatar,
  setPendingMethod,
  takePendingMethod,
} from './lastAccount'

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
})

const user = { name: 'Kazuha', username: 'kazuha', email: 'k@example.test' } as User

describe('the last account of this browser', () => {
  it('keeps the name, username, email and method, and forgets them', () => {
    rememberAccount(user, { type: 'passkey' })
    expect(lastAccount()).toEqual({ ...user, method: { type: 'passkey' } })
    forgetLastAccount()
    expect(lastAccount()).toBeNull()
  })

  it('a pending single sign-on is taken once, and only while it is recent', () => {
    const sso = { type: 'sso', kind: 'oidc', slug: 'company', name: 'Company SSO' } as const
    setPendingMethod(sso)
    expect(takePendingMethod()).toEqual(sso)
    expect(takePendingMethod()).toBeNull()
    setPendingMethod(sso)
    expect(takePendingMethod(Date.now() + 16 * 60_000)).toBeNull()
  })

  it('a session that is back from the provider keeps the method that was pending; one that began otherwise keeps what was known', () => {
    rememberAccount(user, { type: 'password' })
    setPendingMethod({ type: 'sso', kind: 'saml', slug: 'campus', name: 'Campus' })
    noteSession({ ...user, name: 'Kazuha Z' })
    expect(lastAccount()).toMatchObject({ name: 'Kazuha Z', method: { type: 'sso', slug: 'campus' } })
    noteSession(user)
    expect(lastAccount()).toMatchObject({ name: 'Kazuha', method: { type: 'sso', slug: 'campus' } })
    // Someone else, with nothing pending: a password is the guess.
    noteSession({ ...user, username: 'tang', name: 'Tang' })
    expect(lastAccount()).toMatchObject({ username: 'tang', method: { type: 'password' } })
  })

  it('storage that throws is not an error', () => {
    const real = Storage.prototype.getItem
    Storage.prototype.getItem = () => {
      throw new Error('blocked')
    }
    try {
      expect(lastAccount()).toBeNull()
      expect(takePendingMethod()).toBeNull()
    } finally {
      Storage.prototype.getItem = real
    }
  })

  it('an avatar address is this site or https, nothing else', () => {
    expect(safeAvatar('/a.png')).toBe('/a.png')
    expect(safeAvatar('https://x.example/a.png')).toBe('https://x.example/a.png')
    for (const bad of [
      '//x.example/a.png',
      'http://x.example/a.png',
      'javascript:alert(1)',
      'data:image/png;base64,AA',
      7,
    ])
      expect(safeAvatar(bad)).toBeUndefined()
  })
})
