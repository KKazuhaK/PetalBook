import { useState } from 'react'

// Previous and next over a list the server pages by cursor: the cursors of the pages already seen are kept, so going
// back asks for the same page again. `resetKey` (the search, the filters, the sort, the page size) starts over.
export function useCursorPages(resetKey: string) {
  const [state, setState] = useState<{ key: string; stack: string[] }>({ key: resetKey, stack: [''] })
  // A change of the key is a new list: back to its first page, in the same render.
  const stack = state.key === resetKey ? state.stack : ['']
  return {
    cursor: stack[stack.length - 1],
    page: stack.length,
    hasPrev: stack.length > 1,
    next: (to: string) => {
      if (to) setState({ key: resetKey, stack: [...stack, to] })
    },
    prev: () => {
      if (stack.length > 1) setState({ key: resetKey, stack: stack.slice(0, -1) })
    },
    first: () => setState({ key: resetKey, stack: [''] }),
  }
}
