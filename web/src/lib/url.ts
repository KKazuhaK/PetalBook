// What the link box accepts. A bare "example.com" gets https://; schemes that
// can run script (javascript:, data:) are refused.
export function normalizeUrl(input: string): string | null {
  const v = input.trim()
  if (!v) return null
  if (/^(https?:|mailto:|tel:)/i.test(v)) return v
  if (v.startsWith('/') || v.startsWith('#') || v.startsWith('./') || v.startsWith('../')) return v
  if (/^[a-z][a-z0-9+.-]*:/i.test(v)) return null
  // A bare address of a Markdown file (`Other.md`, `子目录/页.md#节`) is a page of the space, written relative to
  // this one; the Moldovan domain is reached with `https://` typed out.
  if (!v.startsWith('//') && /^[^#?]*\.md(#.*)?$/i.test(v)) return v
  return `https://${v}`
}
