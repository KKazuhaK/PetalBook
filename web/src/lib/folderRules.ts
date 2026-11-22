// The server's rules for folder names (docs/api.md). Shared by the UI, so a
// bad name is refused with a clear message before any request, and by the
// in-memory mock, so it cannot drift from the UI.

import { t } from '../i18n/index.ts'

const MAX_SEGMENT_BYTES = 255

// null when `path` is acceptable. The root folder ("") always is. The path is
// taken as given: leading and trailing slashes are mistakes, not trimmed.
export function folderRuleMessage(path: string): string | null {
  if (path === '') return null
  const segments = path.split('/')
  if (segments.some((s) => s === '')) return t.folderRules.slashes
  for (const seg of segments) {
    if (seg.startsWith('.')) return t.folderRules.dotStart
    if (seg.endsWith('.') || seg.endsWith(' ')) return t.folderRules.dotEnd
    // That is how page files are named.
    if (/\.md$/i.test(seg)) return t.folderRules.mdEnd
    if (new TextEncoder().encode(seg).length > MAX_SEGMENT_BYTES) return t.folderRules.tooLong(MAX_SEGMENT_BYTES)
  }
  // Reserved for attachments.
  if (segments[0] === 'assets') return t.folderRules.reserved
  return null
}
