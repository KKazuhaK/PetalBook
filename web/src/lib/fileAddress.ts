// Where an address written in a page points to (docs/api.md, "Files").
//
//   - with a scheme (`https:`, `data:`, `mailto:` ...), or starting with `//` or
//     `#`: not a file of the space, left alone;
//   - starting with `/`: a path from the root of the space;
//   - anything else: relative to the folder of the page.
//
// `.` and `..` are resolved, and `..` never goes above the root. The Markdown
// of the page keeps the address as written (`/assets/<id>/x.jpg`, which stays
// right when the page moves); only what the browser is asked to load is mapped.

import { fileUrl } from '@/api/client'

export type Resolved = { kind: 'external'; url: string } | { kind: 'file'; path: string; suffix: string }

const SCHEME = /^[a-z][a-z0-9+.-]*:/i

// A segment as it is written in Markdown may be percent-encoded (`my%20file.png`);
// the path of the file is the decoded one. A segment that is not valid
// percent-encoding (`100%.png`) is taken literally.
function decodeSegment(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

export function resolveAddress(address: string, folder: string): Resolved {
  if (address === '' || SCHEME.test(address) || address.startsWith('//') || address.startsWith('#'))
    return { kind: 'external', url: address }
  // A query or fragment is not part of the path (`manual.pdf#page=3`).
  const cut = address.search(/[?#]/)
  const pathPart = cut < 0 ? address : address.slice(0, cut)
  const suffix = cut < 0 ? '' : address.slice(cut)
  const segments = pathPart.startsWith('/') ? [] : folder.split('/').filter(Boolean)
  for (const raw of pathPart.split('/')) {
    // Decoded first: `%2e%2e` is `..` and `a%2Fb` is two segments, and both must
    // be resolved here, so that what is asked for never leaves the space's files.
    for (const seg of decodeSegment(raw).split(/[/\\]/)) {
      if (seg === '' || seg === '.') continue
      if (seg === '..') segments.pop()
      else segments.push(seg)
    }
  }
  return { kind: 'file', path: '/' + segments.join('/'), suffix }
}

// What the browser should load or open for an address in a page of `folder`.
export function addressToUrl(space: string, folder: string, address: string): string {
  const r = resolveAddress(address, folder)
  return r.kind === 'external' ? r.url : fileUrl(space, r.path) + r.suffix
}
