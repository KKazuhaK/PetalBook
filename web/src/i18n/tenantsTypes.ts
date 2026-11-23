// The shape of the words of tenants (see tenants/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { tenantsZhCN } from './tenants/zh-CN.ts'
import type { Widen } from './types.ts'

export type TenantsMessages = Widen<typeof tenantsZhCN>
