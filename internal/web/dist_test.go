package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// webSource is web/ (the page's source) from this package's directory.
var webSource = filepath.Join("..", "..", "web")

// sourceHash is web/stamp.ts's hash in Go, the two alike: every regular file under root, skipping node_modules and any
// name starting with "."; per file "<slash path>\0<sha256 hex>\n", sorted by path; the sha256 hex of all of it.
func sourceHash(root string) (string, error) {
	type file struct{ path, sum string }
	var files []file
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		sum := sha256.Sum256(b)
		files = append(files, file{filepath.ToSlash(rel), hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.SortFunc(files, func(a, b file) int { return strings.Compare(a.path, b.path) })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\n", f.path, f.sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TestDistFresh (T-760): the embedded page was built from web/ as it is — the stamp `vite build` wrote is the hash of
// web/'s sources now. A change to web/ without `make web` fails here (the gate never builds the page: no network).
func TestDistFresh(t *testing.T) {
	stamp, err := fs.ReadFile(distFS, "source.sha256")
	if err != nil {
		t.Fatalf("dist/ has no source.sha256: run make web (%v)", err)
	}
	want, err := sourceHash(webSource)
	if err != nil {
		t.Fatalf("hashing web/: %v", err)
	}
	if got := strings.TrimSpace(string(stamp)); got != want {
		t.Errorf("internal/web/dist is stale: built from web/ at %s, web/ is %s now: run make web and commit dist/",
			got[:min(12, len(got))], want[:12])
	}
}

// bundle is the built page's scripts, concatenated: what a test greps for the routes and fields the page reads.
func bundle(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	err := fs.WalkDir(distFS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		data, err := fs.ReadFile(distFS, p)
		b.Write(data)
		return err
	})
	if err != nil || b.Len() == 0 {
		t.Fatalf("no script in dist/assets: %v", err)
	}
	return b.String()
}

// TestPageIsEmbedded: "/" (and any other page path) is the built index.html, which loads its hashed assets; an asset
// is served with its type and cached for good; an unknown asset is 404; and the page calls every read route.
func TestPageIsEmbedded(t *testing.T) {
	_, _, s := fixture(t)
	h := s.Handler()
	for _, path := range []string{"/", "/index.html", "/anything"} {
		r := get(t, h, path)
		body := r.Body.String()
		if r.Code != http.StatusOK || !strings.HasPrefix(strings.ToLower(body), "<!doctype html>") {
			t.Fatalf("%s: %d, %.60q", path, r.Code, body)
		}
		if r.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store (a new build shows at once)", path, r.Header().Get("Cache-Control"))
		}
	}
	index := get(t, h, "/").Body.String()
	entries, err := fs.ReadDir(distFS, "assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("dist/assets: %v", err)
	}
	for _, e := range entries {
		if !strings.Contains(index, "/assets/"+e.Name()) {
			t.Errorf("index.html does not load %s", e.Name())
		}
		r := get(t, h, "/assets/"+e.Name())
		if r.Code != http.StatusOK {
			t.Errorf("/assets/%s: %d", e.Name(), r.Code)
		}
		want := map[string]string{".js": "javascript", ".css": "text/css"}[filepath.Ext(e.Name())]
		if ct := r.Header().Get("Content-Type"); want != "" && !strings.Contains(ct, want) {
			t.Errorf("/assets/%s: Content-Type %q, want %s", e.Name(), ct, want)
		}
		if !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("/assets/%s: Cache-Control %q, want immutable", e.Name(), r.Header().Get("Cache-Control"))
		}
	}
	if r := get(t, h, "/assets/nope.js"); r.Code != http.StatusNotFound {
		t.Errorf("/assets/nope.js: %d, want 404", r.Code)
	}
	js := bundle(t)
	for _, route := range []string{"/api/projects", "/api/view", "/api/setup", "/api/events", "/api/task", "/api/agent",
		"/api/job", "/api/config", "/api/pause"} {
		if !strings.Contains(js, route) {
			t.Errorf("the page never calls %s", route)
		}
	}
}
