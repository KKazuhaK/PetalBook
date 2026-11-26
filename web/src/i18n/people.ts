// The words of the dialogs that add people: the picker, the CSV import, the invitation links.

import { peopleEn } from './people/en.ts'
import { peopleZhCN } from './people/zh-CN.ts'
import type { PeopleMessages } from './peopleTypes.ts'
import { lang } from './index.ts'

export type { PeopleMessages }
export const tp: PeopleMessages = lang === 'en' ? peopleEn : peopleZhCN
