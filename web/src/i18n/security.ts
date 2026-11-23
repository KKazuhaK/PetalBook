// The words of the administration's security page, the sign-in providers and the audit log, in the language
// of the page. Imported only by the administration, which loads on demand.

import { lang } from './index.ts'
import { securityEn } from './security/en.ts'
import { securityZhCN } from './security/zh-CN.ts'
import type { SecurityMessages } from './securityTypes.ts'

export type { SecurityMessages }
export const tsec: SecurityMessages = lang === 'en' ? securityEn : securityZhCN
