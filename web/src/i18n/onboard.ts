// The words of the tour and the repository guide, in the language of the page.

import { onboardEn } from './onboard/en.ts'
import { onboardZhCN } from './onboard/zh-CN.ts'
import type { OnboardMessages } from './onboardTypes.ts'
import { lang } from './index.ts'

export type { OnboardMessages }
export const to: OnboardMessages = lang === 'en' ? onboardEn : onboardZhCN
