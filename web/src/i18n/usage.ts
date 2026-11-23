// The words of quotas and usage, in the language of the page.

import { usageEn } from './usage/en.ts'
import { usageZhCN } from './usage/zh-CN.ts'
import type { UsageMessages } from './usageTypes.ts'
import { lang } from './index.ts'

export type { UsageMessages }
export const tu: UsageMessages = lang === 'en' ? usageEn : usageZhCN
