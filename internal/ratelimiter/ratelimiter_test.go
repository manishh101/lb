package ratelimiter

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiter_Middleware(t *testing.T) {
	// 10 RPS, burst of 2
	rl := New(10, 2)

	// A dummy handler that returns 200 OK
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := rl.Middleware(dummyHandler)

	// Since burst is 2, the first 2 requests should succeed immediately
	t.Run("Burst allowed", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "http://example.com/foo", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("Request %d expected 200 OK, got %d", i+1, w.Code)
			}
		}
	})

	// The 3rd request should fail immediately because tokens are exhausted
	t.Run("Rate limit exceeded", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com/foo", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("Expected 429 Too Many Requests, got %d", w.Code)
		}

		if retryAfter := w.Header().Get("Retry-After"); retryAfter != "1" {
			t.Errorf("Expected Retry-After header to be '1', got '%s'", retryAfter)
		}
	})
}
