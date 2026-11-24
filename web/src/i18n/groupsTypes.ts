// The shape of the words of groups (see groups/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { groupsZhCN } from './groups/zh-CN.ts'
import type { Widen } from './types.ts'

export type GroupsMessages = Widen<typeof groupsZhCN>
