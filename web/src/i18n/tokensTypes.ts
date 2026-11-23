// The shape of the words of API tokens (see tokens/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { tokensZhCN } from './tokens/zh-CN.ts'
import type { Widen } from './types.ts'

export type TokensMessages = Widen<typeof tokensZhCN>
