// The shape of the words of quotas and usage (see usage/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { usageZhCN } from './usage/zh-CN.ts'
import type { Widen } from './types.ts'

export type UsageMessages = Widen<typeof usageZhCN>
