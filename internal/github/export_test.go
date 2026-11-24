package github

// SetRenewReadHook makes every renewal call fn(userID) right after it read the
// user's connection from the database; nil removes it.
func SetRenewReadHook(fn func(userID string)) { testHookAfterRenewRead = fn }

// SetRefreshWriteHook makes every re-check of the App call fn after it asked
// GitHub and before it writes; nil removes it.
func SetRefreshWriteHook(fn func()) { testHookBeforeRefreshWrite = fn }
