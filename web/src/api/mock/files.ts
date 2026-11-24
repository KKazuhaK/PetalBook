// The files of one space that are not pages: what docs/api.md calls "Files".
// Only what was uploaded exists here; the mock has no other files to serve.

import { sha256Hex } from './sha256.ts'
import type { UploadedAsset } from '../types.ts'

export const DEFAULT_MAX_UPLOAD = 10 * 1024 * 1024

// Characters file systems refuse (and control characters).
// eslint-disable-next-line no-control-regex
const REFUSED = /[\\/:*?"<>|\u0000-\u001f]/g
const MAX_STEM = 80
const MAX_EXT = 16

// `photo.JPG` + hash -> `photo-3f9a1c2b.jpg`: the given name with refused
// characters replaced, cut to a reasonable length, the extension in lower case,
// the first 8 hex digits of the content's SHA-256 before the extension.
export function storedName(given: string, hash8: string): string {
  const last = given.split(/[\\/]/).pop() ?? ''
  const cleaned = last.replace(REFUSED, '_').trim()
  const dot = cleaned.lastIndexOf('.')
  let stem = dot > 0 ? cleaned.slice(0, dot) : cleaned
  const ext = dot > 0 ? cleaned.slice(dot + 1).toLowerCase().slice(0, MAX_EXT) : ''
  // A name starting with a dot could not be read back (segments with a leading dot are 404).
  stem = stem.replace(/^[.\s]+/, '')
  stem = Array.from(stem).slice(0, MAX_STEM).join('')
  if (stem === '') stem = 'file'
  return `${stem}-${hash8}${ext ? `.${ext}` : ''}`
}

const INLINE_TYPES: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  avif: 'image/avif',
  svg: 'image/svg+xml',
  bmp: 'image/bmp',
  ico: 'image/x-icon',
  mp3: 'audio/mpeg',
  wav: 'audio/wav',
  ogg: 'audio/ogg',
  m4a: 'audio/mp4',
  mp4: 'video/mp4',
  webm: 'video/webm',
  mov: 'video/quicktime',
  pdf: 'application/pdf',
  txt: 'text/plain; charset=utf-8',
  csv: 'text/csv; charset=utf-8',
  log: 'text/plain; charset=utf-8',
}

interface Stored {
  bytes: Uint8Array
  mtime: number
}

export type UploadOutcome = { created: boolean; asset: UploadedAsset }

export function createFileStore(now: () => number) {
  const files = new Map<string, Stored>()

  const upload = (pageId: string, name: string, bytes: Uint8Array): UploadOutcome => {
    const stored = storedName(name, sha256Hex(bytes).slice(0, 8))
    const path = `/assets/${pageId}/${stored}`
    const created = !files.has(path)
    if (created) files.set(path, { bytes: bytes.slice(), mtime: now() })
    return { created, asset: { path, name: stored, size: bytes.length } }
  }

  // `segments` are decoded. Null is a 404: nothing there, a segment starting with
  // a dot, or a page.
  const read = (segments: string[]) => {
    if (!segments.length || segments.some((s) => s === '' || s.startsWith('.'))) return null
    const last = segments[segments.length - 1]
    // Under assets/ nothing is a page.
    if (/\.md$/i.test(last) && segments[0] !== 'assets') return null
    const f = files.get('/' + segments.join('/'))
    if (!f) return null
    const ext = last.includes('.') ? last.slice(last.lastIndexOf('.') + 1).toLowerCase() : ''
    const type = INLINE_TYPES[ext]
    return {
      bytes: f.bytes,
      headers: {
        'Content-Type': type ?? 'application/octet-stream',
        'Content-Disposition': type ? 'inline' : 'attachment',
        'X-Content-Type-Options': 'nosniff',
        'Content-Security-Policy': 'sandbox; default-src \'none\'; style-src \'unsafe-inline\'',
        'Cache-Control': 'private, no-cache',
        'Last-Modified': new Date(f.mtime).toUTCString(),
      },
    }
  }

  const count = () => files.size
  return { upload, read, count }
}

export type FileStore = ReturnType<typeof createFileStore>
