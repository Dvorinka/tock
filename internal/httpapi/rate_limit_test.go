package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPRateLimiterBlocksAfterLimitAndAllowsReads(t *testing.T) {
	limiter := newIPRateLimiter(1, time.Minute)
	calls := 0
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/login", nil)
		r.RemoteAddr = "192.0.2.10:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	if got := request(http.MethodPost).Code; got != http.StatusNoContent {
		t.Fatalf("first POST status = %d, want %d", got, http.StatusNoContent)
	}
	limited := request(http.MethodPost)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("second POST status = %d, want %d", limited.Code, http.StatusTooManyRequests)
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("limited response is missing Retry-After")
	}
	if got := request(http.MethodGet).Code; got != http.StatusNoContent {
		t.Fatalf("GET status = %d, want %d", got, http.StatusNoContent)
	}
	if calls != 2 {
		t.Fatalf("next handler called %d times, want 2", calls)
	}
}
