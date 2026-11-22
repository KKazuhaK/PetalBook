// The words of the board, continuous reading and the tools of a view, in the language of the page. Imported only
// by those, which load on demand: the main bundle does not carry them.

import { boardsEn } from './boards/en.ts'
import { boardsZhCN } from './boards/zh-CN.ts'
import type { BoardsMessages } from './boardsTypes.ts'
import { lang } from './index.ts'

export type { BoardsMessages }
export const tb: BoardsMessages = lang === 'en' ? boardsEn : boardsZhCN
