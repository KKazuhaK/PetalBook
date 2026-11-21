// A callback that is handed over before the thing it calls exists (the
// controller is built before the editor session's other pieces). Calling
// through it before it is set does nothing.
export class LateBound<A extends unknown[]> {
  private fn: ((...args: A) => void) | null = null
  bind(fn: ((...args: A) => void) | null) {
    this.fn = fn
  }
  call = (...args: A) => this.fn?.(...args)
}
