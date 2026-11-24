// A page as the text of its file: the header between two `---` lines, then the
// body. The source view shows and edits this text; this module takes it apart
// into what the rest of the editor works with (header fields, body) and puts
// it together again.
//
// The rules for where the header ends are the server's (internal/page/doc.go):
// the first line is `---`, the header runs to the next line that is `---` or
// `...` (trailing spaces allowed on both), and the body is everything after it.

import { isMap, isScalar, LineCounter, parseDocument, stringify } from 'yaml'
import type { PageMeta } from '@/api/types'
import { fieldsOf, othersOf, type Fields } from '@/editor/saveController'

export interface HeaderProblem {
  // 1-based line in the whole file text.
  line: number
  message: string
  code: 'no_header' | 'unterminated' | 'bad_yaml' | 'not_mapping' | 'id'
}

export const composeFile = (header: string, body: string) =>
  `---\n${header}${header === '' || header.endsWith('\n') ? '' : '\n'}---\n${body}`

const isFence = (line: string, closing: boolean) => {
  const t = line.replace(/[ \t]+$/, '')
  return t === '---' || (closing && t === '...')
}

export type SplitFile =
  { ok: true; header: string; body: string; bodyLine: number } | { ok: false; problem: HeaderProblem }

// `bodyLine` is the 1-based line of the file on which the body starts.
export function splitFile(text: string): SplitFile {
  const s = text.replace(/\r\n/g, '\n')
  const firstEnd = s.indexOf('\n')
  const first = firstEnd < 0 ? s : s.slice(0, firstEnd)
  if (!isFence(first, false))
    return { ok: false, problem: { line: 1, code: 'no_header', message: 'the first line must be ---' } }
  let pos = firstEnd < 0 ? s.length : firstEnd + 1
  const start = pos
  let line = 2
  while (pos < s.length) {
    const end = s.indexOf('\n', pos)
    const l = end < 0 ? s.slice(pos) : s.slice(pos, end)
    if (isFence(l, true))
      return { ok: true, header: s.slice(start, pos), body: end < 0 ? '' : s.slice(end + 1), bodyLine: line + 1 }
    if (end < 0) break
    pos = end + 1
    line++
  }
  return {
    ok: false,
    problem: { line: 1, code: 'unterminated', message: 'the header is opened with --- but never closed' },
  }
}

export type ParsedHeader = { ok: true; fields: Fields; meta: PageMeta } | { ok: false; problem: HeaderProblem }

// Reads the header text. `id` is the page's: the header must carry exactly it,
// since the id is what the page is. `firstLine` is the file line of the
// header's first line (2 in a whole file), so problems point into the file.
export function parseHeader(header: string, id: string, firstLine = 2): ParsedHeader {
  const counter = new LineCounter()
  const doc = parseDocument(header, { lineCounter: counter, prettyErrors: false, uniqueKeys: true })
  const at = (offset: number | undefined) => firstLine + (offset === undefined ? 0 : counter.linePos(offset).line - 1)
  if (doc.errors.length) {
    const e = doc.errors[0]
    return { ok: false, problem: { line: at(e.pos?.[0]), code: 'bad_yaml', message: e.message.split('\n')[0] } }
  }
  if (doc.contents !== null && !isMap(doc.contents))
    return {
      ok: false,
      problem: { line: firstLine, code: 'not_mapping', message: 'the header must be a list of `key: value` lines' },
    }
  // The id is compared as written: ten digits would otherwise read as a number.
  const idNode = isMap(doc.contents) ? doc.contents.get('id', true) : undefined
  const idText = isScalar(idNode)
    ? typeof idNode.source === 'string'
      ? idNode.source
      : String(idNode.value)
    : undefined
  if (idText !== id) {
    const range = (idNode as { range?: [number, number, number] } | undefined)?.range
    return {
      ok: false,
      problem: {
        line: at(range?.[0]),
        code: 'id',
        message:
          idText === undefined
            ? `the header must keep \`id: ${id}\``
            : `the id cannot be changed; it must stay \`${id}\``,
      },
    }
  }
  let js: unknown
  try {
    js = doc.toJS({ maxAliasCount: 50 })
  } catch (e) {
    return {
      ok: false,
      problem: {
        line: firstLine,
        code: 'bad_yaml',
        message: e instanceof Error ? e.message : 'the header cannot be read',
      },
    }
  }
  const meta: PageMeta = { ...(js as PageMeta), id }
  for (const [k, v] of Object.entries(meta)) {
    if (v === undefined || typeof v === 'bigint' || typeof v === 'symbol' || typeof v === 'function') delete meta[k]
    // JSON has no way to say these; the server would refuse or mangle them.
    else if (typeof v === 'number' && !Number.isFinite(v)) meta[k] = String(v)
  }
  return { ok: true, meta, fields: { ...fieldsOf(meta), others: othersOf(meta) } }
}

// A header for showing, when the text of the file's own cannot be used (the
// fields were edited outside the source view). The server writes its own form
// when it saves; this only has to read the same.
export function headerText(meta: PageMeta): string {
  const entries = Object.entries(meta).filter(([, v]) => v !== undefined)
  if (entries.length === 0) return ''
  return stringify(Object.fromEntries(entries), { lineWidth: 0, flowCollectionPadding: false })
}

// The smallest replacement that turns `from` into `to`: what differs between a
// common start and a common end. Used to update the source view without
// moving what is on screen.
export function minimalChange(from: string, to: string): { from: number; to: number; insert: string } | null {
  if (from === to) return null
  let a = 0
  const max = Math.min(from.length, to.length)
  while (a < max && from.charCodeAt(a) === to.charCodeAt(a)) a++
  let b = 0
  while (b < max - a && from.charCodeAt(from.length - 1 - b) === to.charCodeAt(to.length - 1 - b)) b++
  // Never cut a surrogate pair in half.
  if (a > 0 && from.charCodeAt(a - 1) >= 0xd800 && from.charCodeAt(a - 1) <= 0xdbff) a--
  if (b > 0 && from.charCodeAt(from.length - b) >= 0xdc00 && from.charCodeAt(from.length - b) <= 0xdfff) b--
  return { from: a, to: from.length - b, insert: to.slice(a, to.length - b) }
}
