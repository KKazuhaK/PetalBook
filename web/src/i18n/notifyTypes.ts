// The shape of the words of the notification centre (see notify/): the Chinese ones with their literals widened, so the
// English file must have exactly the same keys.

import type { notifyZhCN } from './notify/zh-CN.ts'
import type { Widen } from './types.ts'

export type NotifyMessages = Widen<typeof notifyZhCN>
