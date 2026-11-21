// Markdown round-trip check for a candidate editor.
//
// For every case in corpus/cases.txt:
//   pass 1:  source -> editor -> markdown (out1)
//   pass 2:  out1   -> editor -> markdown (out2)
//
// Verdicts, most to least desirable:
//   exact    out1 === source                    (no diff at all)
//   stable   out1 !== source, out2 === out1, same rendered HTML
//            (one-time normalisation, nothing lost)
//   lossy    rendered HTML of out1 differs from the source's
//   unstable out2 !== out1 (every save changes the file again)
//
// Usage: node roundtrip.mjs <adapter> [--show=<verdict>[,<verdict>]] [--only=<substr>]

import { readFileSync } from 'node:fs'
import { marked } from 'marked'
import { createPatch } from 'diff'

const [adapterName, ...flags] = process.argv.slice(2)
const flag = (name) => flags.find((f) => f.startsWith(`--${name}=`))?.split('=')[1]
const show = new Set((flag('show') ?? 'lossy,unstable').split(','))
const only = flag('only')

const { roundtrip, label } = await import(`./adapters/${adapterName}.mjs`)

const cases = readFileSync(new URL('./corpus/cases.txt', import.meta.url), 'utf8')
  .split(/^=== /m)
  .filter(Boolean)
  .map((chunk) => {
    const nl = chunk.indexOf('\n')
    return { name: chunk.slice(0, nl).trim(), source: chunk.slice(nl + 1).replace(/\n+$/, '') + '\n' }
  })
  .filter((c) => !only || c.name.includes(only))

// Rendered form used to decide whether meaning survived. Whitespace between
// tags is not meaningful for this comparison.
const render = (md) =>
  marked
    .parse(md, { gfm: true })
    .replace(/>\s+</g, '><')
    .replace(/\s+/g, ' ')
    .trim()

const norm = (md) => md.replace(/\n+$/, '') + '\n'

const tally = { exact: 0, stable: 0, lossy: 0, unstable: 0, error: 0 }
const rows = []

for (const c of cases) {
  let verdict, out1, out2, error
  try {
    out1 = norm(await roundtrip(c.source))
    out2 = norm(await roundtrip(out1))
    if (out2 !== out1) verdict = 'unstable'
    else if (out1 === c.source) verdict = 'exact'
    else if (render(out1) === render(c.source)) verdict = 'stable'
    else verdict = 'lossy'
  } catch (e) {
    verdict = 'error'
    error = e
  }
  tally[verdict]++
  rows.push({ ...c, verdict, out1, out2, error })
}

console.log(`\n${label}: ${cases.length} cases`)
for (const r of rows) console.log(`  ${r.verdict.padEnd(8)} ${r.name}`)
console.log('\n' + Object.entries(tally).map(([k, v]) => `${k}=${v}`).join('  '))
// A file that changes again on every save, or cannot be read, fails the run.
process.exitCode = tally.unstable || tally.error ? 1 : 0

for (const r of rows) {
  if (r.verdict === 'error' && (show.has('error') || show.has('lossy'))) {
    console.log(`\n--- ${r.name} [error]\n${r.error?.stack ?? r.error}`)
    continue
  }
  if (!show.has(r.verdict)) continue
  console.log(`\n--- ${r.name} [${r.verdict}]`)
  console.log(createPatch('pass1', r.source, r.out1, 'source', 'out1').split('\n').slice(4).join('\n'))
  if (r.verdict === 'unstable') {
    console.log(createPatch('pass2', r.out1, r.out2, 'out1', 'out2').split('\n').slice(4).join('\n'))
  }
}
