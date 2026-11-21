// What the link box accepts. A bare "example.com" gets https://; schemes that
// can run script (javascript:, data:) are refused.
export function normalizeUrl(input: string): string | null {
  const v = input.trim()
  if (!v) return null
  if (/^(https?:|mailto:|tel:)/i.test(v)) return v
  if (v.startsWith('/') || v.startsWith('#') || v.startsWith('./') || v.startsWith('../')) return v
  if (/^[a-z][a-z0-9+.-]*:/i.test(v)) return null
  return `https://${v}`
}
