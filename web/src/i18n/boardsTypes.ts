// The shape of the words of the board, reading and the view tools (see boards/): the Chinese ones with their
// literals widened, so the English file must have exactly the same keys.

import type { boardsZhCN } from './boards/zh-CN.ts'
import type { Widen } from './types.ts'

export type BoardsMessages = Widen<typeof boardsZhCN>
