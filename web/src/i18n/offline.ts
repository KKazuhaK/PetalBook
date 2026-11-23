// The words of the "Offline" settings section, in the language of the page. Imported only by it, which loads on
// demand: the main bundle does not carry them.

import { offlineEn } from './offline/en.ts'
import { offlineZhCN } from './offline/zh-CN.ts'
import type { OfflineMessages } from './offlineTypes.ts'
import { lang } from './index.ts'

export type { OfflineMessages }
export const ol: OfflineMessages = lang === 'en' ? offlineEn : offlineZhCN
