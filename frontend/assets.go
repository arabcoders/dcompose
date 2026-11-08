//go:build !embed

package frontend

import "io/fs"

func Assets() fs.FS {
	return nil
}
