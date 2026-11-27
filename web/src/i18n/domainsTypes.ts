// The shape of the words of the email domains (see domains/): the Chinese ones with their literals widened, so the
// English file must have exactly the same keys.

import type { domainsZhCN } from './domains/zh-CN.ts'
import type { Widen } from './types.ts'

export type DomainsMessages = Widen<typeof domainsZhCN>
