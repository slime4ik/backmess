// Package web — фронтенд, зашитый прямо в бинарь через go:embed.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var embedded embed.FS

func Static() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
