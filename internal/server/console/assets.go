package console

import (
	"embed"
	"io/fs"
	"net/http"
)

// webUI holds the embedded Membrane Console single-page UI. Embedding the assets
// in the binary is what gives the Jenkins/Neo4j experience: build the image,
// open the port, operate in a browser — no second service or runtime dependency.
//
//go:embed webui/dist
var webUI embed.FS

// staticHandler serves the embedded UI. Unknown paths fall back to index.html so
// client-side routing works (SPA behavior).
func staticHandler() http.Handler {
	sub, err := fs.Sub(webUI, "webui/dist")
	if err != nil {
		// The embed directive guarantees webui/dist exists at build time.
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := trimLeadingSlash(r.URL.Path)
		if p == "" {
			serveIndex(w, index)
			return
		}
		if f, err := sub.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve the shell for unknown (client-routed) paths.
		serveIndex(w, index)
	})
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
