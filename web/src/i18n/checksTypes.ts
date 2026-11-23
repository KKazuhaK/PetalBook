// The shape of the words of the check page (see checks/): the Chinese ones with their literals widened, so the
// English file must have exactly the same keys.

import type { checksZhCN } from './checks/zh-CN.ts'
import type { Widen } from './types.ts'

export type ChecksMessages = Widen<typeof checksZhCN>
