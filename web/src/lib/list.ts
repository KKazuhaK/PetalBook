import { lang } from '@/i18n'

// Names joined the way the interface language writes a list ("a, b and c"; "甲、乙和丙"). `locale` is the UI language.
export function joinList(items: readonly string[], locale: string = lang): string {
  return new Intl.ListFormat(locale, { style: 'long', type: 'conjunction' }).format(items)
}
