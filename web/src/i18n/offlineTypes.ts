// The shape of the words of the Offline settings (see offline/): the Chinese ones with their literals widened, so the
// English file must have exactly the same keys.

import type { offlineZhCN } from './offline/zh-CN.ts'
import type { Widen } from './types.ts'

export type OfflineMessages = Widen<typeof offlineZhCN>
