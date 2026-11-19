// Command fixtureserve serves testdata/fixtures for local end-to-end checks.
// It rewrites the fixture's hard-coded http://127.0.0.1:8090 base to the address
// the request arrived on, so the fixtures also work on another free port.
package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const fixtureBase = "http://127.0.0.1:8090"

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	dir := flag.String("dir", "testdata/fixtures", "fixture directory")
	updated := flag.Bool("updated", false, "serve feed-updated.xml at /feed.xml")
	flag.Parse()

	root, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		log.Fatalf("fixture directory %q is not readable: %v", root, err)
	}
	log.Printf("serving %s at http://%s (updated feed: %v)", root, *addr, *updated)
	server := &http.Server{Addr: *addr, Handler: handler(root, *updated)}
	log.Fatal(server.ListenAndServe())
}

func handler(root string, updated bool) http.Handler {
	dir := http.Dir(root)
	files := http.FileServer(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
		if updated && name == "/feed.xml" {
			name = "/feed-updated.xml"
		}
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".opml") {
			files.ServeHTTP(w, r)
			return
		}
		file, err := dir.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 4<<20))
		if err != nil {
			http.Error(w, "cannot read fixture", http.StatusInternalServerError)
			return
		}
		data = bytes.ReplaceAll(data, []byte(fixtureBase), []byte("http://"+r.Host))
		if strings.HasSuffix(name, ".opml") {
			w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		}
		_, _ = w.Write(data)
	})
}
