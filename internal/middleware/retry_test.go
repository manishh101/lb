package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetry_SuccessfulRequest(t *testing.T) {
	callCount := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	mw := NewRetry(3, 50)
	handler := mw(backend)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rr.Code)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

func TestRetry_RetryOn5xx(t *testing.T) {
	callCount := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	mw := NewRetry(3, 10) // 10ms initial interval for fast tests
	handler := mw(backend)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 after retries, got %d", rr.Code)
	}
	if callCount != 3 {
		t.Errorf("Expected 3 calls (2 failures + 1 success), got %d", callCount)
	}
}

func TestRetry_NoRetryOn4xx(t *testing.T) {
	callCount := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	})

	mw := NewRetry(3, 10)
	handler := mw(backend)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", rr.Code)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 call (no retry on 4xx), got %d", callCount)
	}
}

func TestRetry_MaxAttemptsExhausted(t *testing.T) {
	callCount := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("always failing"))
	})

	mw := NewRetry(3, 10) // 3 max attempts, 10ms interval
	handler := mw(backend)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("Expected 500 when all retries exhausted, got %d", rr.Code)
	}
	if callCount != 3 {
		t.Errorf("Expected 3 calls (all attempts), got %d", callCount)
	}
}

func TestRetry_XAttemptsHeader(t *testing.T) {
	callCount := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount < 2 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mw := NewRetry(3, 10)
	handler := mw(backend)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rr.Code)
	}

	attempts := rr.Header().Get("X-Attempts")
	if attempts == "" {
		t.Error("Expected X-Attempts header in response")
	}
}

func TestRetry_SingleAttempt(t *testing.T) {
	callCount := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	})

	mw := NewRetry(1, 10)
	handler := mw(backend)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if callCount != 1 {
		t.Errorf("Expected 1 call with single attempt, got %d", callCount)
	}
}
