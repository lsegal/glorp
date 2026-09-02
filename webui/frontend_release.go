//go:build production

package webui

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"net/http"
)

//go:embed dist/*
var builtAssets embed.FS

func newAssets() http.Handler {
	dist, err := fs.Sub(builtAssets, "dist")
	if err != nil {
		panic(err)
	}
	return spaFileServer{assets: http.FileServer(http.FS(dist))}
}

// StartFrontend does nothing in a release build: the frontend is already built
// and embedded, so there is no Vite dev server to run.
func StartFrontend(context.Context, io.Writer, Supervisor) (func(), error) {
	return func() {}, nil
}

type spaFileServer struct{ assets http.Handler }

func (server spaFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		if _, err := fs.Stat(builtAssets, "dist"+r.URL.Path); err != nil {
			r.URL.Path = "/"
		}
	}
	server.assets.ServeHTTP(w, r)
}
