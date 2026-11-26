// The shape of the words of the add dialogs (see people/): the Chinese ones with their literals widened, so the
// English file must have exactly the same keys.

import type { peopleZhCN } from './people/zh-CN.ts'
import type { Widen } from './types.ts'

export type PeopleMessages = Widen<typeof peopleZhCN>
