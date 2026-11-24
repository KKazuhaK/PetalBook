// The words of GitHub: making a repository for a new space, the account's
// connection, a space's GitHub credential and the administrators' App. One module
// for all four, picked in the language of the page.

import { githubEn } from './github/en.ts'
import { githubZhCN } from './github/zh-CN.ts'
import type { GitHubMessages } from './githubTypes.ts'
import { lang } from './index.ts'

export type { GitHubMessages }
export const tg: GitHubMessages = lang === 'en' ? (githubEn as GitHubMessages) : githubZhCN
