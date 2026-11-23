import type { ReasonCode } from '@/api/types'
import type { AdminMessages } from '@/i18n/admin'

type Words = AdminMessages['daily']

// The words of a refusal of the daily-text endpoints, from the stable code the server gives (and the thing it is
// about), in the language of `d`. null for a code that is not known, so the caller shows the server's own message.
export function dailyCodeMessage(r: Partial<ReasonCode> | undefined, d: Words): string | null {
  if (!r?.code) return null
  const field = (k: string | undefined): string => {
    switch (k) {
      case 'text':
        return d.fieldText
      case 'source':
        return d.fieldSource
      case 'author':
        return d.fieldAuthor
      case 'link':
        return d.fieldLink
      default:
        return k ?? ''
    }
  }
  switch (r.code) {
    case 'host_not_allowed':
      return d.tryHostNotAllowed(r.host ?? '')
    case 'header_reenter':
      return d.headerNeedsValue(r.header ?? '')
    case 'header_missing':
      return d.headerValueMissing(r.header ?? '')
    case 'not_json':
      return d.tryNotJson
    case 'too_large':
      return d.tryTooLarge
    case 'path_error':
      return d.pathError(field(r.field))
    case 'path_reveals_secret':
      return d.pathRevealsSecret(field(r.field))
    case 'link_uses_header':
      return d.linkUsesHeader
    default:
      return null
  }
}
