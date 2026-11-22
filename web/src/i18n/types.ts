// The shape every language has to have. It is the Chinese dictionary with its
// string literals widened to `string` and its functions kept by signature, so
// a language file that misses a key, adds one, or turns a function into plain
// text does not compile.

import type { zhCN } from './zh-CN.ts'

type Widen<T> = T extends string ? string : T extends (...args: infer A) => string ? (...args: A) => string : T extends object ? { -readonly [K in keyof T]: Widen<T[K]> } : T

export type Messages = Widen<typeof zhCN>
