//go:build !no_web

package web

import (
	"embed"
	"io/fs"

	"github.com/chenhg5/cc-connect/core"
)

//go:embed all:dist
var distFS embed.FS

func init() {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: embed dist: " + err.Error())
	}
	core.RegisterWebAssets(sub)
}
