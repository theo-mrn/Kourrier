package console

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the build of web/console (make console-build). Without it, the
// console serves a page explaining how to build it.
//
//go:embed all:dist
var dist embed.FS

// notBuilt is served when the binary was built without the web console. It
// has no inline style or script, so it complies with the console's CSP.
const notBuilt = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Kourrier console</title>
<h1>The Kourrier console is not built into this binary</h1>
<p>Build the web console, then the binary:</p>
<pre>make console-build
make build</pre>
<p>The container image always includes it. The admin API is available under <code>/api/</code>.</p>
</html>
`

// spaHandler serves the single-page application: files of the build as they
// are, and index.html for any other path so that client-side routes can be
// reloaded.
func spaHandler() http.Handler {
	return spa(dist)
}

func spa(root fs.FS) http.Handler {
	sub, err := fs.Sub(root, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		index = []byte(notBuilt)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" && !strings.HasPrefix(name, ".") {
			if st, err := fs.Stat(sub, name); err == nil && !st.IsDir() {
				// Vite fingerprints the assets: they never change.
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
