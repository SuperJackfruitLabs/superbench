package benchstate

import (
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func aggregate() *model.Bench {
	data := "/srv/data"
	root := filepath.Join(data, "benches", "card")
	repo := "/srv/source.git"
	return &model.Bench{Key: "card", Path: root, Lease: &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 48), Continuation: strings.Repeat("b", 32)}, Workspace: &model.Workspace{ID: strings.Repeat("c", 32), CreationID: "request1", Phase: "materializing", CheckpointRepository: "api", Members: []model.WorkspaceMember{{Name: "api", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(root, "api"), Mirror: repository.Path(data, repo), Source: strings.Repeat("d", 40), ObjectFormat: "sha1"}}}}
}
