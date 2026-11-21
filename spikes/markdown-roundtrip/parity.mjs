// Does the Go renderer agree with the front end on what a file means?
//
// Reads ../render-parity/cases.json (Markdown as the editor saves it) and
// ../render-parity/go.json (the same cases rendered to HTML by goldmark),
// renders each case with the front end's own pipeline, and compares the two
// after removing differences that are only markup style.
//
// Usage: node parity.mjs [--show]

import { readFileSync } from 'node:fs'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
import remarkCjkFriendly from 'remark-cjk-friendly'
import remarkCjkFriendlyStrike from 'remark-cjk-friendly-gfm-strikethrough'
import remarkRehype from 'remark-rehype'
import rehypeStringify from 'rehype-stringify'

const dir = new URL('../render-parity/', import.meta.url)
const cases = JSON.parse(readFileSync(new URL('cases.json', dir), 'utf8'))
const go = JSON.parse(readFileSync(new URL('go.json', dir), 'utf8'))
const show = process.argv.includes('--show')

const js = unified()
  .use(remarkParse)
  .use(remarkGfm, { singleTilde: false })
  .use(remarkCjkFriendly)
  .use(remarkCjkFriendlyStrike, { singleTilde: false })
  .use(remarkRehype, { allowDangerousHtml: true })
  .use(rehypeStringify, { allowDangerousHtml: true })

// Reduce HTML to what a reader would see as structure: which tags wrap which
// text, plus link and image targets. Everything else is renderer style.
function shape(html) {
  return html
    .replace(/<input[^>]*checked[^>]*>/g, '[x]')
    .replace(/<input[^>]*>/g, '[ ]')
    .replace(/<(\/?)(\w+)([^>]*)>/g, (_, close, tag, attrs) => {
      tag = tag.toLowerCase()
      if (tag === 'thead' || tag === 'tbody') return ''
      if (close) return `</${tag}>`
      const keep = []
      for (const name of ['href', 'src', 'alt', 'title', 'start']) {
        const m = attrs.match(new RegExp(`\\s${name}="((?:[^"]|"(?=[^=]*"))*)"`))
        if (m) keep.push(`${name}="${decodeURI(m[1].replace(/&amp;/g, '&')).replace(/%28/g, '(').replace(/%29/g, ')')}"`)
      }
      const lang = attrs.match(/class="language-([^"\s]+)/)
      if (lang) keep.push(`lang="${lang[1]}"`)
      const align = attrs.match(/align="(\w+)"|text-align:\s*(\w+)/)
      if (align) keep.push(`align="${align[1] ?? align[2]}"`)
      return `<${tag}${keep.length ? ' ' + keep.join(' ') : ''}>`
    })
    .replace(/&quot;|&#x22;|&#34;/gi, '"')
    .replace(/&gt;|&#x3E;|&#62;/gi, '>')
    .replace(/&#x3C;|&#60;/gi, '&lt;')
    .replace(/&#x26;|&#38;/gi, '&amp;')
    .replace(/&#39;|&#x27;/gi, "'")
    .replace(/<br>\s*/g, '<br>')
    .replace(/\s+/g, ' ')
    .replace(/> </g, '><')
    .replace(/ ?(<\/?(?:p|li|ul|ol|h\d|blockquote|pre|table|tr|td|th|hr)[^>]*>) ?/g, '$1')
    .replace(/\n?<\/code><\/pre>/g, '</code></pre>')
    .trim()
}

let same = 0
const different = []
for (const [name, markdown] of Object.entries(cases)) {
  const a = shape(String(js.processSync(markdown)))
  const b = shape(go[name] ?? '')
  if (a === b) same++
  else different.push({ name, markdown, a, b })
}

console.log(`front end vs Go: ${same}/${Object.keys(cases).length} cases render the same`)
// Differences already understood; each needs a change in lib/markdown.mjs.
const known = new Set(['mark/交叠', 'lit/网址'])
for (const d of different) console.log(`  ${known.has(d.name) ? 'known     ' : 'UNEXPECTED'} ${d.name}`)
process.exitCode = different.some((d) => !known.has(d.name)) ? 1 : 0
if (show) {
  for (const d of different) {
    console.log(`\n--- ${d.name}\nmarkdown: ${JSON.stringify(d.markdown)}\nfront:    ${d.a}\ngo:       ${d.b}`)
  }
}
