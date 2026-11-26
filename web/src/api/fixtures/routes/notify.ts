// The notification centre and the person's own preferences. A notification is a record a test puts in the data
// (`addNotification`); nothing in the fixtures makes one by itself.

import type { NotificationItem } from '../../types.ts'
import type { Route } from '../kit.ts'

const unread = (items: NotificationItem[]) => Math.min(100, items.filter((i) => !i.read).length)

export const notifyRoutes: Route[] = [
  [
    'GET',
    '/api/notifications',
    (c) => {
      const all = c.data.area.notifications
      const kind = c.query.get('kind') ?? ''
      const unreadOnly = c.query.get('unread') === '1'
      const limit = Number(c.query.get('limit')) || 20
      const cursor = c.query.get('cursor') ?? ''
      const matching = all.filter((i) => (kind === '' || i.kind === kind) && (!unreadOnly || !i.read))
      // A problem is a state that holds now, not an event: it is listed on the first page only, before the events.
      const derived = cursor === '' ? matching.filter((i) => i.derived) : []
      const events = matching
        .filter((i) => !i.derived)
        .sort((a, b) => (a.id < b.id ? 1 : -1))
        .filter((i) => cursor === '' || i.id < cursor)
      const page = events.slice(0, limit)
      return {
        items: [...derived, ...page].map((i) => structuredClone(i)),
        next: events.length > limit ? page[page.length - 1].id : '',
        unread: unread(all),
      }
    },
  ],
  ['GET', '/api/notifications/unread', (c) => ({ count: unread(c.data.area.notifications) })],
  [
    'POST',
    '/api/notifications/read',
    (c) => {
      const all = c.data.area.notifications
      for (const i of all) if ((c.body.ids as string[]).includes(i.id)) i.read = true
      return { unread: unread(all) }
    },
  ],
  [
    'POST',
    '/api/notifications/read-all',
    (c) => {
      const all = c.data.area.notifications
      for (const i of all) if (!c.body.kind || i.kind === c.body.kind) i.read = true
      return { unread: unread(all) }
    },
  ],

  // the person's own preferences: kept for a person who has a personal space (accounts mode)
  [
    'GET',
    '/api/prefs',
    (c) =>
      c.data.auth.mode === 'accounts'
        ? { personal: true, prefs: { ...c.data.area.prefs } }
        : { personal: false, prefs: {} },
  ],
  [
    'PATCH',
    '/api/prefs',
    (c) => {
      if (c.data.auth.mode !== 'accounts') return { personal: false }
      for (const [k, v] of Object.entries(c.body.set as Record<string, unknown>)) {
        if (v === null) delete c.data.area.prefs[k]
        else c.data.area.prefs[k] = v
      }
      return { personal: true }
    },
  ],
]
