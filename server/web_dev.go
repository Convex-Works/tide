//go:build !embed

package klisi

import "io/fs"

// WebFS is absent in development because Vite serves the SPA directly.
func WebFS() fs.FS {
	return nil
}
