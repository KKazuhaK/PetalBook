import { describe, expect, it } from 'vitest'
import kinds from './notifyKinds.json'
import { notifyEn } from './notify/en'
import { notifyZhCN } from './notify/zh-CN'
import { NOTIFY_KINDS } from '@/api/types'
import { describeNotification } from '@/components/notifications/notifyText'
import { tn } from './notify'

// notifyKinds.json is written by a Go test (internal/store/notifications_test.go) from store.NotifyKinds, so a kind added
// there fails this test until the web app lists it, filters by it and has its sentence in both languages.
describe('the notification kinds of the server', () => {
  it('are the kinds the web app lists', () => {
    expect([...NOTIFY_KINDS]).toEqual(kinds)
  })

  for (const [name, table] of [
    ['zh-CN', notifyZhCN],
    ['en', notifyEn],
  ] as const) {
    it(`${name} has a filter for every kind`, () => {
      const filters = table.filters as Record<string, string>
      expect(kinds.filter((k) => !filters[k]?.trim())).toEqual([])
    })
  }

  it('each has a sentence of its own, not the plain notice of an unknown kind', () => {
    for (const kind of kinds) {
      const d = describeNotification({
        id: '1',
        kind,
        ref: 'SP',
        payload: {
          space: 'Team',
          by: 'Ada',
          email: 'a@example.test',
          fields: ['name'],
          role: 'editor',
          counts: {},
          total: 1,
        },
        at: '2026-11-25T08:00:00Z',
        read: false,
      })
      expect(d.title, kind).not.toBe(tn.items.unknown)
      expect(d.title.trim(), kind).not.toBe('')
    }
  })
})
