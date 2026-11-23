// The words of mail, registration, email verification and password reset, in the language of the page.

import { mailEn } from './mail/en.ts'
import { mailZhCN } from './mail/zh-CN.ts'
import type { MailMessages } from './mailTypes.ts'
import { lang } from './index.ts'

export type { MailMessages }
export const tm: MailMessages = lang === 'en' ? mailEn : mailZhCN
