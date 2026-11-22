// Does pasted plain text look like Markdown source?
//
// Wrong in one direction costs little (text pasted as text, which the person can
// still format) and in the other it rewrites what they pasted, so this errs
// toward "no". A text counts as Markdown when it has one strong signal, or two
// different weak ones:
//
//   strong  an ATX heading (`# 标题`), a fenced code block that is closed, a table
//           (a header row over a `| --- |` row), a task item (`- [ ] x`), two
//           or more list items, two or more quote lines, a link or image
//           `[text](address)`, bold `**x**`, strike `~~x~~`
//   weak    italic `*x*` / `_x_`, bold `__x__` (Python's `__init__`), inline code `x`, one list item, one quote
//           line, a rule (`---`)
//
// Not counted at all: a lone `- item` or `1. item` (chat text is full of them),
// a bare URL, `#tag` (no space), setext headings, HTML. Text that looks like
// program code (several lines ending in `;` `{` `}` or starting `import`,
// `def`, `const`, `$ `...) is never Markdown, unless it contains a closed fence:
// a `# comment` in a script is not a heading.
//
// Lines inside a fenced block are not looked at for anything else.

export interface Detection {
  markdown: boolean
  // What was found, for tests and for anyone wondering why a paste was converted.
  signals: string[]
}

const FENCE = /^ {0,3}(`{3,}|~{3,})/
const HEADING = /^ {0,3}#{1,6}[ \t]+\S/
const LIST_ITEM = /^ {0,}(?:[-*+]|\d{1,9}[.)])[ \t]+\S/
const TASK = /^ {0,}[-*+][ \t]+\[[ xX]\][ \t]+\S/
const QUOTE = /^ {0,3}>[ \t]?\S/
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/
const TABLE_DELIMITER = /^[ \t]*\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/
const LINK = /!?\[[^\]\n]+\]\([^)\s]+(?:[ \t]+"[^"\n]*")?\)/
const BOLD = /(?<![*\\])\*\*(?=[^\s*])[^*\n]*[^\s*\\]\*\*(?!\*)/
// Double underscores are a Python habit as much as bold (`__init__.py`).
const BOLD_UNDERSCORE = /(?<![\p{L}\p{N}_\\])__(?=[^\s_])[^_\n]*[^\s_\\]__(?![\p{L}\p{N}_])/u
const STRIKE = /(?<![~\\])~~(?=[^\s~])[^~\n]*[^\s~\\]~~(?!~)/
const ITALIC = /(?<![*\\])\*(?=[^\s*])[^*\n]*[^\s*\\]\*(?![*])|(?<![\p{L}\p{N}_\\])_(?=[^\s_])[^_\n]*[^\s_\\]_(?![\p{L}\p{N}_])/u
const CODE_SPAN = /(?<!`)`[^`\n]*[^`\s\n][^`\n]*`(?!`)/

// Lines that read as program code rather than prose.
const CODE_LINE = /[;{}]\s*$|^\s*(?:import\s|from\s+\S+\s+import\s|def\s+\w+\s*\(|function\s+\w+\s*\(|(?:const|let|var)\s+\w+\s*=|#include\b|package\s+\w+|\$\s)/

export function detectMarkdown(text: string): Detection {
  const lines = text.replace(/\r\n?/g, '\n').split('\n')
  const strong = new Set<string>()
  const weak = new Set<string>()
  let listItems = 0
  let quoteLines = 0
  let codeLines = 0
  let nonEmpty = 0
  let closedFence = false

  // First pass: fences, so that what is inside never counts.
  const inside = new Array<boolean>(lines.length).fill(false)
  for (let i = 0; i < lines.length; i++) {
    const open = FENCE.exec(lines[i])
    if (!open) continue
    const marker = open[1]
    // The info string of a backtick fence cannot contain a backtick.
    if (marker[0] === '`' && lines[i].slice(open[0].length).includes('`')) continue
    let end = -1
    for (let j = i + 1; j < lines.length; j++) {
      const m = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(lines[j])
      if (m && m[1][0] === marker[0] && m[1].length >= marker.length) {
        end = j
        break
      }
    }
    if (end < 0) {
      // Never closed: a weak hint only, and the rest is not inspected as Markdown.
      weak.add('fence')
      for (let j = i; j < lines.length; j++) inside[j] = true
      break
    }
    closedFence = true
    strong.add('fence')
    for (let j = i; j <= end; j++) inside[j] = true
    i = end
  }

  for (let i = 0; i < lines.length; i++) {
    if (inside[i]) continue
    const line = lines[i]
    if (line.trim() === '') continue
    nonEmpty++
    if (CODE_LINE.test(line)) codeLines++
    // `---` and `- - -` are rules, not list items.
    if (RULE.test(line)) {
      weak.add('rule')
      continue
    }
    if (HEADING.test(line)) strong.add('heading')
    if (TASK.test(line)) strong.add('task')
    if (LIST_ITEM.test(line)) listItems++
    if (QUOTE.test(line)) quoteLines++
    if (LINK.test(line)) strong.add('link')
    if (BOLD.test(line)) strong.add('bold')
    if (BOLD_UNDERSCORE.test(line)) weak.add('bold underscore')
    if (STRIKE.test(line)) strong.add('strike')
    if (ITALIC.test(line)) weak.add('italic')
    if (CODE_SPAN.test(line)) weak.add('code')
    if (line.includes('|') && i + 1 < lines.length && !inside[i + 1]) {
      const next = lines[i + 1]
      if (next.includes('|') && next.includes('-') && TABLE_DELIMITER.test(next)) strong.add('table')
    }
  }
  if (listItems >= 2) strong.add('list')
  else if (listItems === 1) weak.add('list item')
  if (quoteLines >= 2) strong.add('quote')
  else if (quoteLines === 1) weak.add('quote line')

  const signals = [...strong, ...weak]
  // Code first: a script with comments is not a document, however many `#` it has.
  const looksLikeCode = codeLines >= 2 || (nonEmpty > 0 && codeLines * 3 >= nonEmpty && codeLines > 0 && nonEmpty > 2)
  if (looksLikeCode && !closedFence) return { markdown: false, signals }
  return { markdown: strong.size > 0 || weak.size >= 2, signals }
}

export const looksLikeMarkdown = (text: string): boolean => detectMarkdown(text).markdown
