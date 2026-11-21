package store

// SetDraftRaceHook makes every PutDraft call fn between reading the current
// draft and writing the new one; nil removes it.
func SetDraftRaceHook(fn func()) { testHookAfterDraftRead = fn }
