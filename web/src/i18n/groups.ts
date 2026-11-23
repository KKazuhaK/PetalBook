// The words of groups, in the language of the page: the administration's Groups page, the groups of a space's
// members, the groups mapping of a sign-in provider and "My groups" of the account settings.

import { groupsEn } from './groups/en.ts'
import { groupsZhCN } from './groups/zh-CN.ts'
import type { GroupsMessages } from './groupsTypes.ts'
import { lang } from './index.ts'

export type { GroupsMessages }
export const tg: GroupsMessages = lang === 'en' ? groupsEn : groupsZhCN
