// The words of server storage: the administrators' page for the policy, what a
// space shows of its copy on the server, the notices of a space that needs a
// remote, and the refusals that need a sentence of their own. Picked in the
// language of the page.

import { storageEn } from './storage/en.ts'
import { storageZhCN } from './storage/zh-CN.ts'
import type { StorageMessages } from './storageTypes.ts'
import { lang } from './index.ts'

export type { StorageMessages }
export const ts: StorageMessages = lang === 'en' ? (storageEn as StorageMessages) : storageZhCN
