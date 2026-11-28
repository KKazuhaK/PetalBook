// The features of a tenant (docs/api.md, "Features") in words, in the language of the page: what an error says is
// turned off (403 feature_off names the key) and what a page says of a part it does not show. The keys are the
// server's; a key this list does not have is shown as it is.

import type { FeatureKey } from '@/api/types.ts'
import { lang } from './index.ts'

const zhCN: Record<FeatureKey, string> = {
  'features.tokens': '访问令牌',
  'features.mcp': 'MCP',
  'features.offline': '离线副本',
  'features.tenancy': '多个组织',
  'features.passkeys': '通行密钥',
  'features.adminView': '管理员查看空间内容',
  'features.auditDownload': '审计日志下载',
  'features.tenantAudit': '组织的审计日志',
  'features.ownSso': '组织自己的单点登录',
  'features.domains': '认领邮箱域名',
  'features.managedAccounts': '托管账号',
  'features.idpAdmins': '由身份提供方指定管理员',
  'features.ownCaptcha': '组织自己的验证码',
  'features.ownConnections': '组织自己的代码托管连接',
  'features.ownDaily': '组织自己的每日一句',
  'features.githubConnect': '连接 GitHub',
  'mail.ownSender': '组织自己的发信服务器',
  'accounts.multipleEmails': '一个账号多个邮箱',
  'features.orgCreation': '自己创建组织',
}

const en: Record<FeatureKey, string> = {
  'features.tokens': 'API tokens',
  'features.mcp': 'MCP',
  'features.offline': 'offline copies',
  'features.tenancy': 'several organisations',
  'features.passkeys': 'passkeys',
  'features.adminView': 'administrators viewing space contents',
  'features.auditDownload': 'audit log download',
  'features.tenantAudit': 'the organisation’s audit log',
  'features.ownSso': 'the organisation’s own single sign-on',
  'features.domains': 'claiming email domains',
  'features.managedAccounts': 'managed accounts',
  'features.idpAdmins': 'administrators named by the identity provider',
  'features.ownCaptcha': 'the organisation’s own CAPTCHA',
  'features.ownConnections': 'the organisation’s own repository connections',
  'features.ownDaily': 'the organisation’s own daily text',
  'features.githubConnect': 'connecting GitHub',
  'mail.ownSender': 'the organisation’s own mail server',
  'accounts.multipleEmails': 'more than one email address per account',
  'features.orgCreation': 'organisations people create themselves',
}

const names: Record<string, string> = lang === 'en' ? en : zhCN

// The words for a feature key, or the key itself when there are none; undefined for no key.
export function featureName(key: string | undefined): string | undefined {
  if (key === undefined) return undefined
  return Object.hasOwn(names, key) ? names[key] : key
}
