// The administration's words, in the language of the page. Imported only by the
// administration, which is loaded on demand: the main bundle does not carry them.

import { adminEn } from './admin/en.ts'
import { adminZhCN } from './admin/zh-CN.ts'
import type { AdminMessages } from './adminTypes.ts'
import { lang } from './index.ts'

export type { AdminMessages }
export const ta: AdminMessages = lang === 'en' ? adminEn : adminZhCN
