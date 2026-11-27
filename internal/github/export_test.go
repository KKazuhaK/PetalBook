package github

// SetRefreshWriteHook makes every re-check of the App call fn after it asked
// GitHub and before it writes; nil removes it.
func SetRefreshWriteHook(fn func()) { testHookBeforeRefreshWrite = fn }
