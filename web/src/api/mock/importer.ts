// A stand-in for importing Markdown (docs/api.md, "Import"), enough to build
// and test the page that uses it. A single Markdown file becomes one real
// page. A zip is not opened: the mock makes a few sample pages instead and
// reports one skipped entry.

import type { ImportReport, Page } from '../types.ts'
import { HttpError } from './store.ts'

export const DEFAULT_MAX_IMPORT = 100 * 1024 * 1024

// A zip starts with PK\x03\x04.
export const isZip = (b: Uint8Array) => b.length >= 4 && b[0] === 0x50 && b[1] === 0x4b && b[2] === 3 && b[3] === 4

const isMarkdownName = (name: string) => /\.(md|markdown)$/i.test(name)

function scalar(v: string): unknown {
  const s = v.trim()
  if (s === 'true') return true
  if (s === 'false') return false
  if (/^-?\d+(\.\d+)?$/.test(s)) return Number(s)
  if (s.startsWith('[') && s.endsWith(']')) return s.slice(1, -1).split(',').map((x) => String(scalar(x))).filter((x) => x !== '')
  if (/^(".*"|'.*')$/.test(s) && s.length >= 2) return s.slice(1, -1)
  return s
}

// The leading `---` header as keys and values (one `key: value` per line, which
// is all the mock reads), and the rest. A header that does not read this way
// stays in the body as text.
export function splitHeader(text: string): { meta: Record<string, unknown>; body: string } {
  const m = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/.exec(text)
  if (!m) return { meta: {}, body: text }
  const meta: Record<string, unknown> = {}
  for (const line of m[1].split(/\r?\n/)) {
    if (line.trim() === '' || line.trim().startsWith('#')) continue
    const kv = /^([A-Za-z0-9_-]+):(.*)$/.exec(line)
    if (!kv) return { meta: {}, body: text }
    meta[kv[1]] = scalar(kv[2])
  }
  return { meta, body: text.slice(m[0].length) }
}

export function pageFromMarkdown(text: string, name: string): { meta: Record<string, unknown>; body: string } {
  const { meta, body } = splitHeader(text.replace(/^\uFEFF/, ''))
  const given = { ...meta }
  delete given.id
  let rest = body
  let title = typeof given.title === 'string' && given.title.trim() !== '' ? given.title.trim() : ''
  if (!title) {
    const h = /^\s*#[ \t]+(.+?)[ \t]*(?:\r?\n|$)/.exec(rest)
    if (h) {
      title = h[1]
      rest = rest.slice(h[0].length)
    }
  }
  if (!title) title = name.replace(/\.(md|markdown)$/i, '')
  return { meta: { ...given, title }, body: rest.replace(/^(\r?\n)+/, '') }
}

export function runImport(create: (body: Record<string, unknown>) => Page, raw: Uint8Array, folder: string, name: string): ImportReport {
  if (isZip(raw)) {
    const at = (sub: string) => (folder ? `${folder}/${sub}` : sub)
    const samples: [string, string, string][] = [
      [folder, 'Imported note', 'The first sample page of an import.\n'],
      [folder, 'Another imported note', 'The second sample page.\n'],
      [at('details'), 'Imported note in a subfolder', 'A page below the target folder.\n'],
    ]
    for (const [f, title, body] of samples) create({ folder: f, meta: { title }, body })
    return { pages: samples.length, files: 1, skipped: [{ path: 'notes/draft.docx', reason: 'not a Markdown file and no page refers to it' }] }
  }
  if (!isMarkdownName(name)) throw new HttpError(400, 'invalid', '单个文件需要 name，而且要以 .md 或 .markdown 结尾')
  const text = new TextDecoder().decode(raw)
  const { meta, body } = pageFromMarkdown(text, name)
  create({ folder, meta, body })
  return { pages: 1, files: 0, skipped: [] }
}
