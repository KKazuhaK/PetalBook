// How a page names another page in its Markdown (docs/design/milestones/phase-2.md, 2.4): a
// standard relative link whose title is the target's id,
// `[暮雪](../Tang/暮雪.md "01JD3K8Q7Z")`. The rules here are the server's
// (internal/links) and are held to the same cases (markdown/links.test.ts).

const ID = /^[0-9A-HJKMNP-TV-Z]{10}$/
export const isPageId = (title: string | null | undefined): title is string => !!title && ID.test(title)

const byte = (c: number) => '%' + (c < 16 ? '0' : '') + c.toString(16).toUpperCase()

// A path as the destination of a link: spaces, brackets and the like are
// percent-encoded (the form that needs no angle brackets), % # ? and control
// characters always; other text, Chinese included, is written as it is.
export function encodeDest(path: string): string {
  let out = ''
  for (const ch of path) {
    const c = ch.codePointAt(0)!
    if ('%#?\\<> ()[]"`'.includes(ch) || c < 0x20 || c === 0x7f) out += byte(c)
    else out += ch
  }
  return out
}

// The path of the file `to`, written as a link in a page that sits in `fromFolder`
// (both from the root of the space): relative, with `..` where it has to go up.
export function relativePath(fromFolder: string, to: string): string {
  const a = fromFolder.split('/').filter(Boolean)
  const b = to.split('/')
  let i = 0
  while (i < a.length && i < b.length - 1 && a[i] === b[i]) i++
  return [...a.slice(i).map(() => '..'), ...b.slice(i)].join('/')
}

// The destination to write for a link from a page in `fromFolder` to the page at `to`.
export const linkDest = (fromFolder: string, to: string) => encodeDest(relativePath(fromFolder, to))

// The page file a link names, from a page in `folder`: null for what is not a page
// link (the web, an image, an anchor on this page, a path that leaves the space).
export function resolveHref(folder: string, href: string): { path: string; anchor: string } | null {
  let dest = href
  let anchor = ''
  const h = dest.indexOf('#')
  if (h >= 0) [dest, anchor] = [dest.slice(0, h), dest.slice(h + 1)]
  const q = dest.indexOf('?')
  if (q >= 0) dest = dest.slice(0, q)
  if (!dest || /^[A-Za-z][A-Za-z0-9+.-]*:/.test(dest) || dest.startsWith('//')) return null
  try {
    dest = decodeURIComponent(dest)
  } catch {
    // kept as written
  }
  if (!/\.md$/i.test(dest)) return null
  const parts: string[] = dest.startsWith('/') ? [] : folder.split('/').filter(Boolean)
  for (const seg of dest.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length === 0) return null
      parts.pop()
    } else parts.push(seg)
  }
  return { path: parts.join('/'), anchor }
}
