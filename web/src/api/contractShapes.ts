// The shape of a JSON answer: its keys and their types, recursively, with an
// array reduced to one element. No value is kept, so nothing personal or random
// can end up in a checked-in file. The contract suite records the shapes of the
// real server's answers (PETALBOOK_RECORD=1) and checks every answer against
// them; the fixtures of the web tests are checked against the same file.
//
//   null, boolean, number, string  the scalar types, joined with "|" when an
//                                  answer had more than one ("null|string")
//   {key: shape}                   an object; a key that was missing from some
//                                  answers is written "key?"
//   [shape]                        an array (the shape of all its elements);
//                                  [] when no answer had an element
//   {"$or": [shape, ...]}          a place that held more than one kind of value

export type Shape = string | Shape[] | { [key: string]: Shape }

type Parts = { scalars: Set<string>; array?: Shape[]; object?: Record<string, Shape> }

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

function partsOf(s: Shape): Parts {
  if (typeof s === 'string') return { scalars: new Set(s.split('|')) }
  if (Array.isArray(s)) return { scalars: new Set(), array: s }
  if (Object.keys(s).length === 1 && '$or' in s) {
    const out: Parts = { scalars: new Set() }
    for (const alt of s.$or as Shape[]) merge(out, partsOf(alt))
    return out
  }
  return { scalars: new Set(), object: s }
}

function merge(into: Parts, from: Parts): void {
  for (const x of from.scalars) into.scalars.add(x)
  if (from.array)
    into.array = into.array
      ? into.array.length && from.array.length
        ? [join(into.array[0], from.array[0])]
        : [...into.array, ...from.array]
      : from.array
  if (from.object) into.object = into.object ? joinObjects(into.object, from.object) : from.object
}

function joinObjects(a: Record<string, Shape>, b: Record<string, Shape>): Record<string, Shape> {
  const base = (k: string) => (k.endsWith('?') ? k.slice(0, -1) : k)
  const ma = new Map(Object.entries(a).map(([k, v]) => [base(k), { v, opt: k.endsWith('?') }]))
  const mb = new Map(Object.entries(b).map(([k, v]) => [base(k), { v, opt: k.endsWith('?') }]))
  const out: Record<string, Shape> = {}
  for (const k of [...new Set([...ma.keys(), ...mb.keys()])].sort()) {
    const x = ma.get(k)
    const y = mb.get(k)
    const optional = !x || !y || x.opt || y.opt
    out[optional ? `${k}?` : k] = x && y ? join(x.v, y.v) : (x ?? y)!.v
  }
  return out
}

function build(p: Parts): Shape {
  const alts: Shape[] = []
  if (p.scalars.size) alts.push([...p.scalars].sort().join('|'))
  if (p.array) alts.push(p.array)
  if (p.object) alts.push(p.object)
  return alts.length === 1 ? alts[0] : { $or: alts }
}

/** The shape that holds both a and b. */
export function join(a: Shape, b: Shape): Shape {
  const p = partsOf(a)
  merge(p, partsOf(b))
  return build(p)
}

/** The shape of one JSON value. `scrub` rewrites object keys (to drop what a run made up). */
export function shapeOf(v: unknown, scrub: (key: string) => string = (k) => k): Shape {
  if (v === null || v === undefined) return 'null'
  if (Array.isArray(v)) return v.length ? [v.map((e) => shapeOf(e, scrub)).reduce(join)] : []
  if (isObject(v)) {
    const out: Record<string, Shape> = {}
    for (const k of Object.keys(v).sort()) out[scrub(k)] = shapeOf(v[k], scrub)
    return out
  }
  return typeof v
}

/** What is wrong with `v` against `shape`: one line per problem, none when it fits. */
export function misfits(v: unknown, shape: Shape, scrub: (key: string) => string = (k) => k, at = '$'): string[] {
  const p = partsOf(shape)
  const kind = v === null || v === undefined ? 'null' : typeof v
  if (Array.isArray(v)) {
    if (!p.array) return [`${at}: an array, recorded ${JSON.stringify(shape)}`]
    return p.array.length ? v.flatMap((e, i) => misfits(e, p.array![0], scrub, `${at}[${i}]`)) : []
  }
  if (isObject(v)) {
    if (!p.object) return [`${at}: an object, recorded ${JSON.stringify(shape)}`]
    const out: string[] = []
    const known = new Set<string>()
    const seen: Record<string, unknown> = {}
    for (const k of Object.keys(v)) seen[scrub(k)] = v[k]
    for (const [k, s] of Object.entries(p.object)) {
      const name = k.endsWith('?') ? k.slice(0, -1) : k
      known.add(name)
      if (name in seen) out.push(...misfits(seen[name], s, scrub, `${at}.${name}`))
      else if (!k.endsWith('?')) out.push(`${at}.${name}: missing`)
    }
    for (const k of Object.keys(seen)) if (!known.has(k)) out.push(`${at}.${k}: a new key`)
    return out
  }
  return p.scalars.has(kind) ? [] : [`${at}: ${kind}, recorded ${[...p.scalars].join('|') || JSON.stringify(shape)}`]
}

/**
 * The route a request belongs to, as the shapes file names it: the path with every segment that is not a plain
 * lower-case word (an id, a name a run made up, a settings key) written {}.
 */
export const routeOf = (path: string) =>
  path
    .split('?')[0]
    .split('/')
    .map((s) => (s === '' || /^[a-z]+(-[a-z]+)*$/.test(s) ? s : '{}'))
    .join('/')
