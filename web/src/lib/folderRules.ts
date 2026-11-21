// The server's rules for folder names (docs/api.md). Shared by the UI, so a
// bad name is refused with a clear message before any request, and by the
// in-memory mock, so it cannot drift from the UI.

const MAX_SEGMENT_BYTES = 255

// null when `path` is acceptable. The root folder ("") always is. The path is
// taken as given: leading and trailing slashes are mistakes, not trimmed.
export function folderRuleMessage(path: string): string | null {
  if (path === '') return null
  const segments = path.split('/')
  if (segments.some((s) => s === '')) return '路径不能以 / 开头或结尾，也不能有连续的 /'
  for (const seg of segments) {
    if (seg.startsWith('.')) return '文件夹名不能以 . 开头'
    if (seg.endsWith('.') || seg.endsWith(' ')) return '文件夹名不能以 . 或空格结尾'
    if (new TextEncoder().encode(seg).length > MAX_SEGMENT_BYTES) return `文件夹名太长（最多 ${MAX_SEGMENT_BYTES} 字节）`
  }
  // Reserved for attachments.
  if (segments[0] === 'assets') return '“assets” 是附件的保留名称，不能作为顶层文件夹名'
  return null
}
