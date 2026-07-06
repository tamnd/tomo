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

// maxAudioBytes caps a single downloaded voice note. Voice notes are short, so
// this is generous while still bounding an untrusted download.
const maxAudioBytes = 25 << 20

// audioExt maps the audio media types channels see to a container extension,
// so the transcriber knows how to decode the bytes.
var audioExt = map[string]string{
	"audio/ogg":   ".ogg",
	"audio/opus":  ".ogg",
	"audio/mpeg":  ".mp3",
	"audio/mp4":   ".m4a",
	"audio/m4a":   ".m4a",
	"audio/x-m4a": ".m4a",
	"audio/aac":   ".aac",
	"audio/wav":   ".wav",
	"audio/x-wav": ".wav",
	"audio/webm":  ".webm",
	"audio/amr":   ".amr",
	"audio/x-caf": ".caf",
}

// FetchAudio downloads one voice note and returns it as a clip. header carries
// any auth the platform's file URL needs. A response that is too large is an
// error the caller can skip; anything else is taken as audio, since the
// transcriber decodes by content, not by a strict type check.
func FetchAudio(ctx context.Context, client *http.Client, url string, header http.Header) (Clip, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Clip{}, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Clip{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Clip{}, fmt.Errorf("fetch audio: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes+1))
	if err != nil {
		return Clip{}, err
	}
	if len(data) > maxAudioBytes {
		return Clip{}, fmt.Errorf("audio over %d bytes", maxAudioBytes)
	}
	return Clip{Data: data, Ext: audioExtOf(resp.Header.Get("Content-Type"), url)}, nil
}

// ReadAudioFile reads a voice note off disk, for the iMessage channel whose
// attachments are files.
func ReadAudioFile(path string) (Clip, error) {
	f, err := os.Open(path)
	if err != nil {
		return Clip{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxAudioBytes+1))
	if err != nil {
		return Clip{}, err
	}
	if len(data) > maxAudioBytes {
		return Clip{}, fmt.Errorf("audio over %d bytes", maxAudioBytes)
	}
	return Clip{Data: data, Ext: audioExtOf("", path)}, nil
}

// audioExtOf works out a container extension from the content type, falling
// back to the URL or path extension.
func audioExtOf(contentType, url string) string {
	if t := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]); audioExt[t] != "" {
		return audioExt[t]
	}
	if i := strings.IndexAny(url, "?#"); i >= 0 {
		url = url[:i]
	}
	if dot := strings.LastIndex(url, "."); dot >= 0 {
		return strings.ToLower(url[dot:])
	}
	return ""
}

// DecodeAudioDataURL turns a browser data: URL into a voice clip, the shape the
// webchat UI posts a recording as.
func DecodeAudioDataURL(s string) (Clip, error) {
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return Clip{}, fmt.Errorf("not a data url")
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return Clip{}, fmt.Errorf("malformed data url")
	}
	mediaType := meta
	isBase64 := false
	if i := strings.IndexByte(meta, ';'); i >= 0 {
		mediaType = meta[:i]
		isBase64 = strings.Contains(meta[i:], "base64")
	}
	if !strings.HasPrefix(mediaType, "audio/") {
		return Clip{}, fmt.Errorf("not audio: %s", mediaType)
	}
	if !isBase64 {
		return Clip{}, fmt.Errorf("data url must be base64")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return Clip{}, err
	}
	if len(data) > maxAudioBytes {
		return Clip{}, fmt.Errorf("audio over %d bytes", maxAudioBytes)
	}
	ext := audioExt[mediaType]
	if ext == "" {
		ext = ".webm" // MediaRecorder's usual container
	}
	return Clip{Data: data, Ext: ext}, nil
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
