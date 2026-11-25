package spaces

import "petalbook.app/server/internal/assetrepo"

func assetrepoMaxCommit() int64     { return assetrepo.MaxCommitBytes }
func setAssetrepoMaxCommit(n int64) { assetrepo.MaxCommitBytes = n }
