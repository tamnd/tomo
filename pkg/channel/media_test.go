package channel

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

// a tiny valid PNG (1x1) reused across the media tests.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestFetchImageDecodesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth header not forwarded, got %q", got)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	}))
	defer srv.Close()

	hdr := http.Header{"Authorization": {"Bearer tok"}}
	block, err := FetchImage(context.Background(), srv.Client(), srv.URL, hdr)
	if err != nil {
		t.Fatal(err)
	}
	if block.Type != provider.BlockImage || block.MediaType != "image/png" {
		t.Fatalf("block = %+v", block)
	}
	if raw, _ := base64.StdEncoding.DecodeString(block.Data); len(raw) != len(onePixelPNG) {
		t.Errorf("decoded %d bytes, want %d", len(raw), len(onePixelPNG))
	}
}

func TestFetchImageRejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.Client(), srv.URL, nil); err == nil {
		t.Fatal("expected an error for a non-image response")
	}
}

func TestFetchImageContentTypeFallsBackToExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(onePixelPNG)
	}))
	defer srv.Close()

	block, err := FetchImage(context.Background(), srv.Client(), srv.URL+"/pic.png?v=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if block.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", block.MediaType)
	}
}

func TestReadImageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, onePixelPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	block, err := ReadImageFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if block.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", block.MediaType)
	}

	txt := filepath.Join(t.TempDir(), "note.txt")
	_ = os.WriteFile(txt, []byte("hi"), 0o600)
	if _, err := ReadImageFile(txt, ""); err == nil {
		t.Error("a non-image file should be rejected")
	}
}

func TestDecodeDataURL(t *testing.T) {
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(onePixelPNG)
	block, err := DecodeDataURL(url)
	if err != nil {
		t.Fatal(err)
	}
	if block.Type != provider.BlockImage || block.MediaType != "image/png" {
		t.Fatalf("block = %+v", block)
	}

	for _, bad := range []string{
		"http://example.com/x.png",
		"data:text/plain;base64,aGk=",
		"data:image/png,notbase64",
		"data:image/png;base64,%%%not-base64%%%",
	} {
		if _, err := DecodeDataURL(bad); err == nil {
			t.Errorf("DecodeDataURL(%q) should have failed", bad)
		}
	}
}

func TestNormalizeImageType(t *testing.T) {
	cases := []struct{ ct, url, want string }{
		{"image/jpeg; charset=binary", "x", "image/jpeg"},
		{"application/octet-stream", "a/b/c.WEBP", "image/webp"},
		{"", "https://h/p.jpg?a=1#f", "image/jpeg"},
		{"", "noext", ""},
	}
	for _, c := range cases {
		if got := normalizeImageType(c.ct, c.url); got != c.want {
			t.Errorf("normalizeImageType(%q,%q) = %q, want %q", c.ct, c.url, got, c.want)
		}
	}
}

func TestFetchImageForwardsNoHeaderCleanly(t *testing.T) {
	// A nil header must not panic and must still fetch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Get("Authorization")) != 0 {
			t.Error("did not expect an auth header")
		}
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write([]byte("GIF89a"))
	}))
	defer srv.Close()

	block, err := FetchImage(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(block.MediaType, "image/") {
		t.Errorf("media type = %q", block.MediaType)
	}
}
