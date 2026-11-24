// The mock's copy of how the server cuts the attribution off a line of the administrators' own text
// (internal/api/dailyattrib.go): `text —— Author《Source》`, `text — Author, Source`, `text -- Author`.

export interface Attribution {
  text: string
  author: string
  source: string
}

const MARKS = [' —— ', ' — ', ' -- ']
const PARTICLES = new Set('de da del della di van von der den la le du dos bin ibn al el of the and y e'.split(' '))

// A sentence's punctuation: a name does not have it.
const sentencey = (s: string) => {
  const t = s.replace(/\.+$/, '')
  return /[。！？!?；;：:，、"“”]/.test(t) || t.includes('—') || t.includes(' -- ')
}

const isHan = (c: string) => /\p{Script=Han}/u.test(c)

function readAttribution(rest: string): { author: string; source: string } | null {
  if (/[\r\n]/.test(rest)) return null
  const open = rest.indexOf('《')
  if (open >= 0) {
    const end = rest.indexOf('》', open)
    if (end < 0) return null
    const source = rest.slice(open + 1, end).trim()
    const author = rest.slice(0, open).trim()
    if (
      source === '' ||
      rest.slice(end + 1).trim() !== '' ||
      [...source].length > 60 ||
      [...author].length > 40 ||
      sentencey(author)
    )
      return null
    return { author, source }
  }
  if (sentencey(rest) || /[《》]/.test(rest)) return null
  const first = [...rest][0]
  if (isHan(first)) {
    const n = [...rest].length
    if (n > 12 && !(rest.includes('·') && n <= 20)) return null
    return { author: rest, source: '' }
  }
  const comma = rest.indexOf(', ')
  const name = comma >= 0 ? rest.slice(0, comma) : rest
  const source = comma >= 0 ? rest.slice(comma + 2) : ''
  const words = name.split(/\s+/).filter(Boolean)
  if (words.length === 0 || words.length > 5 || [...rest].length > 60) return null
  for (const [i, w] of words.entries()) {
    const c = [...w][0]
    if (
      c !== c.toLowerCase() ||
      /\d/.test(c) ||
      /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u.test(c)
    )
      continue
    if (i > 0 && PARTICLES.has(w.toLowerCase().replace(/^[.,]+|[.,]+$/g, ''))) continue
    return null
  }
  return { author: name.trim(), source: source.trim() }
}

export function parseAttribution(line: string): Attribution {
  const whole = line.trim()
  let at = -1
  let mark = ''
  for (const m of MARKS) {
    const i = whole.lastIndexOf(m)
    if (i > at) {
      at = i
      mark = m
    }
  }
  if (at <= 0) return { text: whole, author: '', source: '' }
  const text = whole.slice(0, at).trim()
  const rest = whole.slice(at + mark.length).trim()
  if (text === '' || rest === '') return { text: whole, author: '', source: '' }
  const got = readAttribution(rest)
  return got ? { text, ...got } : { text: whole, author: '', source: '' }
}
