// The shape of the home page's words (see home/): the Chinese ones with their literals widened, so the English file
// must have exactly the same keys. Lists of sentences only have to be lists of text; each language has as many as it likes.

import type { homeZhCN } from './home/zh-CN.ts'
import type { Widen } from './types.ts'

export type HomeMessages = Widen<typeof homeZhCN>
export type GreetMessages = HomeMessages['greet']
