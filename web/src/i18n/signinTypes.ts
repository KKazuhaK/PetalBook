// The shape of the sign-in words (see signin/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { signinZhCN } from './signin/zh-CN.ts'
import type { Widen } from './types.ts'

export type SigninMessages = Widen<typeof signinZhCN>
