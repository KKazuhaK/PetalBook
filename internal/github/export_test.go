package github

// SetRenewReadHook makes every renewal call fn(userID) right after it read the
// user's connection from the database; nil removes it.
func SetRenewReadHook(fn func(userID string)) { testHookAfterRenewRead = fn }
