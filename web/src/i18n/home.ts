// The words of the home page and of the pages that set it up (the space's "Home" settings), in the language of the
// page. Imported only by those, which load on demand: the main bundle does not carry them.

import { homeEn } from './home/en.ts'
import { homeZhCN } from './home/zh-CN.ts'
import type { HomeMessages } from './homeTypes.ts'
import { lang } from './index.ts'

export type { HomeMessages }
export const th: HomeMessages = lang === 'en' ? homeEn : homeZhCN
