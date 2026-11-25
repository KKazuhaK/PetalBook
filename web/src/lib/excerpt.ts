// The excerpt of a page, built the way the server builds it (internal/page/text.go), for the offline reader and
// the mock server.

// The text of a page for a listing: up to 400 characters, one line per non-blank line of the body.
export function excerptOf(markdown: string): string {
  const lines = markdown
    .replace(/```[^\n]*\n([\s\S]*?)```/g, '$1')
    .split('\n')
    .map((raw) => {
      const l = raw.trim().replace(/^(#{1,6}|>|[-*+]|\d+\.)\s+/, '')
      // A task item keeps its state at the start of the line, as the server's excerpt does.
      const task = /^\[([ xX])\]\s+/.exec(l)
      const text = (task ? l.slice(task[0].length) : l)
        .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
        .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
        .replace(/[*_`~|]/g, '')
        .replace(/\s+/g, ' ')
        .trim()
      return task && text ? `${task[1] === ' ' ? '[ ]' : '[x]'} ${text}` : text
    })
    .filter(Boolean)
  return Array.from(lines.join('\n')).slice(0, 400).join('')
}
