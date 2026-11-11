package httpserver

import (
	"embed"
	"net/http"
	"path"
)

// pwa holds the installable-app assets (manifest, service worker, icons).
// Sources live in console/public; the Dockerfile refreshes these copies.
//
//go:embed pwa/*
var pwaFiles embed.FS

var pwaTypes = map[string]string{
	"manifest.webmanifest": "application/manifest+json",
	"sw.js":                "text/javascript; charset=utf-8",
	"icon.svg":             "image/svg+xml",
	"favicon.svg":          "image/svg+xml",
	"favicon.ico":          "image/x-icon",
	"og.jpg":               "image/jpeg",
}

// pwaAsset serves one public, secret-free asset so the browser can offer
// "Install app" even before the console token is entered.
func pwaAsset(name string) http.HandlerFunc {
	body, err := pwaFiles.ReadFile(path.Join("pwa", name))
	if err != nil {
		panic("missing embedded pwa asset " + name)
	}
	ctype := pwaTypes[name]
	if ctype == "" {
		ctype = "image/png"
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if name == "sw.js" || name == "manifest.webmanifest" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		_, _ = w.Write(body)
	}
}

var pwaAssets = []string{
	"manifest.webmanifest",
	"sw.js",
	"icon.svg",
	"icon-192.png",
	"icon-512.png",
	"icon-maskable-512.png",
	"apple-touch-icon.png",
	"favicon.ico",
	"favicon.svg",
	"favicon-96.png",
	"og.jpg",
}
