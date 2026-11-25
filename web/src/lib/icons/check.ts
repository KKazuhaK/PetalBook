// What may be an icon, as the server checks it (docs/api.md, "Icons, colours and the presets of the sources"): the name
// of a Lucide icon the app ships, or one emoji (one grapheme cluster) of at most 64 bytes with no control character,
// space or markup. Plain functions with no imports, so the in-memory server uses them too.

// A Lucide name is lower case with dashes; anything else (an emoji) is text.
export const isIconName = (v: string) => /^[a-z][a-z0-9-]*$/.test(v)

// The name of a Lucide component (`BookOpen`) as the app and the server write it (`book-open`).
export const kebab = (s: string) =>
  s
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .replace(/([A-Za-z])(\d)/g, '$1-$2')
    .toLowerCase()

// The longest icon, in bytes of UTF-8.
export const MAX_ICON_BYTES = 64

const segmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' })

// The first grapheme cluster of `s` (what a person sees as one character: an emoji with its skin tone, a flag).
export const firstGrapheme = (s: string): string => {
  for (const { segment } of segmenter.segment(s)) return segment
  return ''
}

// Whether text can be an icon: one grapheme cluster of at most 64 bytes, with no control character, space or markup.
export const isTextIcon = (v: string): boolean =>
  v !== '' &&
  !isIconName(v) &&
  firstGrapheme(v) === v &&
  new TextEncoder().encode(v).length <= MAX_ICON_BYTES &&
  !/[\p{Cc}\p{Cf}\s<>&"'`]/u.test(v.replace(/[‍\u{e0020}-\u{e007f}]/gu, ''))
