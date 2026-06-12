// Package webui embeds the Matter workspace single-page app served at /ui/.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// FS returns the embedded UI rooted at the static directory.
func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // embed layout is fixed at compile time
	}
	return sub
}
