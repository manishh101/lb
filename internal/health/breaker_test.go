package health

import (
	"testing"
	"time"
)

func TestBreaker_StateTransitions(t *testing.T) {
	// Create a breaker with threshold 3 and recovery timeout 100ms
	timeout := 100 * time.Millisecond
	b := NewBreaker(3, timeout)

	if b.State() != "CLOSED" {
		t.Errorf("Expected initial state to be CLOSED, got %s", b.State())
	}
	if b.IsOpen() {
		t.Errorf("Expected IsOpen() to be false initially")
	}
	if !b.CanSend() {
		t.Errorf("Expected CanSend() to be true initially")
	}

	// 1 failure - still closed
	b.RecordFailure()
	if b.State() != "CLOSED" {
		t.Errorf("Expected state to be CLOSED after 1 failure, got %s", b.State())
	}

	// 2 failures - still closed
	b.RecordFailure()
	if b.State() != "CLOSED" {
		t.Errorf("Expected state to be CLOSED after 2 failures, got %s", b.State())
	}

	// 3 failures - OPEN
	b.RecordFailure()
	if b.State() != "OPEN" {
		t.Errorf("Expected state to be OPEN after 3 failures, got %s", b.State())
	}
	if !b.IsOpen() {
		t.Errorf("Expected IsOpen() to be true")
	}
	if b.CanSend() {
		t.Errorf("Expected CanSend() to be false when OPEN and timeout not elapsed")
	}

	// Wait for recovery timeout to elapse
	time.Sleep(timeout + 10*time.Millisecond)

	// IsOpen() should be true regarding the state but mathematically returns false if time elapsed
	// Actually, IsOpen() in our implementation returns false if time elapsed:
	// "Still within recovery timeout -> truly open. return time.Since(b.lastFailureTime) <= b.recoveryTimeout"
	if b.IsOpen() {
		t.Errorf("Expected IsOpen() to be false after timeout elapsed (ready for probe)")
	}

	// CanSend should transition it to HALF_OPEN and return true
	if !b.CanSend() {
		t.Errorf("Expected CanSend() to be true after timeout elapsed")
	}
	if b.State() != "HALF_OPEN" {
		t.Errorf("Expected state to be HALF_OPEN after CanSend(), got %s", b.State())
	}

	// Subsequent CanSend() calls in HALF_OPEN should return true (our implementation allows this indefinitely until we record success/failure)
	if !b.CanSend() {
		t.Errorf("Expected CanSend() to be true when HALF_OPEN")
	}

	// Record success from the half-open probe
	b.RecordSuccess()
	if b.State() != "CLOSED" {
		t.Errorf("Expected state to be CLOSED after success in HALF_OPEN, got %s", b.State())
	}

	// Fail again to immediately trip (if threshold reached again, but we reset failure count)
	// So 1 failure shouldn't trip it if we reset it correctly
	b.RecordFailure()
	if b.State() == "OPEN" {
		t.Errorf("Expected state to be CLOSED after 1 failure following reset, got %s", b.State())
	}

	// Trip it again
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != "OPEN" {
		t.Errorf("Expected state to be OPEN after 3 failures again, got %s", b.State())
	}

	// Wait half the timeout, it's still open
	time.Sleep(timeout / 2)
	if !b.IsOpen() {
		t.Errorf("Expected IsOpen to be true before timeout elapsed")
	}

	// Wait full timeout + a bit
	time.Sleep(timeout / 2 + 10*time.Millisecond)

	// In HALF_OPEN, a failure should trip it immediately:
	b.CanSend() // transition to HALF_OPEN
	if b.State() != "HALF_OPEN" {
		t.Errorf("Expected state to be HALF_OPEN")
	}
	b.RecordFailure()
	if b.State() != "OPEN" {
		t.Errorf("Expected state to be OPEN immediately after failure in HALF_OPEN, got %s", b.State())
	}
}
