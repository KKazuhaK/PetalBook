// The shape of the words of mail (see mail/): the Chinese ones with their literals widened, so the English file
// must have exactly the same keys.

import type { mailZhCN } from './mail/zh-CN.ts'
import type { Widen } from './types.ts'

export type MailMessages = Widen<typeof mailZhCN>
