package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
)

// PathToURI converts a filesystem path to a file:// URI. The path is made
// absolute when possible; each segment is percent-escaped.
func PathToURI(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := &url.URL{Scheme: "file", Path: path}
	return u.String()
}

// URIToPath converts a file:// URI back to a filesystem path. Non-file or
// unparseable URIs are returned unchanged.
func URIToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	p := u.Path
	if p == "" {
		p = u.Opaque
	}
	return filepath.FromSlash(p)
}
