// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import type { User } from '@/api/types'
import {
  clearPendingMethod,
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

  it('a single sign-on that was left half way does not stand for a later sign-in some other way', () => {
    setPendingMethod({ type: 'sso', kind: 'oidc', slug: 'company', name: 'Company SSO' })
    // The person came back without finishing, and signed in with their password.
    rememberAccount(user, { type: 'password' })
    noteSession(user)
    expect(lastAccount()).toMatchObject({ method: { type: 'password' } })
    setPendingMethod({ type: 'sso', kind: 'oidc', slug: 'company', name: 'Company SSO' })
    // Or any other way (an invitation, a link by email, the first setup): the pending note is dropped.
    clearPendingMethod()
    noteSession(user)
    expect(lastAccount()).toMatchObject({ method: { type: 'password' } })
    expect(takePendingMethod()).toBeNull()
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
    expect(safeAvatar('/api/users/u1/avatar?v=2')).toBe('/api/users/u1/avatar?v=2')
    for (const bad of [
      '//x.example/a.png',
      '/\\x.example/a.png',
      '/\\/x.example/a.png',
      '\\\\x.example/a.png',
      '/\t/x.example/a.png',
      '/\n/x.example/a.png',
      '/',
      'a.png',
      '/a b.png',
      'https://x.example/a\n.png',
      'https:\\\\x.example/a.png',
      'HTTPS://x.example/a.png',
      'http://x.example/a.png',
      'javascript:alert(1)',
      'data:image/png;base64,AA',
      7,
    ])
      expect(safeAvatar(bad)).toBeUndefined()
  })
})
