// Language tags of the daily text: BCP 47 tags in canonical form (zh-Hans, en, pt-BR), as the server writes them
// (docs/api.md, "The administrators' settings of the daily text").

// The canonical form of a well-formed tag whose language is a registered one (`ZH-cn` is `zh-CN`, a bare `zh` is
// `zh-Hans`), or null for anything else (`cn`, `english`, `zh_CN`, a private-use tag).
export function canonicalTag(input: string): string | null {
  const s = input.trim()
  if (s === '' || s.length > 35 || !/^[A-Za-z0-9-]+$/.test(s) || /^(x|und)(-|$)/i.test(s)) return null
  let tag: string
  try {
    tag = Intl.getCanonicalLocales(s)[0] ?? ''
  } catch {
    return null
  }
  const base = tag.split('-')[0]
  if (base === '' || !knownBase(base)) return null
  return tag === 'zh' ? 'zh-Hans' : tag
}

// Whether the language subtag is one the platform knows (the registry the server checks against).
function knownBase(base: string): boolean {
  try {
    return new Intl.DisplayNames(['en'], { type: 'language', fallback: 'none' }).of(base) !== undefined
  } catch {
    return false
  }
}

// The script a language tag is written in, by the likely subtags (`zh-TW` is Hant, `zh-CN` Hans).
function scriptOf(tag: string): string {
  try {
    return new Intl.Locale(tag).maximize().script ?? ''
  } catch {
    return ''
  }
}

// The one of `have` a reader's language is matched to, the BCP 47 way: the same language, the same script first
// (zh-CN and zh-SG find zh-Hans, zh-TW and zh-HK find zh-Hant, en-GB finds en), else another script of it. null
// when none is the reader's language.
export function matchTag(reader: string, have: readonly string[]): string | null {
  const r = canonicalTag(reader)
  if (r === null) return null
  const base = r.split('-')[0]
  const same = have.map((h) => canonicalTag(h)).filter((h): h is string => h !== null && h.split('-')[0] === base)
  if (same.length === 0) return null
  const script = scriptOf(r)
  return same.find((h) => h === r) ?? same.find((h) => scriptOf(h) === script) ?? same[0]
}
