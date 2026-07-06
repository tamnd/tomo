package channel

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/tamnd/tomo/pkg/provider"
)

// maxImageBytes caps a single downloaded image. Model APIs reject large images
// anyway, and an unbounded download is a memory risk from an untrusted sender.
const maxImageBytes = 10 << 20

// imageTypes are the media types the model backends accept.
var imageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// FetchImage downloads one image and returns it as a provider image block. It
// is the shared path every channel uses to turn an attachment into something
// the model can see. header carries any auth a platform's file URL needs.
// A non-image response, or one too large, is an error the caller can skip.
func FetchImage(ctx context.Context, client *http.Client, url string, header http.Header) (provider.Block, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return provider.Block{}, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return provider.Block{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return provider.Block{}, fmt.Errorf("fetch image: %s", resp.Status)
	}

	mediaType := normalizeImageType(resp.Header.Get("Content-Type"), url)
	if !imageTypes[mediaType] {
		return provider.Block{}, fmt.Errorf("not a supported image: %s", mediaType)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return provider.Block{}, err
	}
	if len(data) > maxImageBytes {
		return provider.Block{}, fmt.Errorf("image over %d bytes", maxImageBytes)
	}
	return provider.Block{
		Type:      provider.BlockImage,
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// ReadImageFile reads an image off disk and returns it as a model-ready block.
// iMessage stores attachments as files, so this is that channel's path to
// vision. declaredType is the mime type the database recorded, if any; the
// file extension is the fallback.
func ReadImageFile(path, declaredType string) (provider.Block, error) {
	mediaType := normalizeImageType(declaredType, path)
	if !imageTypes[mediaType] {
		return provider.Block{}, fmt.Errorf("not a supported image: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return provider.Block{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return provider.Block{}, err
	}
	if len(data) > maxImageBytes {
		return provider.Block{}, fmt.Errorf("image over %d bytes", maxImageBytes)
	}
	return provider.Block{
		Type:      provider.BlockImage,
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// DecodeDataURL turns a browser data: URL into an image block. The webchat UI
// posts pasted or attached images this way, so it never touches the network.
func DecodeDataURL(s string) (provider.Block, error) {
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return provider.Block{}, fmt.Errorf("not a data url")
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return provider.Block{}, fmt.Errorf("malformed data url")
	}
	mediaType := meta
	isBase64 := false
	if i := strings.IndexByte(meta, ';'); i >= 0 {
		mediaType = meta[:i]
		isBase64 = strings.Contains(meta[i:], "base64")
	}
	if !imageTypes[mediaType] {
		return provider.Block{}, fmt.Errorf("not a supported image: %s", mediaType)
	}
	if !isBase64 {
		return provider.Block{}, fmt.Errorf("data url must be base64")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return provider.Block{}, err
	}
	if len(data) > maxImageBytes {
		return provider.Block{}, fmt.Errorf("image over %d bytes", maxImageBytes)
	}
	return provider.Block{
		Type:      provider.BlockImage,
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// normalizeImageType prefers the response content type and falls back to the
// URL's extension, so a CDN that serves images as octet-stream still works.
func normalizeImageType(contentType, url string) string {
	if t := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]); imageTypes[t] {
		return t
	}
	return imageTypeFromExt(url)
}

func imageTypeFromExt(url string) string {
	if i := strings.IndexAny(url, "?#"); i >= 0 {
		url = url[:i]
	}
	dot := strings.LastIndex(url, ".")
	if dot < 0 {
		return ""
	}
	switch strings.ToLower(url[dot:]) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}
