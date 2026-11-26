// The reads a page makes on load that have nothing to keep in the fixtures: no findings. Generic answers, so a mock run has no 404 noise.

import type { Route } from '../kit.ts'

export const quietRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/checks',
    () => ({
      counts: {
        bad_header: 0,
        broken_link: 0,
        duplicate_id: 0,
        duplicate_title: 0,
        missing_asset: 0,
        no_id: 0,
        not_in_type: 0,
        type_mismatch: 0,
      },
      items: [],
      next: '',
    }),
  ],
]
