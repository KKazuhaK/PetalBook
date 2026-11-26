package store

import "context"

// SetDraftRaceHook makes every PutDraft call fn between reading the current
// draft and writing the new one; nil removes it.
func SetDraftRaceHook(fn func()) { testHookAfterDraftRead = fn }

// SetGitHubPutRaceHook makes every PutGitHubAuthorization call fn between its
// check of the GitHub account and its write; nil removes it.
func SetGitHubPutRaceHook(fn func()) { testHookBeforeGitHubPut = fn }

// ReloadCustomRoles reads again whether any custom role exists, as Open does.
func (s *Store) ReloadCustomRoles() error { return s.loadCustomRoles(context.Background()) }
