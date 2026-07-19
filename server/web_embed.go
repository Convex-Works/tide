//go:build embed

package klisi

import (
	"embed"
	"io/fs"
)

//go:embed all:web/build
var embeddedWeb embed.FS

func WebFS() fs.FS {
	web, err := fs.Sub(embeddedWeb, "web/build")
	if err != nil {
		panic(err)
	}
	return web
}
