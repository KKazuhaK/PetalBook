// The languages the code block offers, and how the `language` attribute of a
// code block is read and written.
//
// The attribute holds the whole info string of the fence ("js title=a.js");
// the Markdown layer splits it into the language and the rest. Everything here
// works on its first word and leaves the rest as it was.

export interface CodeLanguage {
  // The name a fence uses when the picker writes one.
  id: string
  // Shown in the picker.
  name: string
  // Other words people write after a fence that mean the same language.
  aliases?: string[]
}

// What the highlighter knows (lowlight's "common" set), under the names people
// use. A test checks every id and alias against the highlighter, so the list
// cannot promise colours that are not there. "Plain text" is not in it: it is
// the absence of a language.
export const LANGUAGES: readonly CodeLanguage[] = [
  { id: 'bash', name: 'Bash', aliases: ['sh', 'zsh'] },
  { id: 'c', name: 'C', aliases: ['h'] },
  { id: 'cpp', name: 'C++', aliases: ['c++', 'cc', 'hpp'] },
  { id: 'csharp', name: 'C#', aliases: ['cs', 'c#'] },
  { id: 'css', name: 'CSS' },
  { id: 'diff', name: 'Diff', aliases: ['patch'] },
  { id: 'go', name: 'Go', aliases: ['golang'] },
  { id: 'graphql', name: 'GraphQL', aliases: ['gql'] },
  { id: 'html', name: 'HTML', aliases: ['xml', 'svg'] },
  { id: 'ini', name: 'INI', aliases: ['toml'] },
  { id: 'java', name: 'Java' },
  { id: 'javascript', name: 'JavaScript', aliases: ['js', 'jsx', 'mjs', 'cjs'] },
  { id: 'json', name: 'JSON' },
  { id: 'kotlin', name: 'Kotlin', aliases: ['kt'] },
  { id: 'less', name: 'Less' },
  { id: 'lua', name: 'Lua' },
  { id: 'makefile', name: 'Makefile', aliases: ['make'] },
  { id: 'markdown', name: 'Markdown', aliases: ['md'] },
  { id: 'objectivec', name: 'Objective-C', aliases: ['objc'] },
  { id: 'perl', name: 'Perl', aliases: ['pl'] },
  { id: 'php', name: 'PHP' },
  { id: 'python', name: 'Python', aliases: ['py'] },
  { id: 'r', name: 'R' },
  { id: 'ruby', name: 'Ruby', aliases: ['rb'] },
  { id: 'rust', name: 'Rust', aliases: ['rs'] },
  { id: 'scss', name: 'SCSS' },
  { id: 'shell', name: 'Shell session', aliases: ['console'] },
  { id: 'sql', name: 'SQL' },
  { id: 'swift', name: 'Swift' },
  { id: 'typescript', name: 'TypeScript', aliases: ['ts', 'tsx'] },
  { id: 'wasm', name: 'WebAssembly' },
  { id: 'yaml', name: 'YAML', aliases: ['yml'] },
]

// Diagrams are drawn instead of coloured; the picker offers them as a language.
export const DIAGRAM: CodeLanguage = { id: 'mermaid', name: 'Mermaid', aliases: ['mmd'] }

export const PICKER_LANGUAGES: readonly CodeLanguage[] = [...LANGUAGES, DIAGRAM].sort((a, b) =>
  a.name.localeCompare(b.name, 'en'),
)

const byWord = new Map<string, CodeLanguage>()
for (const lang of [...LANGUAGES, DIAGRAM]) {
  byWord.set(lang.id, lang)
  for (const alias of lang.aliases ?? []) byWord.set(alias, lang)
}

// The first word of the info string, as written: "js title=a.js" -> "js".
export function languageWord(attr: unknown): string | null {
  if (typeof attr !== 'string') return null
  const word = attr.trim().split(/\s+/)[0]
  return word || null
}

// The language a code block is in, or null when it has none or one that is not known.
export function resolveLanguage(attr: unknown): CodeLanguage | null {
  const word = languageWord(attr)
  return word ? (byWord.get(word.toLowerCase()) ?? null) : null
}

// The word to hand to the highlighter, or null where nothing can be coloured.
export function highlightWord(attr: unknown): string | null {
  const lang = resolveLanguage(attr)
  return lang && lang !== DIAGRAM ? (languageWord(attr) as string).toLowerCase() : null
}

// What the picker shows as chosen: the language's name; a word that is not
// known, as written; or null for plain text.
export function languageLabel(attr: unknown): string | null {
  return resolveLanguage(attr)?.name ?? languageWord(attr)
}

// The info string after choosing `id` (null: plain text). The words after the
// language stay; the language itself is only rewritten when the new one is not
// already what is there ("ts" stays "ts" when TypeScript is chosen).
// Plain text drops the whole string: an info string cannot start without a language.
export function withLanguage(attr: unknown, id: string | null): string | null {
  if (id === null) return null
  const current = typeof attr === 'string' ? attr.trim() : ''
  if (resolveLanguage(current)?.id === id) return current
  // Only the first word changes: what follows it stays exactly as written.
  return current === '' ? id : current.replace(/^\S+/, id)
}

// The picker's list narrowed by what was typed: matches the name, the id and
// the aliases, ignoring case; names that start with the text come first.
export function filterLanguages(query: string): CodeLanguage[] {
  const q = query.trim().toLowerCase()
  if (!q) return [...PICKER_LANGUAGES]
  const words = (l: CodeLanguage) => [l.name.toLowerCase(), l.id, ...(l.aliases ?? [])]
  const hits = PICKER_LANGUAGES.filter((l) => words(l).some((w) => w.includes(q)))
  const rank = (l: CodeLanguage) => (words(l).some((w) => w.startsWith(q)) ? 0 : 1)
  return hits.sort((a, b) => rank(a) - rank(b))
}
