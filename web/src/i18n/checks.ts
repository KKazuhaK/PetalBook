// The words of the check page of a space, in the language of the page. Imported only by the page, which loads on
// demand: the main bundle does not carry them.

import { checksEn } from './checks/en.ts'
import { checksZhCN } from './checks/zh-CN.ts'
import type { ChecksMessages } from './checksTypes.ts'
import { lang } from './index.ts'

export type { ChecksMessages }
export const ct: ChecksMessages = lang === 'en' ? checksEn : checksZhCN
