package core

import "io/fs"

var webAssetsFS fs.FS

// RegisterWebAssets registers the embedded web frontend assets.
// Called from web/embed.go's init() function.
func RegisterWebAssets(fsys fs.FS) {
	webAssetsFS = fsys
}

// GetWebAssets returns the registered web assets filesystem, or nil.
func GetWebAssets() fs.FS {
	return webAssetsFS
}

// WebAssetsAvailable reports whether web frontend assets are embedded.
func WebAssetsAvailable() bool {
	return webAssetsFS != nil
}
