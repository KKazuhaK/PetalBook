// The words of OAuth for MCP clients, in the language of the page: the consent page, "connected apps" in the account settings
// and the administration's switch and list. Imported only by those, which load on demand: the main bundle does not carry them.

import { oauthEn } from './oauth/en.ts'
import { oauthZhCN } from './oauth/zh-CN.ts'
import type { OAuthMessages } from './oauthTypes.ts'
import { lang } from './index.ts'

export type { OAuthMessages }
export const oa: OAuthMessages = lang === 'en' ? oauthEn : oauthZhCN
