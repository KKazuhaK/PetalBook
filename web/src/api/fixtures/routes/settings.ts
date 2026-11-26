// What a space's settings pages read beyond the lists in routes/files.ts: one template with its body.

import { find, type Route } from '../kit.ts'
import { spaceOf } from './files.ts'

export const settingsRoutes: Route[] = [
  [
    'GET',
    '/api/spaces/:space/templates/:name',
    (c) => find(spaceOf(c).templates, (t) => t.name === c.params.name, 'the template'),
  ],
]
