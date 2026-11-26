package connections

// SetDueHook makes f run before each query of the loop for due links; an error
// it returns is that query's. Set it before Run.
func SetDueHook(t *Tokens, f func() error) { t.dueHook = f }
