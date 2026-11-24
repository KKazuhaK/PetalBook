// The words of storage management (milestone 3.5): the summary, the lists of people and spaces, the recycle bin,
// deleting a space or a person, bulk actions, exports and the audited view of a space. Picked in the page's language.

import { manageEn } from './manage/en.ts'
import { manageZhCN } from './manage/zh-CN.ts'
import type { ManageMessages } from './manageTypes.ts'
import { lang } from './index.ts'

export type { ManageMessages }
export const tg: ManageMessages = lang === 'en' ? (manageEn as ManageMessages) : manageZhCN
