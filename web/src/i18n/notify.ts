// The words of the notification centre, in the language of the page.

import { notifyEn } from './notify/en.ts'
import { notifyZhCN } from './notify/zh-CN.ts'
import type { NotifyMessages } from './notifyTypes.ts'
import { lang } from './index.ts'

export type { NotifyMessages }
export const tn: NotifyMessages = lang === 'en' ? notifyEn : notifyZhCN
