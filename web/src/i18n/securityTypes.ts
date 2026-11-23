// The shape of the security page's words (see security/): the Chinese ones with their literals widened, so
// the English file must have exactly the same keys.

import type { securityZhCN } from './security/zh-CN.ts'
import type { Widen } from './types.ts'

export type SecurityMessages = Widen<typeof securityZhCN>
