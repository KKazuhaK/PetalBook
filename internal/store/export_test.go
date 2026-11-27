package store

import "context"

// SetDraftRaceHook makes every PutDraft call fn between reading the current
// draft and writing the new one; nil removes it.
func SetDraftRaceHook(fn func()) { testHookAfterDraftRead = fn }

// SetEmailInsertRaceHook makes every SetPrimaryEmail call fn with its
// transaction between its check of who holds the address and its insert; nil
// removes it.
func SetEmailInsertRaceHook(fn func(Q)) { testHookBeforeEmailInsert = fn }

// ReloadCustomRoles reads again whether any custom role exists, as Open does.
func (s *Store) ReloadCustomRoles() error { return s.loadCustomRoles(context.Background()) }

// SetPrimaryRaceHook makes every MakePrimaryEmail call fn with its
// transaction after it demoted the old primary and before it promotes the
// address; nil removes it.
func SetPrimaryRaceHook(fn func(Q)) { testHookBeforePrimarySet = fn }
