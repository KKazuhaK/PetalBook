// The words of an organisation's email domains, in the language of the page. Imported only by the administration,
// which loads on demand.

import { lang } from './index.ts'
import { domainsEn } from './domains/en.ts'
import { domainsZhCN } from './domains/zh-CN.ts'
import type { DomainsMessages } from './domainsTypes.ts'

export type { DomainsMessages }
export const tdom: DomainsMessages = lang === 'en' ? domainsEn : domainsZhCN
