// The pure parts of the import page: which of the chosen files can be sent,
// and how the reports of several requests add up.

import type { ImportReport } from '@/api/types'

export const isMarkdownName = (name: string) => /\.(md|markdown)$/i.test(name)
export const isZipName = (name: string) => /\.zip$/i.test(name)

export type Picked<F extends { name: string }> =
  | { kind: 'none'; ignored: string[] }
  | { kind: 'markdown'; files: F[]; ignored: string[] }
  | { kind: 'zip'; file: F; ignored: string[] }
  | { kind: 'error'; error: 'zipAlone' | 'oneZip'; ignored: string[] }

// What to do with what was chosen: one or more Markdown files, or exactly one
// zip on its own. Anything else is named in `ignored` and left out, except that
// mixing a zip with other importable files is refused rather than guessed at.
export function classifyFiles<F extends { name: string }>(files: readonly F[]): Picked<F> {
  const zips = files.filter((f) => isZipName(f.name))
  const markdown = files.filter((f) => isMarkdownName(f.name))
  const ignored = files.filter((f) => !isZipName(f.name) && !isMarkdownName(f.name)).map((f) => f.name)
  if (zips.length > 1) return { kind: 'error', error: 'oneZip', ignored }
  if (zips.length === 1 && markdown.length > 0) return { kind: 'error', error: 'zipAlone', ignored }
  if (zips.length === 1) return { kind: 'zip', file: zips[0], ignored }
  if (markdown.length > 0) return { kind: 'markdown', files: markdown, ignored }
  return { kind: 'none', ignored }
}

// The most entries of `skipped` a report carries (docs/api.md); a total of
// several reports is cut to the same length and counts the rest in `skippedMore`.
export const SKIPPED_SHOWN = 200

export function addReports(a: ImportReport, b: ImportReport): ImportReport {
  const all = [...a.skipped, ...b.skipped]
  const kept = all.slice(0, SKIPPED_SHOWN)
  const more = (a.skippedMore ?? 0) + (b.skippedMore ?? 0) + (all.length - kept.length)
  return {
    pages: a.pages + b.pages,
    files: a.files + b.files,
    skipped: kept,
    ...(more > 0 ? { skippedMore: more } : {}),
  }
}

export const emptyReport = (): ImportReport => ({ pages: 0, files: 0, skipped: [] })

// The same file chosen twice (or dropped after being picked) is one entry.
// "The same" is name, size and time of last change together: two different
// files that share a name (a README from each of two folders) both stay.
export function mergeFiles<F extends { name: string; size: number; lastModified?: number }>(
  current: readonly F[],
  added: readonly F[],
): F[] {
  const out = [...current]
  for (const f of added)
    if (!out.some((o) => o.name === f.name && o.size === f.size && o.lastModified === f.lastModified)) out.push(f)
  return out
}
