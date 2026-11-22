// The words of page types, the property panel and the table view, in the language
// of the page. Imported only by those, which load on demand: the main bundle does
// not carry them.

import { propsEn } from './props/en.ts'
import { propsZhCN } from './props/zh-CN.ts'
import type { PropsMessages } from './propsTypes.ts'
import { lang } from './index.ts'

export type { PropsMessages }
export const tp: PropsMessages = lang === 'en' ? propsEn : propsZhCN
