package store

import "context"

// SetDraftRaceHook makes every PutDraft call fn between reading the current
// draft and writing the new one; nil removes it.
func SetDraftRaceHook(fn func()) { testHookAfterDraftRead = fn }

// FillSpaceNameKeys runs the step that fills name_key of migration 7.
func FillSpaceNameKeys(ctx context.Context, q Q) error { return fillSpaceNameKeys(ctx, q) }
