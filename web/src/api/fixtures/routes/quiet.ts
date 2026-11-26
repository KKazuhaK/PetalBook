// The reads a page makes on load that have nothing to keep in the fixtures: no stored preferences, no daily text
// source. Generic answers, so a mock run has no 404 noise.

import type { Route } from '../kit.ts'

export const quietRoutes: Route[] = [
  ['GET', '/api/prefs', () => ({ personal: false, prefs: {} })],
  ['GET', '/api/spaces/:space/daily', () => ({ page: null, reason: 'no_source' })],
]
