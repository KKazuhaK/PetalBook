// The words of the sign-in pages, the passkeys and the captcha, in the language of the page. The pages
// that use them load on demand, so the main bundle does not carry them.

import { lang } from './index.ts'
import { signinEn } from './signin/en.ts'
import { signinZhCN } from './signin/zh-CN.ts'
import type { SigninMessages } from './signinTypes.ts'

export type { SigninMessages }
export const ts: SigninMessages = lang === 'en' ? signinEn : signinZhCN
