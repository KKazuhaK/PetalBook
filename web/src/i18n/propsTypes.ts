// The shape of the words of page types, properties and the table view (see props/):
// the Chinese ones with their literals widened, so the English file must have
// exactly the same keys.

import type { propsZhCN } from './props/zh-CN.ts'
import type { Widen } from './types.ts'

export type PropsMessages = Widen<typeof propsZhCN>
