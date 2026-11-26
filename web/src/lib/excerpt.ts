// The excerpt of a page, built the way the server builds it (internal/page/text.go), for the offline reader and
// the fixture server. Both are tested with the same cases (internal/page/testdata/excerpts.json).

// A run of three or more backquotes or tildes: the character and the length.
function fenceRun(t: string): [string, number] {
  if (!t || (t[0] !== '`' && t[0] !== '~')) return ['', 0]
  let n = 0
  while (n < t.length && t[n] === t[0]) n++
  return [t[0], n]
}

// The text of a page for a listing: up to 400 characters, one line per non-blank line of the body. A task item keeps
// its state at the start of the line, as "[x] " or "[ ] ".
export function excerptOf(markdown: string): string {
  const lines: string[] = []
  let fence = ''
  for (const raw of markdown.split('\n')) {
    const t = raw.trim()
    const [c, n] = fenceRun(t)
    if (n >= 3) {
      if (!fence) {
        fence = c
        continue
      }
      if (c === fence && t.replaceAll(c, '') === '') {
        fence = ''
        continue
      }
    }
    // Inside a code fence the text is kept as it is.
    if (fence) {
      lines.push(t.replace(/\s+/g, ' '))
      continue
    }
    const l = t
      .replace(/^(\s*>)+ ?/, '')
      .replace(/^#{1,6}(\s+|$)/, '')
      .replace(/^\s*([-*+]|\d{1,9}[.)])\s+/, '')
    const task = /^\[([ xX])\]\s+/.exec(l)
    const text = (task ? l.slice(task[0].length) : l)
      .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/[*_`~|]/g, '')
      .replace(/\s+/g, ' ')
      .trim()
    lines.push(task && text ? `${task[1] === ' ' ? '[ ]' : '[x]'} ${text}` : text)
  }
  return Array.from(lines.filter(Boolean).join('\n')).slice(0, 400).join('')
}
