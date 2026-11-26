import { describe, expect, it } from 'vitest'
import { tn } from '@/i18n/notify'
import { describeNotification } from './notifyText'

describe('the notice that an address moved', () => {
  it('names the address and leads to the profile', () => {
    const d = describeNotification({
      id: '1',
      kind: 'system',
      ref: '',
      payload: { event: 'email_released', email: 'tang@example.test' },
      at: '2026-11-25T08:00:00Z',
      read: false,
    })
    expect(d.title).toBe(tn.items.emailReleased.title('tang@example.test'))
    expect(d.title).toContain('tang@example.test')
    expect(d.body).toBe(tn.items.emailReleased.body)
    expect(d.to).toBe('/settings/profile')
  })
})
