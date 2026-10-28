// Package reposync selects the mirror sync transport for a config.
package reposync

import (
	"context"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
)

// Sync runs the shared-mirror sync or the SSH cache sync when mirror_remote is
// configured.
func Sync(ctx context.Context, cfg *config.Config) (gitutil.SyncReport, error) {
	if cfg.MirrorRemote != "" {
		return gitutil.SyncOverSSH(ctx, cfg.Mirror, cfg.MirrorRemote, cfg.RemoteURL, cfg.Remote)
	}
	return gitutil.Sync(ctx, cfg.Mirror, cfg.Remote)
}
