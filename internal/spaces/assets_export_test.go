package spaces

import "github.com/KKazuhaK/PetalBook/internal/assetrepo"

func assetrepoMaxCommit() int64     { return assetrepo.MaxCommitBytes }
func setAssetrepoMaxCommit(n int64) { assetrepo.MaxCommitBytes = n }
