package forge

// SetReloadHook sets a function called by Reload right after it has read the
// providers of the database (tests); nil clears it.
func SetReloadHook(f func()) { testHookReloadRead = f }
