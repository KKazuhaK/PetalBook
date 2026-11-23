package store

import "context"

// SetDraftRaceHook makes every PutDraft call fn between reading the current
// draft and writing the new one; nil removes it.
func SetDraftRaceHook(fn func()) { testHookAfterDraftRead = fn }

// FillSpaceNameKeys runs the step that fills name_key of migration 7.
func FillSpaceNameKeys(ctx context.Context, q Q) error { return fillSpaceNameKeys(ctx, q) }

// MigrationVersions lists the version of every embedded migration, in order.
func MigrationVersions() ([]int, error) {
	list, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	out := make([]int, len(list))
	for i, m := range list {
		out[i] = m.version
	}
	return out, nil
}

// MigrationNames lists the file name of every embedded migration, in order.
func MigrationNames() ([]string, error) {
	list, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.name
	}
	return out, nil
}

// SetGitHubPutRaceHook makes every PutGitHubAuthorization call fn between its
// check of the GitHub account and its write; nil removes it.
func SetGitHubPutRaceHook(fn func()) { testHookBeforeGitHubPut = fn }
