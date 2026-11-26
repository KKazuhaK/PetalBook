import { describe, expect, it } from 'vitest'
import { tn } from '@/i18n/notify'
import { describeNotification } from './notifyText'

const note = (kind: string, payload: Record<string, unknown>) => ({
  id: '1',
  kind,
  ref: '',
  payload,
  at: '2026-11-25T08:00:00Z',
  read: false,
})

describe('the notice that an address moved', () => {
  it('names the address and leads to the profile', () => {
    const d = describeNotification(note('email_released', { email: 'tang@example.test' }))
    expect(d.title).toBe(tn.items.emailReleased.title('tang@example.test'))
    expect(d.title).toContain('tang@example.test')
    expect(d.body).toBe(tn.items.emailReleased.body)
    expect(d.to).toBe('/settings/profile')
  })
})

describe('the notice that someone changed the account', () => {
  it('names who and which fields, never a value', () => {
    const d = describeNotification(note('account_changed', { fields: ['name', 'email'], by: 'Ada' }))
    expect(d.title).toBe(tn.items.accountChanged.title('Ada'))
    expect(d.title).toContain('Ada')
    expect(d.body).toBe(
      tn.items.accountChanged.body(
        [tn.items.accountChanged.fields.name, tn.items.accountChanged.fields.email].join(tn.items.accountChanged.join),
      ),
    )
    expect(d.title).not.toBe(tn.items.unknown)
  })
})

describe('the notice of a temporary password', () => {
  it('names who set it', () => {
    const d = describeNotification(note('password_set', { by: 'Ada' }))
    expect(d.title).toBe(tn.items.passwordSet.title('Ada'))
    expect(d.title).toContain('Ada')
    expect(d.body).toBe(tn.items.passwordSet.body)
  })
})
