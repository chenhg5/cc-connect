//go:build !no_web

package web

import (
	"embed"
	"io/fs"
	"log/slog"

	"github.com/chenhg5/cc-connect/core"
)

//go:embed all:dist
var distFS embed.FS

func init() {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		slog.Warn("web: embedded dist directory unavailable; web admin disabled", "error", err)
		return
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		slog.Warn("web: embedded dist assets missing index.html; web admin disabled", "error", err)
		return
	}
	core.RegisterWebAssets(sub)
}
