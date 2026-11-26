// The routes of the editor panels, the settings, people, notifications and connections, in one list for the server to
// take in.

import type { Route } from '../kit.ts'
import { accountRoutes } from './account.ts'
import { connectionRoutes } from './connections.ts'
import { dailyRoutes } from './daily.ts'
import { editorRoutes } from './editor.ts'
import { memberRoutes } from './members.ts'
import { notifyRoutes } from './notify.ts'
import { remoteRoutes } from './remote.ts'
import { settingsRoutes } from './settings.ts'
import { spaceViewRoutes } from './spaceView.ts'

export const areaRoutes: Route[] = [
  ...editorRoutes,
  ...accountRoutes,
  ...memberRoutes,
  ...settingsRoutes,
  ...dailyRoutes,
  ...spaceViewRoutes,
  ...notifyRoutes,
  ...connectionRoutes,
  ...remoteRoutes,
]
