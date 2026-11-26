// An administrator's audited view of a space they are not a member of: the space is not in their list, the
// administration knows it, and opening a view records the time it lasts. (The permission to read comes with it on the
// page's side.)

import type { AdminSpace } from '../../types.ts'
import { rfc3339, type Route } from '../kit.ts'
import { spaceOf } from './files.ts'

const VIEW_MS = 30 * 60_000

export const spaceViewRoutes: Route[] = [
  [
    'GET',
    '/api/admin/spaces/:space',
    (c): AdminSpace => {
      const s = spaceOf(c)
      const owner = c.data.auth.users[0]
      return {
        id: s.info.id,
        name: s.info.name,
        personal: s.info.personal,
        inbox: s.info.inbox,
        created: '2026-01-05T08:00:00Z',
        disabled: s.info.disabled ?? false,
        owner: { id: owner.id, username: owner.username, name: owner.name },
        members: 1,
        sizeBytes: null,
        sizeAt: null,
        sync: s.info.sync ?? 'local',
        // The real answer names the tenant too; the web type does not say so.
        ...({ tenant: owner.tenant?.id } as Record<string, unknown>),
        storage: {
          cache: 'local',
          pinned: false,
          exempt: false,
          lastUsed: '2026-11-01T00:00:00Z',
          cloneBytes: 0,
          indexBytes: 0,
          assetCacheBytes: 0,
          assets: 'space',
          remote: null,
        },
        location: 'server',
        counted: true,
        view: c.data.area.views[s.info.id] ?? null,
      }
    },
  ],
  [
    'POST',
    '/api/admin/spaces/:space/view',
    (c) => {
      const s = spaceOf(c)
      const until = rfc3339(c.now() + VIEW_MS)
      c.data.area.views[s.info.id] = { since: rfc3339(c.now()), until }
      return { space: s.info.id, until }
    },
  ],
  [
    'DELETE',
    '/api/admin/spaces/:space/view',
    (c) => {
      delete c.data.area.views[spaceOf(c).info.id]
    },
  ],
]
