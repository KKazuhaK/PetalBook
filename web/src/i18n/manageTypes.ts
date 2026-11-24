import type { manageEn } from './manage/en.ts'

// The same shape in every language; what a message takes (a count, a name) is fixed by the English one.
type Widen<T> = T extends (...a: infer A) => string
  ? (...a: A) => string
  : T extends string
    ? string
    : T extends readonly string[]
      ? readonly string[]
      : { [K in keyof T]: Widen<T[K]> }

export type ManageMessages = Widen<typeof manageEn>
