//go:build embed

package frontend

import (
	"embed"
	"io/fs"
)

// Files contains the generated Nuxt SPA.
// From the repository root, run bun --cwd=./frontend/ generate before release builds.
//
//go:embed all:exported
var Files embed.FS

func Assets() fs.FS {
	assets, err := fs.Sub(Files, "exported")
	if nil != err {
		panic(err)
	}
	return assets
}
