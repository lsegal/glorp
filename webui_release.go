//go:build production

package main

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"net/http"
)

//go:embed web/dist/*
var webUIAssets embed.FS

func newWebUIAssets() http.Handler {
	dist, err := fs.Sub(webUIAssets, "web/dist")
	if err != nil {
		panic(err)
	}
	return spaFileServer{assets: http.FileServer(http.FS(dist))}
}

func startWebUIFrontend(context.Context, io.Writer) (func(), error) { return func() {}, nil }

type spaFileServer struct{ assets http.Handler }

func (server spaFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		if _, err := fs.Stat(webUIAssets, "web/dist"+r.URL.Path); err != nil {
			r.URL.Path = "/"
		}
	}
	server.assets.ServeHTTP(w, r)
}
