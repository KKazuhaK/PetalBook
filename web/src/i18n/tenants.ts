// The words of tenants and layered settings, in the language of the page: the administration's Tenants and
// Settings pages and the administrator kind on a user.

import { tenantsEn } from './tenants/en.ts'
import { tenantsZhCN } from './tenants/zh-CN.ts'
import type { TenantsMessages } from './tenantsTypes.ts'
import { lang } from './index.ts'

export type { TenantsMessages }
export const tt: TenantsMessages = lang === 'en' ? tenantsEn : tenantsZhCN
