// The words of API tokens, in the language of the page: the "API tokens" section of the account settings and the
// administration's machine access. Imported only by those, which load on demand: the main bundle does not carry them.

import { tokensEn } from './tokens/en.ts'
import { tokensZhCN } from './tokens/zh-CN.ts'
import type { TokensMessages } from './tokensTypes.ts'
import { lang } from './index.ts'

export type { TokensMessages }
export const tk: TokensMessages = lang === 'en' ? tokensEn : tokensZhCN
