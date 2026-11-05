package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticAssetServerGzipsAndCachesHashedAssets(t *testing.T) {
	body := []byte(strings.Repeat("const timer = 'tracked';\n", 80))
	assets, err := newStaticAssetServer(fstest.MapFS{
		"build/assets/app-Abcdef12.js": &fstest.MapFile{Data: body},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/build/assets/app-Abcdef12.js", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	assets.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("gzip response missing Content-Encoding")
	}
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache control = %q", got)
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if string(decoded) != string(body) {
		t.Fatal("gzip body differs from source asset")
	}

	conditional := httptest.NewRequest(http.MethodGet, "/build/assets/app-Abcdef12.js", nil)
	conditional.Header.Set("Accept-Encoding", "gzip")
	conditional.Header.Set("If-None-Match", recorder.Header().Get("ETag"))
	conditionalResponse := httptest.NewRecorder()
	assets.ServeHTTP(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", conditionalResponse.Code)
	}
}

func TestStaticAssetServerHonorsGzipQualityAndRejectsUnknownAsset(t *testing.T) {
	assets, err := newStaticAssetServer(fstest.MapFS{
		"build/assets/app-Abcdef12.js": &fstest.MapFile{Data: []byte(strings.Repeat("x", 1024))},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/build/assets/app-Abcdef12.js", nil)
	request.Header.Set("Accept-Encoding", "gzip;q=0")
	response := httptest.NewRecorder()
	assets.ServeHTTP(response, request)
	if response.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip was selected despite q=0")
	}

	missing := httptest.NewRecorder()
	assets.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/build/assets/missing.js", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", missing.Code)
	}
}
