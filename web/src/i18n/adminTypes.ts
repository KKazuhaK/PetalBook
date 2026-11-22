// The shape of the administration's words (see admin/): the Chinese ones with
// their literals widened, so the English file must have exactly the same keys.

import type { adminZhCN } from './admin/zh-CN.ts'
import type { Widen } from './types.ts'

export type AdminMessages = Widen<typeof adminZhCN>
