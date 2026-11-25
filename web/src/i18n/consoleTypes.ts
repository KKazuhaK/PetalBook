// The shape of the console's words (see console/): the Chinese ones with their literals widened, so the English file
// must have exactly the same keys.

import type { consoleZhCN } from './console/zh-CN.ts'
import type { Widen } from './types.ts'

export type ConsoleMessages = Widen<typeof consoleZhCN>
