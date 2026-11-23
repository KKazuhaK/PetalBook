package store

import "context"

// SetDraftRaceHook makes every PutDraft call fn between reading the current
// draft and writing the new one; nil removes it.
func SetDraftRaceHook(fn func()) { testHookAfterDraftRead = fn }

// FillSpaceNameKeys runs the step that fills name_key of migration 7.
func FillSpaceNameKeys(ctx context.Context, q Q) error { return fillSpaceNameKeys(ctx, q) }

// SeedUsage runs the step that makes the usage rows of migration 18 (the table must be empty).
func SeedUsage(ctx context.Context, q Q) error { return seedUsage(ctx, q) }
