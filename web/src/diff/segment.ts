// Splitting text into the units a difference is computed over.

const hasSegmenter = typeof Intl !== 'undefined' && typeof (Intl as { Segmenter?: unknown }).Segmenter === 'function'
// Chinese word breaks need a dictionary, which `zh` selects; the same segmenter
// handles Latin text and mixed text.
const wordSegmenter = hasSegmenter ? new Intl.Segmenter('zh', { granularity: 'word' }) : null
const graphemeSegmenter = hasSegmenter ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : null

export const CJK = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u

// Without Intl.Segmenter: one unit per CJK character, whole runs of other
// letters and digits, one per space run, and any other character on its own.
const FALLBACK_UNIT = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]|(?:(?![\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}])[\p{L}\p{N}_])+|\s+|[\s\S]/gu

// Offsets (UTF-16) at which a new word unit starts, ascending, ending with
// text.length. Never empty for non-empty text.
export function wordCuts(text: string, forceFallback = false): number[] {
  const cuts: number[] = []
  if (wordSegmenter && !forceFallback) {
    for (const s of wordSegmenter.segment(text)) cuts.push(s.index + s.segment.length)
  } else {
    for (const m of text.matchAll(FALLBACK_UNIT)) cuts.push(m.index + m[0].length)
  }
  return cuts
}

export function graphemes(text: string): string[] {
  if (graphemeSegmenter) return Array.from(graphemeSegmenter.segment(text), (s) => s.segment)
  return Array.from(text)
}

export const graphemeCount = (text: string): number => (text === '' ? 0 : graphemes(text).length)
