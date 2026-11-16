//go:build !embed

package frontend

import (
	"io/fs"
	"os"
)

func Assets() fs.FS {
	return os.DirFS("frontend/exported")
}
