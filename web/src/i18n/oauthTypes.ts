// The shape of the words of OAuth for MCP clients (see oauth/): the Chinese ones with their literals widened, so the English
// file must have exactly the same keys.

import type { oauthZhCN } from './oauth/zh-CN.ts'
import type { Widen } from './types.ts'

export type OAuthMessages = Widen<typeof oauthZhCN>
