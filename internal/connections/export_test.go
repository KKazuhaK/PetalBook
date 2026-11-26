package connections

import "context"

// SetDueHook makes f run before each query of the loop for due links; an error
// it returns is that query's. Set it before Run.
func SetDueHook(t *Tokens, f func() error) { t.dueHook = f }

// SetClaimLostHook makes f run when a renewal could not claim a link's lease,
// before it reads the link again. Set it before use.
func SetClaimLostHook(t *Tokens, f func()) { t.claimLostHook = f }

// SetRenewContext makes f make each renewal's context from its caller's. Set
// it before use.
func SetRenewContext(t *Tokens, f func(context.Context) (context.Context, context.CancelFunc)) {
	t.renewCtx = f
}
