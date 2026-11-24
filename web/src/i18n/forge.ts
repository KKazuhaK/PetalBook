// The words of the hosts that need no App (GitLab, Gitea and the like): the account's connection and the
// administrators' cards, picked in the language of the page.

import { forgeEn } from './forge/en.ts'
import { forgeZhCN } from './forge/zh-CN.ts'
import type { ForgeMessages } from './forgeTypes.ts'
import { lang } from './index.ts'

export type { ForgeMessages }
export const tf: ForgeMessages = lang === 'en' ? (forgeEn as ForgeMessages) : forgeZhCN
