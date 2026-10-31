package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

type staticAsset struct {
	contentType string
	body        []byte
	gzipBody    []byte
	etag        string
	gzipETag    string
}

type staticAssetServer struct {
	assets map[string]staticAsset
}

func newStaticAssetServer(public fs.FS) (*staticAssetServer, error) {
	server := &staticAssetServer{assets: make(map[string]staticAsset)}
	err := fs.WalkDir(public, "build/assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(public, name)
		if err != nil {
			return err
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		sum := sha256.Sum256(body)
		asset := staticAsset{
			contentType: contentType,
			body:        body,
			etag:        `"` + hex.EncodeToString(sum[:]) + `"`,
		}
		if len(body) >= 512 && compressibleAsset(contentType) {
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write(body); err != nil {
				return err
			}
			if err := writer.Close(); err != nil {
				return err
			}
			asset.gzipBody = compressed.Bytes()
			asset.gzipETag = `"` + hex.EncodeToString(sum[:]) + `-gzip"`
		}
		server.assets[strings.TrimPrefix(name, "build/assets/")] = asset
		return nil
	})
	if err != nil {
		return nil, err
	}
	return server, nil
}

func (s *staticAssetServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/build/assets/")
	if name == "." || strings.HasPrefix(name, "../") {
		http.NotFound(w, r)
		return
	}
	asset, ok := s.assets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, etag := asset.body, asset.etag
	if len(asset.gzipBody) > 0 && acceptsGzip(r.Header.Get("Accept-Encoding")) {
		body, etag = asset.gzipBody, asset.gzipETag
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
	}
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("ETag", etag)
	if isFingerprintFilename(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func compressibleAsset(contentType string) bool {
	return strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "javascript") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "svg+xml")
}

func acceptsGzip(header string) bool {
	for _, token := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(token), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		for _, param := range strings.Split(params, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(key, "q") && strings.TrimSpace(value) == "0" {
				return false
			}
		}
		return true
	}
	return false
}

func matchesETag(header, etag string) bool {
	for _, value := range strings.Split(header, ",") {
		value = strings.TrimSpace(value)
		if value == "*" || value == etag {
			return true
		}
	}
	return false
}

func isFingerprintFilename(name string) bool {
	base := path.Base(name)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	_, suffix, ok := strings.Cut(stem, "-")
	if !ok {
		return false
	}
	return len(suffix) >= 8
}
