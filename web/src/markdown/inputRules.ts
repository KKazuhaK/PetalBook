// Typing shortcuts for emphasis, in place of the ones Tiptap ships.
//
// Tiptap's rules only fire when the opening `**` follows a space or the start
// of the line. Chinese text has no spaces, so `这是**粗体**` never converted.
// CommonMark itself allows `*` and `~` emphasis right after any character, and
// so do these rules. `_` keeps the word-boundary rule, as Markdown does: it must
// not follow a letter, digit or underscore (so `snake_case_name` stays plain, and
// so does `中文_斜体_`, as in CommonMark).
//
// Shared shape of the star/tilde/backtick patterns:
//   - the opening marker is not preceded by the same character, so `**` is never
//     read as two `*` and ``` ``` ``` is never read as a code span;
//   - the text inside starts and ends with something other than a space, the
//     flanking rule that keeps `2 * 3 * 4` and `a ** b ** c` as typed.
// The patterns end in `$`: input rules look at the text before the cursor.
//
// What does not exist here: `***x***` (bold and italic at once) is left as
// typed, and so is `_x_` inside a word. Neither loses anything; the text
// round-trips as literal characters.

import { markInputRule } from '@tiptap/core'
import Bold from '@tiptap/extension-bold'
import Italic from '@tiptap/extension-italic'
import Strike from '@tiptap/extension-strike'
import Code from '@tiptap/extension-code'

// `inner` for a delimiter character `c`: no leading/trailing space, no `c` inside.
const inner = (c: string) => `[^\\s${c}](?:[^${c}]*[^\\s${c}])?`

export const boldStarRule = new RegExp(`(?<!\\*)\\*\\*(${inner('*')})\\*\\*$`)
export const italicStarRule = new RegExp(`(?<!\\*)\\*(${inner('*')})\\*$`)
export const strikeRule = new RegExp(`(?<!~)~~(${inner('~')})~~$`)
// Spaces are allowed inside a code span, but not only spaces.
export const codeRule = /(?<!`)`([^`]*[^`\s][^`]*)`$/

const WORD = '(?<![\\p{L}\\p{N}_])'
export const boldUnderscoreRule = new RegExp(`${WORD}__(${inner('_')})__$`, 'u')
export const italicUnderscoreRule = new RegExp(`${WORD}_(${inner('_')})_$`, 'u')

// Pasted text goes through the Markdown paste in components/editor/paste.ts, and
// pasted HTML keeps any asterisks it carries as literal text: no paste rules.
export const CjkBold = Bold.extend({
  // Marks are ordered in the schema by priority and then by position. These
  // three came from the starter kit, ahead of the link mark; keep that order so
  // documents list marks the way they always did.
  priority: 101,
  addInputRules() {
    return [
      markInputRule({ find: boldStarRule, type: this.type }),
      markInputRule({ find: boldUnderscoreRule, type: this.type }),
    ]
  },
  addPasteRules: () => [],
})

export const CjkItalic = Italic.extend({
  priority: 101,
  addInputRules() {
    return [
      markInputRule({ find: italicStarRule, type: this.type }),
      markInputRule({ find: italicUnderscoreRule, type: this.type }),
    ]
  },
  addPasteRules: () => [],
})

export const CjkStrike = Strike.extend({
  addInputRules() {
    return [markInputRule({ find: strikeRule, type: this.type })]
  },
  addPasteRules: () => [],
})

// By default inline code excludes every other mark, which would split a
// link or bold run that contains code. Markdown allows the combination.
export const CjkCode = Code.extend({
  excludes: '',
  addInputRules() {
    return [markInputRule({ find: codeRule, type: this.type })]
  },
  addPasteRules: () => [],
})
